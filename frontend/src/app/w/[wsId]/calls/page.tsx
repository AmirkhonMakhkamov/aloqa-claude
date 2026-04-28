"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useParams, useRouter } from "next/navigation";
import {
  Briefcase,
  Loader2,
  Phone,
  PhoneCall,
  Plus,
  Radio,
  Users,
  Video,
  X,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { callsApi } from "@/lib/api/endpoints";
import type { Call, CallType } from "@/lib/types";

/*
 * Calls landing surface — split into "Live right now" and "Recent". The
 * page polls every 15s so a call somebody else just started shows up
 * without a manual refresh; the WS bridge will eventually push these
 * events but the polling fallback is cheap and keeps the page useful in
 * pre-WS mode too.
 *
 * "New meeting" reveals an inline composer rather than navigating away —
 * the meeting is created server-side and the user is bounced into the
 * call detail route on success.
 */

type Bucket = "live" | "recent";

export default function CallsPage() {
  const router = useRouter();
  const { wsId } = useParams<{ wsId: string }>();

  const [calls, setCalls] = useState<Call[] | null>(null);
  const [creating, setCreating] = useState(false);
  const [title, setTitle] = useState("");
  const [type, setType] = useState<CallType>("meeting");
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const titleRef = useRef<HTMLInputElement | null>(null);

  async function refresh() {
    try {
      // Backend serializes an empty list as `null` (Go nil slice). Coerce.
      const list = (await callsApi.list(wsId)) ?? [];
      setCalls(list);
    } catch (e) {
      setCalls((prev) => prev ?? []);
      setErr(e instanceof Error ? e.message : "could not load calls");
    }
  }

  useEffect(() => {
    void refresh();
    const id = window.setInterval(refresh, 15000);
    return () => window.clearInterval(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wsId]);

  useEffect(() => {
    if (creating) titleRef.current?.focus();
  }, [creating]);

  async function onCreate(e: React.FormEvent) {
    e.preventDefault();
    setSubmitting(true);
    setErr(null);
    try {
      // Enable recording + screen-sharing on every call we create from the
      // UI. Both gates are off by default server-side; opting in here
      // means the in-call control bar (record button, share-screen
      // button) is functional without a follow-up settings round-trip.
      const call = await callsApi.start(wsId, {
        type,
        title,
        settings: {
          recording: true,
          screen_sharing: true,
          breakout_rooms: true,
        },
      });
      router.push(`/w/${wsId}/calls/${call.id}`);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "could not start");
    } finally {
      setSubmitting(false);
    }
  }

  // Backend transitions ringing → active when the first participant joins
  // the SFU. We treat ringing as "live" for the host so a meeting they
  // just created doesn't flash into Recent before anyone arrives.
  const { live, recent } = useMemo(() => {
    const ls: Call[] = [];
    const rs: Call[] = [];
    for (const c of calls ?? []) {
      if (c.status === "ended") rs.push(c);
      else ls.push(c);
    }
    rs.sort((a, b) => Date.parse(b.started_at) - Date.parse(a.started_at));
    ls.sort((a, b) => Date.parse(b.started_at) - Date.parse(a.started_at));
    return { live: ls, recent: rs };
  }, [calls]);

  return (
    <div className="flex h-full min-w-0 flex-col bg-app">
      <header className="flex h-[52px] shrink-0 items-center justify-between gap-3 border-b border-line bg-app px-5">
        <div className="flex items-center gap-3">
          <h1 className="text-[15px] font-semibold text-ink">Meetings</h1>
          <span className="text-[12px] text-ink-3">
            {live.length > 0
              ? `${live.length} live now`
              : "Start an instant meeting or join one in progress"}
          </span>
        </div>
        {!creating ? (
          <button
            type="button"
            onClick={() => setCreating(true)}
            className="inline-flex h-8 items-center gap-2 rounded-lg bg-accent px-3 text-[13px] font-semibold text-white transition hover:bg-accent-hover"
          >
            <Plus className="h-4 w-4" />
            New meeting
          </button>
        ) : null}
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-4xl space-y-8 px-6 py-6">
          {creating ? (
            <CreateCard
              title={title}
              setTitle={setTitle}
              type={type}
              setType={setType}
              submitting={submitting}
              err={err}
              onCancel={() => {
                setCreating(false);
                setTitle("");
                setErr(null);
              }}
              onSubmit={onCreate}
              titleRef={titleRef}
            />
          ) : null}

          {err && !creating ? (
            <div className="rounded-lg border border-status-red/30 bg-status-red/5 p-3 text-[13px] text-status-red">
              {err}
            </div>
          ) : null}

          <Section
            title="Live right now"
            count={live.length}
            empty={
              <EmptyTile
                icon={<Radio className="h-5 w-5" />}
                title="No one's in a meeting yet"
                hint="Start a new meeting to invite teammates."
              />
            }
          >
            {live.map((c) => (
              <CallRow
                key={c.id}
                call={c}
                wsId={wsId}
                bucket="live"
                onOpen={(id) => router.push(`/w/${wsId}/calls/${id}`)}
              />
            ))}
          </Section>

          <Section
            title="Recent"
            count={recent.length}
            empty={
              <EmptyTile
                icon={<Phone className="h-5 w-5" />}
                title="Nothing recent"
                hint="Past meetings will appear here once they end."
              />
            }
          >
            {recent.slice(0, 25).map((c) => (
              <CallRow
                key={c.id}
                call={c}
                wsId={wsId}
                bucket="recent"
                onOpen={(id) => router.push(`/w/${wsId}/calls/${id}`)}
              />
            ))}
          </Section>
        </div>
      </div>
    </div>
  );
}

function Section({
  title,
  count,
  empty,
  children,
}: {
  title: string;
  count: number;
  empty: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <section>
      <div className="mb-3 flex items-baseline gap-2">
        <h2 className="text-[11px] font-semibold uppercase tracking-wider text-ink-2">
          {title}
        </h2>
        <span className="text-[11px] text-ink-3">
          {count === 0 ? "" : count}
        </span>
      </div>
      {count === 0 ? empty : <ul className="space-y-2">{children}</ul>}
    </section>
  );
}

function CreateCard({
  title,
  setTitle,
  type,
  setType,
  submitting,
  err,
  onCancel,
  onSubmit,
  titleRef,
}: {
  title: string;
  setTitle: (v: string) => void;
  type: CallType;
  setType: (v: CallType) => void;
  submitting: boolean;
  err: string | null;
  onCancel: () => void;
  onSubmit: (e: React.FormEvent) => void;
  titleRef: React.RefObject<HTMLInputElement | null>;
}) {
  return (
    <form
      onSubmit={onSubmit}
      className="rounded-xl border border-line bg-app-2 p-5"
    >
      <div className="mb-4 flex items-center justify-between">
        <div className="text-[13px] font-semibold text-ink">New meeting</div>
        <button
          type="button"
          onClick={onCancel}
          className="grid h-7 w-7 place-items-center rounded-md text-ink-3 transition hover:bg-app-3 hover:text-ink"
          aria-label="Close"
        >
          <X className="h-4 w-4" />
        </button>
      </div>

      <label className="mb-1 block text-[11px] font-medium text-ink-2">
        Title
      </label>
      <input
        ref={titleRef}
        required
        value={title}
        onChange={(e) => setTitle(e.target.value)}
        placeholder="Eng weekly sync"
        className="mb-4 h-10 w-full rounded-lg border border-line bg-app px-3 text-[14px] text-ink placeholder:text-ink-3 focus:border-accent focus:outline-none focus:ring-2 focus:ring-accent/20"
      />

      <label className="mb-2 block text-[11px] font-medium text-ink-2">
        Type
      </label>
      <div className="mb-4 grid grid-cols-2 gap-2 sm:grid-cols-4">
        <TypeChoice
          value="meeting"
          current={type}
          onSelect={setType}
          label="Meeting"
          hint="Scheduled or open"
          Icon={Video}
        />
        <TypeChoice
          value="group"
          current={type}
          onSelect={setType}
          label="Group call"
          hint="Quick huddle"
          Icon={Users}
        />
        <TypeChoice
          value="one_to_one"
          current={type}
          onSelect={setType}
          label="1:1 call"
          hint="With a teammate"
          Icon={Phone}
        />
        <TypeChoice
          value="webinar"
          current={type}
          onSelect={setType}
          label="Webinar"
          hint="One-to-many"
          Icon={Briefcase}
        />
      </div>

      {err ? (
        <div className="mb-3 rounded-md border border-status-red/30 bg-status-red/5 p-2.5 text-[12px] text-status-red">
          {err}
        </div>
      ) : null}

      <div className="flex items-center justify-end gap-2">
        <button
          type="button"
          onClick={onCancel}
          className="inline-flex h-9 items-center rounded-lg px-3 text-[13px] font-medium text-ink-2 transition hover:bg-app-3 hover:text-ink"
        >
          Cancel
        </button>
        <button
          type="submit"
          disabled={submitting || !title.trim()}
          className={cn(
            "inline-flex h-9 items-center gap-2 rounded-lg px-4 text-[13px] font-semibold text-white transition",
            submitting || !title.trim()
              ? "cursor-not-allowed bg-accent/40"
              : "bg-accent hover:bg-accent-hover",
          )}
        >
          {submitting ? <Loader2 className="h-4 w-4 animate-spin" /> : null}
          Start meeting
        </button>
      </div>
    </form>
  );
}

function TypeChoice({
  value,
  current,
  onSelect,
  label,
  hint,
  Icon,
}: {
  value: CallType;
  current: CallType;
  onSelect: (v: CallType) => void;
  label: string;
  hint: string;
  Icon: React.ComponentType<{ className?: string }>;
}) {
  const active = value === current;
  return (
    <button
      type="button"
      onClick={() => onSelect(value)}
      className={cn(
        "flex flex-col items-start gap-1 rounded-lg border px-3 py-2.5 text-left transition",
        active
          ? "border-accent bg-accent-dim text-accent"
          : "border-line bg-app text-ink-2 hover:border-accent/40 hover:text-ink",
      )}
    >
      <Icon className="h-4 w-4" />
      <div className={cn("text-[12px] font-semibold", active ? "text-accent" : "text-ink")}>
        {label}
      </div>
      <div className={cn("text-[11px]", active ? "text-accent/70" : "text-ink-3")}>
        {hint}
      </div>
    </button>
  );
}

function CallRow({
  call,
  bucket,
  onOpen,
}: {
  call: Call;
  wsId: string;
  bucket: Bucket;
  onOpen: (id: string) => void;
}) {
  const live = bucket === "live";
  const Icon = iconForType(call.type);
  const stateText =
    call.status === "ringing"
      ? "Waiting for participants"
      : call.status === "active"
        ? "Ongoing"
        : call.ended_at
          ? `Ended · ${durationText(call.started_at, call.ended_at)}`
          : "Ended";
  return (
    <li>
      <button
        type="button"
        onClick={() => onOpen(call.id)}
        className="flex w-full items-center gap-3 rounded-xl border border-line bg-app-2 px-4 py-3 text-left transition hover:border-accent/30 hover:bg-app-3"
      >
        <div
          className={cn(
            "grid h-10 w-10 shrink-0 place-items-center rounded-lg",
            live ? "bg-status-green/15 text-status-green" : "bg-app-3 text-ink-2",
          )}
        >
          <Icon className="h-5 w-5" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="truncate text-[14px] font-semibold text-ink">
              {call.title || "Untitled"}
            </span>
            {live ? (
              <span className="inline-flex items-center gap-1 rounded-full bg-status-green/15 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wider text-status-green">
                <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-status-green" />
                Live
              </span>
            ) : null}
          </div>
          <div className="mt-0.5 text-[12px] text-ink-3">
            {labelForType(call.type)} · {stateText} · {timeAgo(call.started_at)}
          </div>
        </div>
        <span
          className={cn(
            "inline-flex h-8 items-center gap-1.5 rounded-lg px-3 text-[12px] font-semibold transition",
            live
              ? "bg-accent text-white hover:bg-accent-hover"
              : "border border-line bg-app text-ink-2 hover:border-accent/40 hover:text-ink",
          )}
        >
          <PhoneCall className="h-3.5 w-3.5" />
          {live ? "Join" : "Open"}
        </span>
      </button>
    </li>
  );
}

function EmptyTile({
  icon,
  title,
  hint,
}: {
  icon: React.ReactNode;
  title: string;
  hint: string;
}) {
  return (
    <div className="flex items-center gap-3 rounded-xl border border-dashed border-line bg-app p-5 text-ink-3">
      <div className="grid h-10 w-10 place-items-center rounded-lg bg-app-2 text-ink-2">
        {icon}
      </div>
      <div>
        <div className="text-[13px] font-medium text-ink-2">{title}</div>
        <div className="text-[12px]">{hint}</div>
      </div>
    </div>
  );
}

function iconForType(t: CallType) {
  switch (t) {
    case "meeting":
      return Video;
    case "group":
      return Users;
    case "one_to_one":
      return Phone;
    case "webinar":
      return Briefcase;
    case "selector":
      return Phone;
  }
}

function labelForType(t: CallType): string {
  switch (t) {
    case "meeting":
      return "Meeting";
    case "group":
      return "Group call";
    case "one_to_one":
      return "1:1 call";
    case "webinar":
      return "Webinar";
    case "selector":
      return "Selector";
  }
}

function durationText(start: string, end: string): string {
  const ms = new Date(end).getTime() - new Date(start).getTime();
  if (ms < 60_000) return `${Math.round(ms / 1000)}s`;
  if (ms < 3600_000) return `${Math.floor(ms / 60_000)}m`;
  const h = Math.floor(ms / 3600_000);
  const m = Math.floor((ms % 3600_000) / 60_000);
  return m === 0 ? `${h}h` : `${h}h ${m}m`;
}

function timeAgo(iso: string): string {
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
  });
}
