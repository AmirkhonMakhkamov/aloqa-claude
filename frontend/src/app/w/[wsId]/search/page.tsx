"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useParams, useRouter, useSearchParams } from "next/navigation";
import Link from "next/link";
import {
  AtSign,
  FileText,
  Filter,
  Hash,
  Loader2,
  MessageSquare,
  Search,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { searchApi } from "@/lib/api/endpoints";
import type { SearchResult } from "@/lib/types";
import { useWorkspace } from "@/stores/workspace";

/*
 * Workspace-wide search. The backend exposes a single endpoint that mixes
 * messages, channels, users, and files; this view fans those out into
 * client-side type chips so the user can scope without a round-trip. The
 * URL retains `?q=` so a result page is shareable.
 *
 * Result rows reuse the same icon-tile + highlighted-snippet pattern as
 * the Files view so the visual language across "search-driven" surfaces
 * stays consistent.
 */

type ResultType = SearchResult["type"];
type Filter = "all" | ResultType;

export default function SearchPage() {
  const { wsId } = useParams<{ wsId: string }>();
  const router = useRouter();
  const params = useSearchParams();
  const initial = params.get("q") ?? "";
  const channels = useWorkspace((s) => s.channels);
  const refreshChannels = useWorkspace((s) => s.refreshChannels);

  const [q, setQ] = useState(initial);
  const [submitted, setSubmitted] = useState(initial);
  const [filter, setFilter] = useState<Filter>("all");
  const [results, setResults] = useState<SearchResult[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  useEffect(() => {
    if (channels.length === 0) void refreshChannels();
  }, [channels.length, refreshChannels]);

  // Keep URL in sync so the result page is shareable.
  useEffect(() => {
    const qs = q ? `?q=${encodeURIComponent(q)}` : "";
    window.history.replaceState(null, "", `/w/${wsId}/search${qs}`);
  }, [q, wsId]);

  async function run(e?: React.FormEvent) {
    if (e) e.preventDefault();
    const term = q.trim();
    setSubmitted(term);
    if (!term) {
      setResults(null);
      setErr(null);
      return;
    }
    setLoading(true);
    setErr(null);
    try {
      const resp = await searchApi.query(wsId, term, { limit: 50 });
      setResults(resp.results ?? []);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "search failed");
      setResults([]);
    } finally {
      setLoading(false);
    }
  }

  // Auto-run on mount if there's an initial query so the URL is hydratable.
  useEffect(() => {
    if (initial) void run();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const counts = useMemo(() => {
    const c: Record<Filter, number> = {
      all: results?.length ?? 0,
      message: 0,
      channel: 0,
      user: 0,
      file: 0,
    };
    if (results) for (const r of results) c[r.type] += 1;
    return c;
  }, [results]);

  const filtered = useMemo(() => {
    if (!results) return [];
    return filter === "all" ? results : results.filter((r) => r.type === filter);
  }, [results, filter]);

  return (
    <div className="flex h-full min-w-0 flex-col bg-app">
      <header className="flex h-[52px] shrink-0 items-center gap-3 border-b border-line bg-app px-5">
        <h1 className="text-[15px] font-semibold text-ink">Search</h1>
        <span className="text-[12px] text-ink-3">
          {results
            ? `${results.length} result${results.length === 1 ? "" : "s"} for "${submitted}"`
            : "Find messages, channels, people, and files"}
        </span>
      </header>

      <form
        onSubmit={run}
        className="flex shrink-0 items-center gap-2 border-b border-line bg-app px-5 py-3"
      >
        <div className="relative flex-1">
          <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-ink-3" />
          <input
            ref={inputRef}
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Try: incident, weekly sync, @alice, report.pdf…"
            className="h-10 w-full rounded-lg border border-line bg-app-2 pl-9 pr-3 text-[14px] text-ink placeholder:text-ink-3 focus:border-accent focus:outline-none focus:ring-2 focus:ring-accent/20"
          />
        </div>
        <button
          type="submit"
          disabled={loading}
          className={cn(
            "inline-flex h-10 items-center gap-2 rounded-lg px-4 text-[13px] font-semibold transition",
            loading
              ? "cursor-wait bg-accent/70 text-white"
              : "bg-accent text-white hover:bg-accent-hover",
          )}
        >
          {loading ? (
            <Loader2 className="h-4 w-4 animate-spin" />
          ) : (
            <Search className="h-4 w-4" />
          )}
          Search
        </button>
      </form>

      {results !== null ? (
        <div className="flex shrink-0 items-center gap-2 border-b border-line bg-app px-5 py-2.5">
          <Filter className="h-3.5 w-3.5 text-ink-3" />
          <FilterChip
            label="All"
            count={counts.all}
            active={filter === "all"}
            onClick={() => setFilter("all")}
          />
          <FilterChip
            label="Messages"
            count={counts.message}
            active={filter === "message"}
            onClick={() => setFilter("message")}
          />
          <FilterChip
            label="Channels"
            count={counts.channel}
            active={filter === "channel"}
            onClick={() => setFilter("channel")}
          />
          <FilterChip
            label="People"
            count={counts.user}
            active={filter === "user"}
            onClick={() => setFilter("user")}
          />
          <FilterChip
            label="Files"
            count={counts.file}
            active={filter === "file"}
            onClick={() => setFilter("file")}
          />
        </div>
      ) : null}

      <div className="min-h-0 flex-1 overflow-y-auto">
        {err ? (
          <div className="m-5 rounded-lg border border-status-red/30 bg-status-red/5 p-4 text-[13px] text-status-red">
            {err}
          </div>
        ) : null}

        {results === null ? (
          <EmptyPrompt />
        ) : filtered.length === 0 ? (
          <NoMatches
            query={submitted}
            filter={filter}
            totalAcrossFilters={results.length}
          />
        ) : (
          <ul className="divide-y divide-line">
            {filtered.map((r) => {
              const channel = r.channel_id
                ? channels.find((c) => c.id === r.channel_id)
                : null;
              return (
                <ResultRow
                  key={`${r.type}-${r.id}`}
                  r={r}
                  wsId={wsId}
                  channel={channel ?? null}
                  onOpenChannel={(chId) => router.push(`/w/${wsId}/c/${chId}`)}
                />
              );
            })}
          </ul>
        )}
      </div>
    </div>
  );
}

function FilterChip({
  label,
  count,
  active,
  onClick,
}: {
  label: string;
  count: number;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full px-3 py-1 text-[12px] transition",
        active
          ? "bg-accent-dim text-accent"
          : "bg-app-2 text-ink-2 hover:bg-app-3 hover:text-ink",
      )}
    >
      {label}
      <span className={cn("text-[11px]", active ? "text-accent/70" : "text-ink-3")}>
        {count}
      </span>
    </button>
  );
}

function ResultRow({
  r,
  wsId,
  channel,
  onOpenChannel,
}: {
  r: SearchResult;
  wsId: string;
  channel: { id: string; name: string } | null;
  onOpenChannel: (chId: string) => void;
}) {
  const Icon = iconForType(r.type);
  const tint = tintForType(r.type);

  // Backend already emits a display-ready `title`. Fall back defensively.
  const title = useMemo(() => {
    if (r.title) return r.title;
    if (r.type === "channel" && channel) return `#${channel.name}`;
    return r.type;
  }, [r, channel]);

  // Users are display-only — no per-user route exists yet.
  const clickable = r.type !== "user";
  const onClick = () => {
    if (r.type === "message" && r.channel_id) onOpenChannel(r.channel_id);
    else if (r.type === "channel") onOpenChannel(r.id);
    else if (r.type === "file" && r.channel_id) onOpenChannel(r.channel_id);
  };

  const body = (
    <>
      <div
        className={cn(
          "grid h-10 w-10 shrink-0 place-items-center rounded-lg",
          tint,
        )}
      >
        <Icon className="h-5 w-5" />
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate text-[13px] font-medium text-ink">
            {title}
          </span>
          <span className="rounded-full bg-app-2 px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-ink-3">
            {labelForType(r.type)}
          </span>
          {(r.type === "message" || r.type === "file") && channel ? (
            <Link
              href={`/w/${wsId}/c/${channel.id}`}
              onClick={(ev) => ev.stopPropagation()}
              className="inline-flex items-center gap-0.5 rounded-full bg-app-2 px-1.5 py-0.5 text-[10px] text-ink-2 hover:bg-accent-dim hover:text-accent"
            >
              <Hash className="h-2.5 w-2.5" />
              {channel.name}
            </Link>
          ) : null}
        </div>
        {r.snippet ? (
          <div className="mt-0.5 line-clamp-2 text-[12px] text-ink-3">
            <HighlightedSnippet text={r.snippet} />
          </div>
        ) : null}
      </div>
      <span className="shrink-0 text-[11px] text-ink-3">
        {formatDate(r.created_at)}
      </span>
    </>
  );

  return (
    <li>
      {clickable ? (
        <button
          type="button"
          onClick={onClick}
          className="flex w-full items-center gap-3 px-5 py-3 text-left transition hover:bg-app-2"
        >
          {body}
        </button>
      ) : (
        <div className="flex items-center gap-3 px-5 py-3">{body}</div>
      )}
    </li>
  );
}

function EmptyPrompt() {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 p-12 text-center">
      <div className="grid h-14 w-14 place-items-center rounded-full bg-accent-dim text-accent">
        <Search className="h-6 w-6" />
      </div>
      <div className="text-[15px] font-semibold text-ink">Search this workspace</div>
      <p className="max-w-sm text-[13px] text-ink-2">
        Full-text search runs across messages, channels, people, and files
        you can access. Type a query and hit Search to begin.
      </p>
    </div>
  );
}

