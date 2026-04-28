"use client";

import type { UUID } from "@/lib/types";
import { useMembers, shortId } from "@/stores/members";
import { useMessages, type TypingUser } from "@/stores/messages";

interface Props {
  wsId: UUID;
  chId: UUID;
}

// Stable empty-array ref. Returning `[]` inline from a zustand selector creates
// a new reference every call and blows up useSyncExternalStore with
// "The result of getSnapshot should be cached to avoid an infinite loop".
const EMPTY_TYPERS: TypingUser[] = [];

export function TypingIndicator({ wsId, chId }: Props) {
  const typers = useMessages((s) => s.typing[chId] ?? EMPTY_TYPERS);
  const lookup = useMembers((s) => s.byWorkspace[wsId]);

  if (typers.length === 0) {
    return <div className="h-5" />; // reserve space to avoid layout jitter
  }

  const names = typers.map((t) => lookup?.[t.userId]?.display_name ?? shortId(t.userId));
  const label =
    names.length === 1
      ? `${names[0]} is typing…`
      : names.length === 2
        ? `${names[0]} and ${names[1]} are typing…`
        : `${names.length} people are typing…`;

  return (
    <div className="flex h-5 shrink-0 items-center gap-2 px-6 text-[11px] text-ink-3">
      <span className="flex gap-0.5">
        <Dot delay="0ms" />
        <Dot delay="150ms" />
        <Dot delay="300ms" />
      </span>
      <span className="truncate">{label}</span>
    </div>
  );
}

function Dot({ delay }: { delay: string }) {
  return (
    <span
      className="h-1 w-1 animate-pulse rounded-full bg-ink-3"
      style={{ animationDelay: delay }}
    />
  );
}
