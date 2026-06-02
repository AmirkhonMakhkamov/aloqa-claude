"use client";

import { useState } from "react";
import {
  MessageSquare,
  MoreHorizontal,
  Paperclip,
  Pencil,
  Pin,
  SmilePlus,
  Trash2,
} from "lucide-react";
import { Avatar } from "@/components/ui/Avatar";
import { cn, formatChatTime } from "@/lib/utils";
import { aggregateReactions, type Message, type UUID } from "@/lib/types";
import { useAuth } from "@/stores/auth";
import { useMembers, shortId } from "@/stores/members";
import { useMessages } from "@/stores/messages";

/*
 * Chat row. The hover toolbar, reaction chips and attachment pills all live
 * in the same flex row; the toolbar floats absolute-top-right and only becomes
 * interactive on hover so it never steals clicks from the message body.
 *
 * Accent chrome in light mode: the Aloqa palette uses `bg-accent-dim`
 * (≈10% accent alpha) for subtle accent surfaces and solid `text-accent` for
 * labels — no dedicated "soft" variant like the old dark scheme used.
 */
const QUICK_EMOJI = ["👍", "🎉", "🙌", "🔥", "❤️", "😄", "🚀", "🤔"];

interface Props {
  wsId: UUID;
  chId: UUID;
  message: Message;
  compact?: boolean; // merge with previous message from same author
  onOpenThread?: (msg: Message) => void;
}

