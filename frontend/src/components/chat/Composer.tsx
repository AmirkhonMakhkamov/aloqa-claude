"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { Paperclip, Send, X } from "lucide-react";
import { cn } from "@/lib/utils";
import type { User, UUID } from "@/lib/types";
import { filesApi } from "@/lib/api/endpoints";
import { rt } from "@/lib/ws/client";
import { useAuth } from "@/stores/auth";
import { useMembers } from "@/stores/members";
import { useMessages } from "@/stores/messages";
import { MentionsMenu, filterMembers } from "./MentionsMenu";

/*
 * The message composer. Visually it's a single rounded card sitting on an
 * `app-2` surface: attachment preview chip sits above the input, attach
 * button + textarea + send button live inside the same bordered shell.
 *
 * It also owns the @mention autocomplete. We watch the textarea's caret and
 * look backwards for an `@` that isn't preceded by a word character — if we
 * find one, everything between that `@` and the caret is the live query. The
 * menu pops up above the textarea; ↑/↓ move the active row, Enter/Tab pick,
 * Esc closes without picking. When the user picks a member we splice
 * `@{display_name_with_underscores} ` into the text and move the caret past
 * the inserted segment so they can keep typing.
 *
 * Wiring-wise Enter sends (unless the mention menu is open), Shift+Enter
 * newlines, typing pings throttle to 2.5s, attachments upload against the
 * freshly-created message after we spot it via the WS event.
 */
interface Props {
  wsId: UUID;
  chId: UUID;
  placeholder?: string;
  parentId?: UUID;
  /** When set, we'll upload an attachment against the freshly-sent message
   *  before clearing the composer. */
  allowAttachments?: boolean;
}

interface MentionTrigger {
  /** Index of the `@` in the string. */
  start: number;
  /** Caret position (end of the query). */
  end: number;
  /** Text between `@` and the caret (exclusive). */
  query: string;
}

