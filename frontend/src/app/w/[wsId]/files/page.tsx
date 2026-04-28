"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import {
  ArrowUpRight,
  FileText,
  FileVideo,
  Filter,
  Hash,
  ImageIcon,
  Loader2,
  Search,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { searchApi } from "@/lib/api/endpoints";
import type { SearchResult } from "@/lib/types";
import { useWorkspace } from "@/stores/workspace";

/*
 * Files view. The backend's full-text search is the only path to enumerate
 * attachments today (no workspace-scoped attachments list endpoint), so this
 * page is structured as a focused file-search surface rather than a Drive-
 * style folder browser. Type=file is pinned in the query, the user supplies
 * the keywords, and we render results as larger file rows with a preview
 * pane on the right.
 *
 * Filter chips (All / Images / Docs / Media / Other) narrow the result set
 * client-side using the `category` we infer from the filename — keeps the
 * server contract dumb and the UI snappy when toggling.
 */

type FileCategory = "all" | "image" | "doc" | "media" | "other";

interface FileRow {
  result: SearchResult;
  category: Exclude<FileCategory, "all">;
}

export default function FilesPage() {
  const { wsId } = useParams<{ wsId: string }>();
  const channels = useWorkspace((s) => s.channels);
  const refreshChannels = useWorkspace((s) => s.refreshChannels);

  const [q, setQ] = useState("");
  const [submitted, setSubmitted] = useState("");
  const [results, setResults] = useState<SearchResult[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [filter, setFilter] = useState<FileCategory>("all");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  // Workspace layout populates `channels` on activation, but a transient
  // failure (e.g. 429) can leave the store empty. We need channel names to
  // render the row chip + preview deep-link, so backfill if missing.
  useEffect(() => {
    if (channels.length === 0) void refreshChannels();
  }, [channels.length, refreshChannels]);

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
      const resp = await searchApi.query(wsId, term, { type: "file", limit: 50 });
      setResults(resp.results ?? []);
      setSelectedId(null);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "search failed");
      setResults([]);
    } finally {
      setLoading(false);
    }
  }

  const rows = useMemo<FileRow[]>(() => {
    if (!results) return [];
    return results.map((r) => ({ result: r, category: categoryFor(r.title) }));
  }, [results]);

  const filtered = useMemo(
    () => (filter === "all" ? rows : rows.filter((r) => r.category === filter)),
    [rows, filter],
  );

  const counts = useMemo(() => {
    const c: Record<FileCategory, number> = {
      all: rows.length,
      image: 0,
      doc: 0,
      media: 0,
      other: 0,
    };
    for (const r of rows) c[r.category] += 1;
    return c;
  }, [rows]);

  const selected = filtered.find((r) => r.result.id === selectedId) ?? filtered[0];

  return (
    <div className="flex h-full min-w-0 flex-col bg-app">
      <header className="flex h-[52px] shrink-0 items-center gap-3 border-b border-line bg-app px-5">
        <h1 className="text-[15px] font-semibold text-ink">Files</h1>
        <span className="text-[12px] text-ink-3">
          {results ? `${rows.length} matching` : "Search this workspace's attachments"}
        </span>
      </header>

      <div className="flex min-h-0 flex-1">
        <div className="flex min-w-0 flex-1 flex-col">
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
                placeholder="Search filenames or content…"
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
                label="Images"
                count={counts.image}
                active={filter === "image"}
                onClick={() => setFilter("image")}
              />
              <FilterChip
                label="Documents"
                count={counts.doc}
                active={filter === "doc"}
                onClick={() => setFilter("doc")}
              />
              <FilterChip
                label="Media"
                count={counts.media}
                active={filter === "media"}
                onClick={() => setFilter("media")}
              />
              <FilterChip
                label="Other"
                count={counts.other}
                active={filter === "other"}
                onClick={() => setFilter("other")}
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
              <NoMatches query={submitted} filter={filter} totalAcrossFilters={rows.length} />
            ) : (
              <ul className="divide-y divide-line">
                {filtered.map((row) => {
                  const channel = row.result.channel_id
                    ? channels.find((c) => c.id === row.result.channel_id)
                    : null;
                  return (
                    <FileRowItem
                      key={row.result.id}
                      row={row}
                      channel={channel ?? null}
                      wsId={wsId}
                      active={selected?.result.id === row.result.id}
                      onSelect={() => setSelectedId(row.result.id)}
                    />
                  );
                })}
              </ul>
            )}
          </div>
        </div>

        <aside className="hidden w-[320px] shrink-0 border-l border-line bg-app md:block">
          <PreviewPane
            row={selected ?? null}
            wsId={wsId}
            channel={
              selected?.result.channel_id
                ? channels.find((c) => c.id === selected.result.channel_id) ?? null
                : null
            }
          />
        </aside>
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