export function MessageItem({ wsId, chId, message, compact, onOpenThread }: Props) {
  const self = useAuth((s) => s.user);
  const memberUser = useMembers((s) => s.get(wsId, message.user_id));
  const toggleReaction = useMessages((s) => s.toggleReaction);
  const togglePin = useMessages((s) => s.togglePin);
  const remove = useMessages((s) => s.remove);
  const edit = useMessages((s) => s.edit);

  const [emojiOpen, setEmojiOpen] = useState(false);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(message.content);

  const isSelf = self?.id === message.user_id;
  // Prefer the embedded `user` (freshest) → roster cache → self → truncated id.
  const authorUser = message.user ?? memberUser;
  const name =
    authorUser?.display_name ??
    (isSelf ? self?.display_name : undefined) ??
    shortId(message.user_id);
  const isDeleted = Boolean(message.deleted_at);
  const reactions = aggregateReactions(message.reactions);
  const isEdited = message.edited || Boolean(message.edited_at);

  async function saveEdit() {
    const next = draft.trim();
    setEditing(false);
    if (!next || next === message.content) return;
    await edit(wsId, chId, message.id, next).catch(() => undefined);
  }

  return (
    <div
      className={cn(
        "group relative flex gap-3 px-6 py-1.5 transition hover:bg-app-2",
        compact ? "pt-0.5" : "pt-3",
      )}
    >
      {compact ? (
        <div className="w-9 shrink-0 pt-1 text-[10px] text-ink-3 opacity-0 group-hover:opacity-100">
          {new Date(message.created_at).toLocaleTimeString(undefined, {
            hour: "2-digit",
            minute: "2-digit",
          })}
        </div>
      ) : (
        <Avatar
          name={name}
          src={authorUser?.avatar_url ?? undefined}
          color={authorUser?.avatar_color}
          size={36}
        />
      )}

      <div className="min-w-0 flex-1">
        {compact ? null : (
          <div className="flex items-baseline gap-2">
            <span className="text-[14px] font-semibold text-ink">{name}</span>
            <span className="text-[11px] text-ink-3">
              {formatChatTime(message.created_at)}
            </span>
            {isEdited ? (
              <span className="text-[10px] text-ink-3">(edited)</span>
            ) : null}
            {message.pinned ? (
              <span className="inline-flex items-center gap-1 rounded-full bg-accent-dim px-2 py-0.5 text-[10px] font-medium text-accent">
                <Pin className="h-3 w-3" />
                Pinned
              </span>
            ) : null}
          </div>
        )}

        {isDeleted ? (
          <div className="text-[13px] italic text-ink-3">
            This message was deleted.
          </div>
        ) : editing ? (
          <div className="mt-1 space-y-2">
            <textarea
              autoFocus
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Escape") {
                  setEditing(false);
                  setDraft(message.content);
                }
                if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                  e.preventDefault();
                  void saveEdit();
                }
              }}
              className="w-full rounded-md border border-line bg-app-2 p-2 text-[13px] text-ink outline-none transition focus-visible:border-accent focus-visible:ring-2 focus-visible:ring-accent/20"
              rows={Math.min(6, draft.split("\n").length + 1)}
            />
            <div className="flex items-center gap-3 text-[12px] text-ink-3">
              <button
                type="button"
                onClick={saveEdit}
                className="rounded-md bg-accent px-2 py-0.5 text-white transition hover:bg-accent-hover"
              >
                Save
              </button>
              <button
                type="button"
                onClick={() => {
                  setEditing(false);
                  setDraft(message.content);
                }}
                className="hover:text-ink"
              >
                Cancel
              </button>
              <span className="ml-auto">⌘↵ to save · esc to cancel</span>
            </div>
          </div>
        ) : (
          <MessageContent content={message.content} />
        )}

        {reactions.length > 0 ? (
          <div className="mt-1 flex flex-wrap gap-1">
            {reactions.map((r) => {
              const mine = self ? r.user_ids.includes(self.id) : false;
              return (
                <button
                  key={r.emoji}
                  type="button"
                  onClick={() =>
                    self && toggleReaction(wsId, chId, message.id, r.emoji, self.id)
                  }
                  className={cn(
                    "flex items-center gap-1 rounded-full border px-1.5 py-0.5 text-[11px] transition",
                    mine
                      ? "border-accent/40 bg-accent-dim text-accent"
                      : "border-line bg-app text-ink-2 hover:border-accent/30 hover:bg-accent-dim hover:text-accent",
                  )}
                >
                  <span>{r.emoji}</span>
                  <span className="tabular-nums">{r.count}</span>
                </button>
              );
            })}
          </div>
        ) : null}

        {message.attachments && message.attachments.length > 0 ? (
          <div className="mt-1.5 flex flex-wrap gap-2">
            {message.attachments.map((a) => (
              <a
                key={a.id}
                href={a.url ?? "#"}
                target="_blank"
                rel="noreferrer noopener"
                className="inline-flex items-center gap-2 rounded-md border border-line bg-app-2 px-2 py-1 text-[12px] text-ink transition hover:border-accent/30 hover:bg-accent-dim hover:text-accent"
              >
                <Paperclip className="h-3 w-3 text-ink-3" />
                <span className="max-w-[180px] truncate">{a.file_name}</span>
                <span className="text-ink-3">{formatBytes(a.file_size)}</span>
              </a>
            ))}
          </div>
        ) : null}

        {message.reply_count && message.reply_count > 0 && !message.parent_id ? (
          <button
            type="button"
            onClick={() => onOpenThread?.(message)}
            className="mt-1 inline-flex items-center gap-1 rounded-md bg-accent-dim px-2 py-0.5 text-[12px] font-medium text-accent transition hover:bg-accent/20"
          >
            <MessageSquare className="h-3 w-3" />
            {message.reply_count} {message.reply_count === 1 ? "reply" : "replies"}
          </button>
        ) : null}
      </div>

      {/* Hover toolbar */}
      {!isDeleted && !editing ? (
        <div className="pointer-events-none absolute right-4 top-1 opacity-0 transition group-hover:pointer-events-auto group-hover:opacity-100">
          <div className="flex items-center gap-0.5 rounded-md border border-line bg-app p-0.5 text-ink-2 shadow-sm">
            <div className="relative">
              <button
                type="button"
                className="grid h-7 w-7 place-items-center rounded transition hover:bg-app-2 hover:text-ink"
                onClick={() => setEmojiOpen((v) => !v)}
                aria-label="Add reaction"
              >
                <SmilePlus className="h-3.5 w-3.5" />
              </button>
              {emojiOpen ? (
                <div className="absolute right-0 top-8 z-10 flex gap-1 rounded-md border border-line bg-app p-1 shadow-lg">
                  {QUICK_EMOJI.map((e) => (
                    <button
                      key={e}
                      type="button"
                      className="rounded p-1 transition hover:bg-app-2"
                      onClick={(ev) => {
                        ev.stopPropagation();
                        setEmojiOpen(false);
                        if (self) {
                          void toggleReaction(wsId, chId, message.id, e, self.id);
                        }
                      }}
                    >
                      {e}
                    </button>
                  ))}
                </div>
              ) : null}
            </div>
            {!message.parent_id ? (
              <button
                type="button"
                className="grid h-7 w-7 place-items-center rounded transition hover:bg-app-2 hover:text-ink"
                onClick={() => onOpenThread?.(message)}
                aria-label="Reply in thread"
              >
                <MessageSquare className="h-3.5 w-3.5" />
              </button>
            ) : null}
            <button
              type="button"
              className="grid h-7 w-7 place-items-center rounded transition hover:bg-app-2 hover:text-ink"
              onClick={() =>
                void togglePin(wsId, chId, message.id, Boolean(message.pinned))
              }
              aria-label={message.pinned ? "Unpin" : "Pin"}
            >
              <Pin
                className={cn(
                  "h-3.5 w-3.5",
                  message.pinned && "fill-accent text-accent",
                )}
              />
            </button>
            {isSelf ? (
              <>
                <button
                  type="button"
                  className="grid h-7 w-7 place-items-center rounded transition hover:bg-app-2 hover:text-ink"
                  onClick={() => setEditing(true)}
                  aria-label="Edit"
                >
                  <Pencil className="h-3.5 w-3.5" />
                </button>
                <button
                  type="button"
                  className="grid h-7 w-7 place-items-center rounded transition hover:bg-status-red/10 hover:text-status-red"
                  onClick={() => {
                    if (confirm("Delete this message?")) {
                      void remove(wsId, chId, message.id);
                    }
                  }}
                  aria-label="Delete"
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </button>
              </>
            ) : (
              <button
                type="button"
                className="grid h-7 w-7 place-items-center rounded text-ink-3"
                aria-label="More"
                disabled
                title="More actions coming soon"
              >
                <MoreHorizontal className="h-3.5 w-3.5" />
              </button>
            )}
          </div>
        </div>
      ) : null}
    </div>
  );
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(1)} GB`;
}

/*
 * Minimal inline renderer — no markdown parser, just the three tokens we
 * actually care about:
 *   - http(s) URLs become accent-coloured links
 *   - `@name` (stored with underscores by the composer) become accent pills
 *   - everything else flows as plain text, preserving newlines
 * We tokenize in one pass so adjacent matches don't nest. Priority order
 * inside the regex alternation: URL → @mention. URLs win because they may
 * legitimately contain `@` characters.
 */
const TOKEN_RE = /(https?:\/\/[^\s]+)|(@[A-Za-z0-9_.-]+)/g;

function MessageContent({ content }: { content: string }) {
  const out: React.ReactNode[] = [];
  let idx = 0;
  let match: RegExpExecArray | null;
  TOKEN_RE.lastIndex = 0;
  while ((match = TOKEN_RE.exec(content)) !== null) {
    if (match.index > idx) {
      out.push(<span key={`t${idx}`}>{content.slice(idx, match.index)}</span>);
    }
    const [, url, mention] = match;
    if (url) {
      out.push(
        <a
          key={`u${match.index}`}
          href={url}
          target="_blank"
          rel="noreferrer noopener"
          className="text-accent underline underline-offset-2 hover:text-accent-hover"
        >
          {url}
        </a>,
      );
    } else if (mention) {
      out.push(
        <span
          key={`m${match.index}`}
          className="rounded-md bg-accent-dim px-1 py-0.5 text-[13px] font-medium text-accent"
        >
          {mention}
        </span>,
      );
    }
    idx = match.index + match[0].length;
  }
  if (idx < content.length) {
    out.push(<span key={`t${idx}-end`}>{content.slice(idx)}</span>);
  }
  return (
    <div className="whitespace-pre-wrap break-words text-[14px] leading-relaxed text-ink">
      {out}
    </div>
  );
}
