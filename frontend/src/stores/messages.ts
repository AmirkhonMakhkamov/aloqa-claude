// Per-channel message state, with optimistic writes and WebSocket reconciliation.
//
// Shape notes:
// - Backend returns messages DESC by id (newest first). We store them in that
//   order and render the scroll container reversed (flex-col-reverse) so the
//   newest is visually at the bottom without extra work.
// - Reactions/attachments are not yet populated by the list endpoint, so
//   historical messages start empty on those fields. We apply reaction and
//   attachment updates as WS events come in.
// - Thread replies live in a separate map keyed by the parent message id. We
//   don't intersperse them in the channel feed — Slack-style side panel.

import { create } from "zustand";
import { messagesApi } from "@/lib/api/endpoints";
import type { Message, Reaction, UUID } from "@/lib/types";
import { WS, type ServerEvent } from "@/lib/ws/events";

// ── Payload shapes we care about (mirrored from Go events package) ──
interface MessagePayload {
  message: Message;
}
interface ReactionPayload {
  message_id: UUID;
  channel_id: UUID;
  user_id: UUID;
  emoji: string;
}
interface PinPayload {
  message_id: UUID;
  channel_id: UUID;
  user_id: UUID;
}
interface TypingPayload {
  channel_id: UUID;
  user_id: UUID;
}

export interface TypingUser {
  userId: UUID;
  expiresAt: number;
}

interface ChannelSlice {
  messages: Message[];
  cursor: string | null;
  hasMore: boolean;
  loading: boolean;
  initialLoaded: boolean;
}

interface ThreadSlice {
  replies: Message[];
  loading: boolean;
  loaded: boolean;
}

interface MessagesState {
  byChannel: Record<UUID, ChannelSlice>;
  threads: Record<UUID, ThreadSlice>; // keyed by parent message id
  typing: Record<UUID, TypingUser[]>; // keyed by channel id

  loadInitial: (wsId: UUID, chId: UUID) => Promise<void>;
  loadOlder: (wsId: UUID, chId: UUID) => Promise<void>;
  send: (wsId: UUID, chId: UUID, content: string, parent_id?: UUID) => Promise<void>;
  edit: (wsId: UUID, chId: UUID, msgId: UUID, content: string) => Promise<void>;
  remove: (wsId: UUID, chId: UUID, msgId: UUID) => Promise<void>;
  toggleReaction: (
    wsId: UUID,
    chId: UUID,
    msgId: UUID,
    emoji: string,
    selfId: UUID,
  ) => Promise<void>;
  togglePin: (wsId: UUID, chId: UUID, msgId: UUID, currentlyPinned: boolean) => Promise<void>;
  loadThread: (wsId: UUID, chId: UUID, parentId: UUID) => Promise<void>;

  applyEvent: (evt: ServerEvent) => void;
  noteTyping: (chId: UUID, userId: UUID) => void;
  clearStaleTyping: () => void;
  resetChannel: (chId: UUID) => void;
}

const emptyChannel: ChannelSlice = {
  messages: [],
  cursor: null,
  hasMore: true,
  loading: false,
  initialLoaded: false,
};

