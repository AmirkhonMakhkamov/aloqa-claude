"use client";

import { useEffect, useMemo, useRef } from "react";
import { dayKey, formatDayLabel } from "@/lib/utils";
import type { Message, UUID } from "@/lib/types";
import { useMessages } from "@/stores/messages";
import { MessageItem } from "./MessageItem";

interface Props {
  wsId: UUID;
  chId: UUID;
  onOpenThread: (msg: Message) => void;
}

// Messages arrive newest-first from the backend. We flip them in render so the
// newest is at the bottom. We also auto-scroll to bottom when new messages
// arrive if the user is already near the bottom.
export function MessageList({ wsId, chId, onOpenThread }: Props) {
  const slice = useMessages((s) => s.byChannel[chId]);
  const loadInitial = useMessages((s) => s.loadInitial);
  const loadOlder = useMessages((s) => s.loadOlder);

  useEffect(() => {
    void loadInitial(wsId, chId);
  }, [wsId, chId, loadInitial]);

  const messages = slice?.messages ?? [];
  // Top-of-list sentinel (for loading older). Because the container is
  // flex-col-reverse, the sentinel appears visually at the top.
  const topRef = useRef<HTMLDivElement | null>(null);
  const scrollerRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    const el = topRef.current;
    const root = scrollerRef.current;
    if (!el || !root) return;
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          if (e.isIntersecting && slice?.hasMore && !slice.loading) {
            void loadOlder(wsId, chId);
          }
        }
      },
      { root, rootMargin: "200px" },
    );
    io.observe(el);
    return () => io.disconnect();
  }, [wsId, chId, slice?.hasMore, slice?.loading, loadOlder]);

  // Render top-level messages only; thread replies appear in the side panel.
  const topLevel = useMemo(
    () => messages.filter((m) => !m.parent_id),
    [messages],
  );

  if (!slice?.initialLoaded && slice?.loading) {
    return (
      <div className="flex h-full items-center justify-center gap-2 text-sm text-ink-3">
        <span className="h-3.5 w-3.5 animate-spin rounded-full border-2 border-line border-t-accent" />
        Loading messages…
      </div>
    );
  }

  if (topLevel.length === 0) {
    return (
      <div className="flex h-full items-center justify-center p-10 text-center">
        <div className="max-w-sm space-y-2">
          <div className="text-[15px] font-semibold text-ink">No messages yet</div>
          <p className="text-[13px] text-ink-2">
            Be the first to say hello. Messages you send will appear here in real time.
          </p>
        </div>
      </div>
    );
  }

  // Build a flat list with day separators. topLevel is newest-first; the
  // rendered container is flex-col-reverse, so emitting newest-first produces
  // the correct visual order bottom-up.
  const rows: Array<
    | { kind: "msg"; msg: Message; compact: boolean }
    | { kind: "day"; label: string; key: string }
  > = [];
  let prev: Message | null = null;
  for (const m of topLevel) {
    const nextDown = prev; // "below" this message visually
    const sameDay = nextDown && dayKey(nextDown.created_at) === dayKey(m.created_at);
    const sameAuthor =
      nextDown &&
      nextDown.user_id === m.user_id &&
      new Date(nextDown.created_at).getTime() - new Date(m.created_at).getTime() < 5 * 60_000 &&
      !nextDown.parent_id;
    rows.push({ kind: "msg", msg: m, compact: Boolean(sameAuthor && sameDay) });
    if (!sameDay && nextDown) {
      // Day changed — insert separator above the later (newer) message.
      rows.splice(rows.length - 1, 0, {
        kind: "day",
        label: formatDayLabel(nextDown.created_at),
        key: `day-${dayKey(nextDown.created_at)}`,
      });
    }
    prev = m;
  }
  // Finally, a separator for the oldest message in our view.
  if (prev) {
    rows.push({
      kind: "day",
      label: formatDayLabel(prev.created_at),
      key: `day-${dayKey(prev.created_at)}-first`,
    });
  }

  return (
    <div ref={scrollerRef} className="flex h-full flex-col-reverse overflow-y-auto">
      {rows.map((r) =>
        r.kind === "day" ? (
          <DaySeparator key={r.key} label={r.label} />
        ) : (
          <MessageItem
            key={r.msg.id}
            wsId={wsId}
            chId={chId}
            message={r.msg}
            compact={r.compact}
            onOpenThread={onOpenThread}
          />
        ),
      )}
      <div ref={topRef} className="h-10 shrink-0">
        {slice?.loading && slice.initialLoaded ? (
          <div className="py-3 text-center text-[12px] text-ink-3">Loading older…</div>
        ) : null}
        {slice && !slice.hasMore ? (
          <div className="py-3 text-center text-[11px] text-ink-3">
            Start of channel history
          </div>
        ) : null}
      </div>
    </div>
  );
}

function DaySeparator({ label }: { label: string }) {
  return (
    <div className="relative my-4 flex items-center px-6">
      <div className="h-px flex-1 bg-line" />
      <span className="mx-3 rounded-full border border-line bg-app px-3 py-0.5 text-[11px] font-medium text-ink-2">
        {label}
      </span>
      <div className="h-px flex-1 bg-line" />
    </div>
  );
}