function FileRowItem({
  row,
  channel,
  wsId,
  active,
  onSelect,
}: {
  row: FileRow;
  channel: { id: string; name: string } | null;
  wsId: string;
  active: boolean;
  onSelect: () => void;
}) {
  const Icon = iconForCategory(row.category);
  const tint = tintForCategory(row.category);
  return (
    <li>
      <button
        type="button"
        onClick={onSelect}
        className={cn(
          "flex w-full items-center gap-3 px-5 py-3 text-left transition",
          active ? "bg-accent-dim/30" : "hover:bg-app-2",
        )}
      >
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
              {row.result.title || "Untitled"}
            </span>
            {channel ? (
              <Link
                href={`/w/${wsId}/c/${channel.id}`}
                onClick={(e) => e.stopPropagation()}
                className="inline-flex items-center gap-0.5 rounded-full bg-app-2 px-1.5 py-0.5 text-[10px] text-ink-2 hover:bg-accent-dim hover:text-accent"
              >
                <Hash className="h-2.5 w-2.5" />
                {channel.name}
              </Link>
            ) : null}
          </div>
          {row.result.snippet ? (
            <div className="mt-0.5 line-clamp-1 text-[11px] text-ink-3">
              <HighlightedSnippet text={row.result.snippet} />
            </div>
          ) : null}
        </div>
        <span className="shrink-0 text-[11px] text-ink-3">
          {formatDate(row.result.created_at)}
        </span>
      </button>
    </li>
  );
}

function PreviewPane({
  row,
  wsId,
  channel,
}: {
  row: FileRow | null;
  wsId: string;
  channel: { id: string; name: string } | null;
}) {
  if (!row) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-2 p-8 text-center text-ink-3">
        <div className="grid h-12 w-12 place-items-center rounded-full bg-app-2">
          <ImageIcon className="h-5 w-5" />
        </div>
        <div className="text-[13px] font-medium text-ink-2">Nothing selected</div>
        <div className="text-[12px]">
          Pick a file from the list to see details and preview options.
        </div>
      </div>
    );
  }

  const Icon = iconForCategory(row.category);
  const tint = tintForCategory(row.category);
  return (
    <div className="flex h-full flex-col">
      <div className="flex shrink-0 items-center justify-between border-b border-line px-5 py-3">
        <div className="text-[11px] font-semibold uppercase tracking-wider text-ink-2">
          File details
        </div>
        {channel ? (
          <Link
            href={`/w/${wsId}/c/${channel.id}`}
            className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-[12px] font-medium text-accent hover:bg-accent-dim"
          >
            Open in #{channel.name}
            <ArrowUpRight className="h-3.5 w-3.5" />
          </Link>
        ) : null}
      </div>
      <div className="flex-1 overflow-y-auto p-5">
        <div className={cn("mb-4 grid h-32 place-items-center rounded-xl", tint)}>
          <Icon className="h-12 w-12" />
        </div>
        <div className="text-[14px] font-semibold text-ink">
          {row.result.title || "Untitled"}
        </div>
        <dl className="mt-4 space-y-2 text-[12px]">
          <Field label="Type" value={labelForCategory(row.category)} />
          <Field label="Shared" value={formatDate(row.result.created_at)} />
          {channel ? <Field label="Channel" value={`#${channel.name}`} /> : null}
        </dl>
        {row.result.snippet ? (
          <div className="mt-4 rounded-lg border border-line bg-app-2 p-3 text-[12px] text-ink-2">
            <HighlightedSnippet text={row.result.snippet} />
          </div>
        ) : null}
      </div>
    </div>
  );
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between gap-3">
      <dt className="text-ink-3">{label}</dt>
      <dd className="truncate text-ink">{value}</dd>
    </div>
  );
}

