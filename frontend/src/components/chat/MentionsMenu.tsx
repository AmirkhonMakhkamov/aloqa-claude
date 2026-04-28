"use client";

import { useEffect, useMemo } from "react";
import { Avatar } from "@/components/ui/Avatar";
import { cn } from "@/lib/utils";
import type { User } from "@/lib/types";

/*
 * Mention autocomplete menu. The Composer owns the trigger/query/selection
 * state and hands us what to show; we render the list and call `onPick` when
 * the user clicks a row. Keyboard navigation also lives in the Composer so
 * `Enter`/`Tab`/arrows stay tied to the textarea focus — passing those keys
 * through the menu would fight React focus handling unnecessarily.
 *
 * Max 6 rows at a time. Anything more and people stop scanning anyway.
 */
const MAX_SUGGESTIONS = 6;

interface Props {
  query: string;
  members: User[];
  activeIndex: number;
  onHover: (index: number) => void;
  onPick: (user: User) => void;
}

export function MentionsMenu({ query, members, activeIndex, onHover, onPick }: Props) {
  const matches = useMemo(
    () => filterMembers(members, query).slice(0, MAX_SUGGESTIONS),
    [members, query],
  );

  // If the query no longer matches anything, hide entirely.
  if (matches.length === 0) return null;

  return (
    <MenuList
      matches={matches}
      activeIndex={activeIndex}
      onHover={onHover}
      onPick={onPick}
    />
  );
}

function MenuList({
  matches,
  activeIndex,
  onHover,
  onPick,
}: {
  matches: User[];
  activeIndex: number;
  onHover: (i: number) => void;
  onPick: (u: User) => void;
}) {
  // Scroll the active item into view when arrow keys move it.
  useEffect(() => {
    const el = document.getElementById(`mention-opt-${activeIndex}`);
    el?.scrollIntoView({ block: "nearest" });
  }, [activeIndex]);

  return (
    <div
      role="listbox"
      className="absolute bottom-full left-0 right-0 z-20 mb-2 max-h-[260px] overflow-y-auto rounded-xl border border-line bg-app p-1 shadow-lg"
    >
      <div className="px-2 pb-1 pt-1 text-[11px] font-semibold uppercase tracking-wider text-ink-3">
        People
      </div>
      {matches.map((m, i) => (
        <button
          key={m.id}
          id={`mention-opt-${i}`}
          type="button"
          role="option"
          aria-selected={i === activeIndex}
          onMouseEnter={() => onHover(i)}
          // We have to react to mousedown (not click) — the textarea would
          // blur first on click, which cancels the insertion sequence.
          onMouseDown={(e) => {
            e.preventDefault();
            onPick(m);
          }}
          className={cn(
            "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-[13px] transition",
            i === activeIndex
              ? "bg-accent-dim text-accent"
              : "text-ink-2 hover:bg-app-2 hover:text-ink",
          )}
        >
          <Avatar name={m.display_name} src={m.avatar_url ?? undefined} size={24} />
          <span className="truncate font-medium">{m.display_name}</span>
          <span className="ml-auto truncate text-[11px] text-ink-3">
            {m.email}
          </span>
        </button>
      ))}
    </div>
  );
}

/*
 * Simple relevance-ish ranking: startsWith beats contains, display name beats
 * email. Case-insensitive. If `query` is empty we return everyone — this lets
 * a bare `@` open the list so the user can browse.
 */
export function filterMembers(members: User[], query: string): User[] {
  const q = query.trim().toLowerCase();
  const active = members.filter((m) => m.status === "active");
  if (!q) return active.slice(0, 20);
  const scored = active
    .map((m) => ({ m, score: scoreMatch(m, q) }))
    .filter((s) => s.score > 0)
    .sort((a, b) => b.score - a.score);
  return scored.map((s) => s.m);
}

function scoreMatch(u: User, q: string): number {
  const name = u.display_name.toLowerCase();
  const email = u.email.toLowerCase();
  if (name.startsWith(q)) return 100;
  if (email.startsWith(q)) return 80;
  if (name.includes(q)) return 60;
  if (email.includes(q)) return 40;
  return 0;
}