function NoMatches({
  query,
  filter,
  totalAcrossFilters,
}: {
  query: string;
  filter: Filter;
  totalAcrossFilters: number;
}) {
  const filtered = filter !== "all" && totalAcrossFilters > 0;
  return (
    <div className="flex h-full flex-col items-center justify-center gap-2 p-12 text-center text-ink-3">
      <div className="text-[14px] font-semibold text-ink-2">
        {filtered ? "No results match this filter" : `No results for "${query}"`}
      </div>
      <div className="text-[12px]">
        {filtered
          ? "Try the All filter or refine your query."
          : "Try a different keyword, or check spelling and access."}
      </div>
    </div>
  );
}

/*
 * Backend ts_headline wraps matches in <mark>…</mark>. React escapes raw
 * strings, so we split on the marker and render real <mark> nodes — no raw
 * HTML, no injection surface.
 */
function HighlightedSnippet({ text }: { text: string }) {
  if (!text) return null;
  const parts = text.split(/(<mark>[\s\S]*?<\/mark>)/g);
  return (
    <>
      {parts.map((chunk, i) => {
        const m = chunk.match(/^<mark>([\s\S]*?)<\/mark>$/);
        if (m) {
          return (
            <mark key={i} className="rounded bg-accent-dim px-0.5 text-accent">
              {m[1]}
            </mark>
          );
        }
        return <span key={i}>{chunk}</span>;
      })}
    </>
  );
}

function iconForType(t: ResultType) {
  switch (t) {
    case "message":
      return MessageSquare;
    case "channel":
      return Hash;
    case "user":
      return AtSign;
    case "file":
      return FileText;
  }
}

function tintForType(t: ResultType): string {
  switch (t) {
    case "message":
      return "bg-accent-dim text-accent";
    case "channel":
      return "bg-status-green/15 text-status-green";
    case "user":
      return "bg-status-yellow/15 text-status-yellow";
    case "file":
      return "bg-app-2 text-ink-2";
  }
}

function labelForType(t: ResultType): string {
  switch (t) {
    case "message":
      return "Message";
    case "channel":
      return "Channel";
    case "user":
      return "Person";
    case "file":
      return "File";
  }
}

function formatDate(iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "—";
  const diffSec = Math.max(0, Math.round((Date.now() - t) / 1000));
  if (diffSec < 60) return "just now";
  const min = Math.round(diffSec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.round(min / 60);
  if (hr < 24) return `${hr}h ago`;
  const day = Math.round(hr / 24);
  if (day < 7) return `${day}d ago`;
  return new Date(t).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
  });
}