function EmptyPrompt() {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 p-12 text-center">
      <div className="grid h-14 w-14 place-items-center rounded-full bg-accent-dim text-accent">
        <Search className="h-6 w-6" />
      </div>
      <div className="text-[15px] font-semibold text-ink">Find a file</div>
      <p className="max-w-sm text-[13px] text-ink-2">
        Search by filename or any text inside an attachment shared in this
        workspace. Filters narrow results by file kind.
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
  filter: FileCategory;
  totalAcrossFilters: number;
}) {
  const filtered = filter !== "all" && totalAcrossFilters > 0;
  return (
    <div className="flex h-full flex-col items-center justify-center gap-2 p-12 text-center text-ink-3">
      <div className="text-[14px] font-semibold text-ink-2">
        {filtered ? "No files match this filter" : `No files match "${query}"`}
      </div>
      <div className="text-[12px]">
        {filtered
          ? "Try the All filter or refine your query."
          : "Try a different keyword — search uses the file name and contents."}
      </div>
    </div>
  );
}

/*
 * Mark up a backend ts_headline snippet without trusting raw HTML. The
 * backend wraps matches in <mark>…</mark>; we split on that, render real
 * <mark> elements for the matches, and emit text for everything else.
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
            <mark
              key={i}
              className="rounded bg-accent-dim px-0.5 text-accent"
            >
              {m[1]}
            </mark>
          );
        }
        return <span key={i}>{chunk}</span>;
      })}
    </>
  );
}

const IMAGE_EXT = new Set(["png", "jpg", "jpeg", "gif", "webp", "svg", "heic", "bmp"]);
const DOC_EXT = new Set([
  "pdf",
  "doc",
  "docx",
  "xls",
  "xlsx",
  "ppt",
  "pptx",
  "txt",
  "md",
  "rtf",
  "csv",
  "tsv",
]);
const MEDIA_EXT = new Set([
  "mp3",
  "wav",
  "flac",
  "ogg",
  "m4a",
  "mp4",
  "mov",
  "webm",
  "mkv",
  "avi",
]);
function categoryFor(filename: string): Exclude<FileCategory, "all"> {
  const ext = filename.toLowerCase().split(".").pop() ?? "";
  if (IMAGE_EXT.has(ext)) return "image";
  if (DOC_EXT.has(ext)) return "doc";
  if (MEDIA_EXT.has(ext)) return "media";
  return "other";
}

function iconForCategory(c: Exclude<FileCategory, "all">) {
  // Inspected in the row + preview, kept synchronous to avoid layout flicker.
  switch (c) {
    case "image":
      return ImageIcon;
    case "doc":
      return FileText;
    case "media":
      return FileVideo;
    default:
      return FileText;
  }
}

// Quick visual cue for the icon tile. We deliberately keep the palette tiny
// — too many tints turns a simple file list into a confetti.
function tintForCategory(c: Exclude<FileCategory, "all">): string {
  switch (c) {
    case "image":
      return "bg-accent-dim text-accent";
    case "doc":
      return "bg-status-yellow/15 text-status-yellow";
    case "media":
      return "bg-status-green/15 text-status-green";
    default:
      return "bg-app-2 text-ink-2";
  }
}

function labelForCategory(c: Exclude<FileCategory, "all">): string {
  switch (c) {
    case "image":
      return "Image";
    case "doc":
      return "Document";
    case "media":
      return "Audio / Video";
    default:
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