export const useMessages = create<MessagesState>((set, get) => ({
  byChannel: {},
  threads: {},
  typing: {},

  async loadInitial(wsId, chId) {
    const slice = get().byChannel[chId] ?? emptyChannel;
    if (slice.initialLoaded || slice.loading) return;
    set({
      byChannel: { ...get().byChannel, [chId]: { ...slice, loading: true } },
    });
    try {
      const page = await messagesApi.list(wsId, chId, undefined, 50);
      set({
        byChannel: {
          ...get().byChannel,
          [chId]: {
            messages: page.items,
            cursor: page.next_cursor ?? null,
            hasMore: page.has_more,
            loading: false,
            initialLoaded: true,
          },
        },
      });
    } catch {
      set({
        byChannel: {
          ...get().byChannel,
          [chId]: { ...slice, loading: false, initialLoaded: true },
        },
      });
    }
  },

  async loadOlder(wsId, chId) {
    const slice = get().byChannel[chId];
    if (!slice || !slice.hasMore || slice.loading || !slice.cursor) return;
    set({
      byChannel: { ...get().byChannel, [chId]: { ...slice, loading: true } },
    });
    try {
      const page = await messagesApi.list(wsId, chId, slice.cursor, 50);
      const existingIds = new Set(slice.messages.map((m) => m.id));
      const appended = [
        ...slice.messages,
        ...page.items.filter((m) => !existingIds.has(m.id)),
      ];
      set({
        byChannel: {
          ...get().byChannel,
          [chId]: {
            messages: appended,
            cursor: page.next_cursor ?? null,
            hasMore: page.has_more,
            loading: false,
            initialLoaded: true,
          },
        },
      });
    } catch {
      set({
        byChannel: { ...get().byChannel, [chId]: { ...slice, loading: false } },
      });
    }
  },

  async send(wsId, chId, content, parent_id) {
    // The server emits a message.created event, which applyEvent handles
    // idempotently. We don't prepend optimistically here because a duplicate
    // of the same id would just be merged; keeping the flow simple.
    const msg = await messagesApi.send(wsId, chId, content, parent_id);
    if (parent_id) {
      // Thread reply — append to thread slice (newest at bottom in thread view).
      const thread = get().threads[parent_id] ?? { replies: [], loading: false, loaded: true };
      if (!thread.replies.some((m) => m.id === msg.id)) {
        set({
          threads: {
            ...get().threads,
            [parent_id]: { ...thread, replies: [...thread.replies, msg] },
          },
        });
      }
    }
    // Non-thread: event handler will prepend.
  },

  async edit(wsId, chId, msgId, content) {
    const updated = await messagesApi.edit(wsId, chId, msgId, content);
    mergeMessage(set, get, chId, updated);
  },

  async remove(wsId, chId, msgId) {
    await messagesApi.delete(wsId, chId, msgId);
    // The event path marks deleted_at; we do the same here in case the event
    // is delayed or lost.
    patchMessage(set, get, chId, msgId, {
      deleted_at: new Date().toISOString(),
      content: "",
    });
  },

  async toggleReaction(wsId, chId, msgId, emoji, selfId) {
    const current = findMessage(get(), chId, msgId);
    const hasMine =
      current?.reactions?.some(
        (r) => r.emoji === emoji && r.user_id === selfId,
      ) ?? false;
    if (hasMine) {
      await messagesApi.unreact(wsId, chId, msgId, emoji);
    } else {
      await messagesApi.react(wsId, chId, msgId, emoji);
    }
    // Optimistic-ish: apply locally immediately; server event reconciles.
    patchReaction(set, get, chId, msgId, emoji, selfId, !hasMine);
  },

  async togglePin(wsId, chId, msgId, currentlyPinned) {
    if (currentlyPinned) {
      await messagesApi.unpin(wsId, chId, msgId);
    } else {
      await messagesApi.pin(wsId, chId, msgId);
    }
    patchMessage(set, get, chId, msgId, { pinned: !currentlyPinned });
  },

  async loadThread(wsId, chId, parentId) {
    const slice = get().threads[parentId];
    if (slice?.loaded || slice?.loading) return;
    set({
      threads: {
        ...get().threads,
        [parentId]: { replies: slice?.replies ?? [], loading: true, loaded: false },
      },
    });
    try {
      const page = await messagesApi.thread(wsId, chId, parentId, undefined, 100);
      set({
        threads: {
          ...get().threads,
          [parentId]: {
            // Thread list returns oldest-first naturally enough for the panel.
            replies: page.items.slice().reverse(),
            loading: false,
            loaded: true,
          },
        },
      });
    } catch {
      set({
        threads: {
          ...get().threads,
          [parentId]: { replies: slice?.replies ?? [], loading: false, loaded: true },
        },
      });
    }
  },

  applyEvent(evt) {
    switch (evt.type) {
      case WS.MessageCreated: {
        const { message } = evt.payload as MessagePayload;
        if (!message) return;
        if (message.parent_id) {
          // Append to thread slice; bump reply_count on parent.
          const thread = get().threads[message.parent_id] ?? {
            replies: [],
            loading: false,
            loaded: false,
          };
          if (!thread.replies.some((m) => m.id === message.id)) {
            set({
              threads: {
                ...get().threads,
                [message.parent_id]: {
                  ...thread,
                  replies: [...thread.replies, message],
                },
              },
            });
          }
          // Bump parent's reply_count optimistically.
          const parent = findMessage(get(), message.channel_id, message.parent_id);
          if (parent) {
            patchMessage(set, get, message.channel_id, message.parent_id, {
              reply_count: (parent.reply_count ?? 0) + 1,
            });
          }
        } else {
          prependMessage(set, get, message.channel_id, message);
        }
        return;
      }
      case WS.MessageUpdated: {
        const { message } = evt.payload as MessagePayload;
        if (!message) return;
        mergeMessage(set, get, message.channel_id, message);
        return;
      }
      case WS.MessageDeleted: {
        const { message } = evt.payload as MessagePayload;
        if (!message) return;
        patchMessage(set, get, message.channel_id, message.id, {
          deleted_at: message.deleted_at ?? new Date().toISOString(),
          content: "",
        });
        return;
      }
      case WS.ReactionAdded: {
        const p = evt.payload as ReactionPayload;
        patchReaction(set, get, p.channel_id, p.message_id, p.emoji, p.user_id, true);
        return;
      }
      case WS.ReactionRemoved: {
        const p = evt.payload as ReactionPayload;
        patchReaction(set, get, p.channel_id, p.message_id, p.emoji, p.user_id, false);
        return;
      }
      case WS.MessagePinned: {
        const p = evt.payload as PinPayload;
        patchMessage(set, get, p.channel_id, p.message_id, { pinned: true });
        return;
      }
      case WS.MessageUnpinned: {
        const p = evt.payload as PinPayload;
        patchMessage(set, get, p.channel_id, p.message_id, { pinned: false });
        return;
      }
      case WS.TypingStarted: {
        const p = evt.payload as TypingPayload;
        get().noteTyping(p.channel_id, p.user_id);
        return;
      }
    }
  },

  noteTyping(chId, userId) {
    const arr = get().typing[chId] ?? [];
    const kept = arr.filter((t) => t.userId !== userId);
    kept.push({ userId, expiresAt: Date.now() + 4000 });
    set({ typing: { ...get().typing, [chId]: kept } });
  },

  clearStaleTyping() {
    const now = Date.now();
    const next: Record<UUID, TypingUser[]> = {};
    let changed = false;
    for (const [chId, arr] of Object.entries(get().typing)) {
      const kept = arr.filter((t) => t.expiresAt > now);
      if (kept.length !== arr.length) changed = true;
      if (kept.length) next[chId] = kept;
    }
    if (changed) set({ typing: next });
  },

  resetChannel(chId) {
    const copy = { ...get().byChannel };
    delete copy[chId];
    set({ byChannel: copy });
  },
}));