export function Composer({
  wsId,
  chId,
  placeholder,
  parentId,
  allowAttachments = true,
}: Props) {
  const send = useMessages((s) => s.send);
  const self = useAuth((s) => s.user);
  const membersMap = useMembers((s) => s.byWorkspace[wsId]);
  const ensureMembers = useMembers((s) => s.ensureLoaded);

  const [text, setText] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const [mention, setMention] = useState<MentionTrigger | null>(null);
  const [mentionIdx, setMentionIdx] = useState(0);
  const taRef = useRef<HTMLTextAreaElement | null>(null);
  const fileRef = useRef<HTMLInputElement | null>(null);

  // Warm the roster — mentions need it, and everyone else benefits too.
  useEffect(() => {
    void ensureMembers(wsId);
  }, [wsId, ensureMembers]);

  // Personal workspaces 403 on /admin/members, so the roster cache may be
  // empty. Merge `self` in as a safe fallback — worst case mentions still
  // resolve to the current user, which is the only member anyway.
  const members = useMemo(() => {
    const byId: Record<string, User> = { ...(membersMap ?? {}) };
    if (self && !byId[self.id]) byId[self.id] = self;
    return Object.values(byId);
  }, [membersMap, self]);
  const mentionMatches = useMemo(
    () => (mention ? filterMembers(members, mention.query).slice(0, 6) : []),
    [mention, members],
  );
  // Clamp the highlighted row if the list shrinks mid-query.
  useEffect(() => {
    if (mentionIdx >= mentionMatches.length) {
      setMentionIdx(Math.max(0, mentionMatches.length - 1));
    }
  }, [mentionMatches.length, mentionIdx]);

  // Auto-resize textarea up to 8 rows.
  useEffect(() => {
    const el = taRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, 240)}px`;
  }, [text]);

  // Reset when switching channel/thread.
  useEffect(() => {
    setText("");
    setFile(null);
    setMention(null);
    setMentionIdx(0);
  }, [wsId, chId, parentId]);

  function recomputeMention() {
    const el = taRef.current;
    if (!el) return;
    const caret = el.selectionStart ?? el.value.length;
    const trigger = detectMentionTrigger(el.value, caret);
    setMention(trigger);
    if (!trigger) setMentionIdx(0);
  }

  function onChange(e: React.ChangeEvent<HTMLTextAreaElement>) {
    setText(e.target.value);
    // selectionStart is updated synchronously after React's state flush;
    // recompute on the next tick so we see the current caret position.
    queueMicrotask(recomputeMention);
  }

  function pickMention(user: User) {
    if (!mention) return;
    const el = taRef.current;
    if (!el) return;
    const token = `@${user.display_name.replace(/\s+/g, "_")} `;
    const before = text.slice(0, mention.start);
    const after = text.slice(mention.end);
    const next = before + token + after;
    setText(next);
    setMention(null);
    setMentionIdx(0);
    // Restore caret just past the inserted token on the next tick.
    queueMicrotask(() => {
      if (!taRef.current) return;
      const pos = before.length + token.length;
      taRef.current.focus();
      taRef.current.setSelectionRange(pos, pos);
    });
  }

  async function submit() {
    const content = text.trim();
    if (!content && !file) return;
    if (busy) return;
    setBusy(true);
    try {
      // If we only have a file, we still need some content for the backend —
      // fall back to the file name.
      const body = content || (file ? file.name : "");
      await send(wsId, chId, body, parentId);
      if (file) {
        const msgId = await findLatestOwnMessage(chId, parentId, body);
        if (msgId) {
          await filesApi.upload(wsId, chId, msgId, file).catch(() => undefined);
        }
      }
      setText("");
      setFile(null);
      setMention(null);
      setMentionIdx(0);
      if (fileRef.current) fileRef.current.value = "";
    } catch {
      // swallow — toast system will surface server errors later
    } finally {
      setBusy(false);
    }
  }

  function onKeyDown(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    // When the mention menu is open, intercept keys that drive it.
    if (mention && mentionMatches.length > 0) {
      if (e.key === "ArrowDown") {
        e.preventDefault();
        setMentionIdx((i) => (i + 1) % mentionMatches.length);
        return;
      }
      if (e.key === "ArrowUp") {
        e.preventDefault();
        setMentionIdx(
          (i) => (i - 1 + mentionMatches.length) % mentionMatches.length,
        );
        return;
      }
      if (e.key === "Enter" || e.key === "Tab") {
        e.preventDefault();
        pickMention(mentionMatches[mentionIdx]);
        return;
      }
      if (e.key === "Escape") {
        e.preventDefault();
        setMention(null);
        return;
      }
    }

    if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault();
      void submit();
    } else {
      maybeSendTyping(chId);
    }
  }

  const hasPayload = text.trim().length > 0 || Boolean(file);

  return (
    <div className="relative shrink-0 border-t border-line bg-app-2 px-4 py-3">
      {mention && mentionMatches.length > 0 ? (
        <MentionsMenu
          query={mention.query}
          members={members}
          activeIndex={mentionIdx}
          onHover={setMentionIdx}
          onPick={pickMention}
        />
      ) : null}

      {file ? (
        <div className="mb-2 flex items-center gap-2 rounded-md border border-line bg-app px-3 py-2 text-[12px] text-ink">
          <Paperclip className="h-3.5 w-3.5 text-ink-3" />
          <span className="truncate">{file.name}</span>
          <span className="text-ink-3">({(file.size / 1024).toFixed(1)} KB)</span>
          <button
            type="button"
            className="ml-auto grid h-6 w-6 place-items-center rounded text-ink-3 transition hover:bg-status-red/10 hover:text-status-red"
            onClick={() => {
              setFile(null);
              if (fileRef.current) fileRef.current.value = "";
            }}
            aria-label="Remove attachment"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        </div>
      ) : null}

      <div className="flex items-end gap-2 rounded-xl border border-line bg-app px-3 py-2 shadow-sm transition focus-within:border-accent focus-within:ring-2 focus-within:ring-accent/20">
        {allowAttachments ? (
          <>
            <input
              ref={fileRef}
              type="file"
              hidden
              onChange={(e) => setFile(e.target.files?.[0] ?? null)}
            />
            <button
              type="button"
              onClick={() => fileRef.current?.click()}
              className="grid h-8 w-8 shrink-0 place-items-center rounded-md text-ink-2 transition hover:bg-app-2 hover:text-ink disabled:cursor-not-allowed disabled:opacity-50"
              aria-label="Attach file"
              disabled={busy}
            >
              <Paperclip className="h-4 w-4" />
            </button>
          </>
        ) : null}
        <textarea
          ref={taRef}
          rows={1}
          value={text}
          onChange={onChange}
          onKeyDown={onKeyDown}
          onKeyUp={recomputeMention}
          onClick={recomputeMention}
          onBlur={() => setMention(null)}
          placeholder={placeholder ?? "Send a message"}
          disabled={busy}
          className="min-h-[24px] flex-1 resize-none bg-transparent py-1 text-[14px] leading-relaxed text-ink outline-none placeholder:text-ink-3"
        />
        <button
          type="button"
          onClick={() => void submit()}
          disabled={busy || !hasPayload}
          className={cn(
            "grid h-8 w-8 shrink-0 place-items-center rounded-md transition",
            hasPayload
              ? "bg-accent text-white hover:bg-accent-hover"
              : "bg-app-2 text-ink-3",
            busy && "cursor-wait opacity-70",
          )}
          aria-label="Send"
        >
          <Send className="h-4 w-4" />
        </button>
      </div>
      <div className="mt-1.5 px-1 text-[11px] text-ink-3">
        Enter to send · Shift+Enter for newline · @ to mention
      </div>
    </div>
  );
}

/*
 * Walk back from the caret to the nearest `@`. It counts as a mention trigger
 * if: there's no whitespace between the `@` and the caret, the character
 * before the `@` is start-of-string or whitespace, and the query is ≤ 30 chars
 * (users don't type `@foobarbazextremelylong`).
 */
function detectMentionTrigger(value: string, caret: number): MentionTrigger | null {
  const before = value.slice(0, caret);
  const atIdx = before.lastIndexOf("@");
  if (atIdx < 0) return null;
  const query = before.slice(atIdx + 1);
  if (query.length > 30) return null;
  if (/\s/.test(query)) return null;
  const prev = atIdx === 0 ? "" : value[atIdx - 1];
  if (prev && !/\s/.test(prev)) return null;
  return { start: atIdx, end: caret, query };
}

// ── Typing throttle ───────────────────────────────────────────────────
const lastTyping = new Map<string, number>();
function maybeSendTyping(chId: string) {
  const now = Date.now();
  const prev = lastTyping.get(chId) ?? 0;
  if (now - prev < 2500) return;
  lastTyping.set(chId, now);
  rt().typing(chId);
}

// ── Attachment linkage ────────────────────────────────────────────────
// After send(), the WS event populates our own message. Find it so we can
// attach the uploaded file. Falls back to undefined if we can't spot it.
async function findLatestOwnMessage(
  chId: string,
  parentId: string | undefined,
  content: string,
): Promise<string | undefined> {
  const deadline = Date.now() + 2000;
  while (Date.now() < deadline) {
    const state = useMessages.getState();
    const pool = parentId
      ? state.threads[parentId]?.replies ?? []
      : state.byChannel[chId]?.messages ?? [];
    // Iterate from newest to oldest. In the top-level array that's index 0
    // (we prepend). In the thread we append, so check the tail.
    const list = parentId ? pool.slice().reverse() : pool;
    for (const m of list) {
      if (m.content === content) return m.id;
    }
    await new Promise((r) => setTimeout(r, 80));
  }
  return undefined;
}