// ── Helpers (pure functions that mutate via set) ─────────────────────

type Setter = (updater: Partial<MessagesState>) => void;
type Getter = () => MessagesState;

function findMessage(state: MessagesState, chId: UUID, msgId: UUID): Message | null {
  const slice = state.byChannel[chId];
  if (slice) {
    const hit = slice.messages.find((m) => m.id === msgId);
    if (hit) return hit;
  }
  for (const t of Object.values(state.threads)) {
    const hit = t.replies.find((m) => m.id === msgId);
    if (hit) return hit;
  }
  return null;
}

function prependMessage(set: Setter, get: Getter, chId: UUID, msg: Message) {
  const slice = get().byChannel[chId] ?? emptyChannel;
  if (slice.messages.some((m) => m.id === msg.id)) return;
  set({
    byChannel: {
      ...get().byChannel,
      [chId]: { ...slice, messages: [msg, ...slice.messages] },
    },
  });
}

function mergeMessage(set: Setter, get: Getter, chId: UUID, msg: Message) {
  const slice = get().byChannel[chId];
  if (slice) {
    const idx = slice.messages.findIndex((m) => m.id === msg.id);
    if (idx >= 0) {
      const copy = slice.messages.slice();
      copy[idx] = { ...copy[idx], ...msg };
      set({
        byChannel: { ...get().byChannel, [chId]: { ...slice, messages: copy } },
      });
    }
  }
  // Also update thread copies if this message lives there.
  const threads = get().threads;
  let touched = false;
  const next: Record<UUID, ThreadSlice> = {};
  for (const [k, t] of Object.entries(threads)) {
    const idx = t.replies.findIndex((m) => m.id === msg.id);
    if (idx >= 0) {
      touched = true;
      const copy = t.replies.slice();
      copy[idx] = { ...copy[idx], ...msg };
      next[k] = { ...t, replies: copy };
    } else {
      next[k] = t;
    }
  }
  if (touched) set({ threads: next });
}

function patchMessage(
  set: Setter,
  get: Getter,
  chId: UUID,
  msgId: UUID,
  patch: Partial<Message>,
) {
  const slice = get().byChannel[chId];
  if (slice) {
    const idx = slice.messages.findIndex((m) => m.id === msgId);
    if (idx >= 0) {
      const copy = slice.messages.slice();
      copy[idx] = { ...copy[idx], ...patch };
      set({
        byChannel: { ...get().byChannel, [chId]: { ...slice, messages: copy } },
      });
    }
  }
  // Patch in thread slices too.
  const threads = get().threads;
  let touched = false;
  const next: Record<UUID, ThreadSlice> = {};
  for (const [k, t] of Object.entries(threads)) {
    const idx = t.replies.findIndex((m) => m.id === msgId);
    if (idx >= 0) {
      touched = true;
      const copy = t.replies.slice();
      copy[idx] = { ...copy[idx], ...patch };
      next[k] = { ...t, replies: copy };
    } else {
      next[k] = t;
    }
  }
  if (touched) set({ threads: next });
}

function patchReaction(
  set: Setter,
  get: Getter,
  chId: UUID,
  msgId: UUID,
  emoji: string,
  userId: UUID,
  add: boolean,
) {
  const existing = findMessage(get(), chId, msgId);
  if (!existing) return;
  const reactions = applyReaction(
    existing.reactions ?? [],
    msgId,
    emoji,
    userId,
    add,
  );
  patchMessage(set, get, chId, msgId, { reactions });
}

// The server stores each (user, emoji) as a separate row. We mirror that —
// aggregation happens in the renderer via `aggregateReactions`.
function applyReaction(
  list: Reaction[],
  msgId: UUID,
  emoji: string,
  userId: UUID,
  add: boolean,
): Reaction[] {
  const existsIdx = list.findIndex(
    (r) => r.emoji === emoji && r.user_id === userId,
  );
  if (add) {
    if (existsIdx >= 0) return list; // already present; no-op
    return [
      ...list,
      {
        id: `local-${msgId}-${userId}-${emoji}`,
        message_id: msgId,
        user_id: userId,
        emoji,
        created_at: new Date().toISOString(),
      },
    ];
  }
  if (existsIdx < 0) return list;
  return list.filter((_, i) => i !== existsIdx);
}
