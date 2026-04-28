"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { Hash, Phone, Plus, Search, Shield, Sparkles } from "lucide-react";
import { useAuth } from "@/stores/auth";
import { useWorkspace } from "@/stores/workspace";

/*
 * Workspace home / default-view landing. The sidebar already gives users
 * ways to navigate — this page is primarily a "no channel selected" state
 * that nudges them toward the common actions. When the chat rebuild lands
 * (Phase 4) we'll consider redirecting the home path straight into the
 * first channel; for now this stays as an intentional splash.
 */
export default function WorkspaceHome() {
  const params = useParams<{ wsId: string }>();
  const wsId = params.wsId;
  const workspaces = useWorkspace((s) => s.workspaces);
  const channels = useWorkspace((s) => s.channels) ?? [];
  const user = useAuth((s) => s.user);
  const ws = workspaces.find((w) => w.id === wsId);

  const firstName = (user?.display_name ?? "").split(/\s+/)[0] || "there";

  const quickLinks = [
    {
      href: `/w/${wsId}/search`,
      Icon: Search,
      title: "Search",
      body: "Full-text across messages, channels, and files.",
    },
    {
      href: `/w/${wsId}/calls`,
      Icon: Phone,
      title: "Meetings",
      body: "Start or join a meeting in this workspace.",
    },
    {
      href: `/w/${wsId}/ai`,
      Icon: Sparkles,
      title: "AI",
      body: "Summaries, transcripts, and the assistant.",
    },
    {
      href: `/w/${wsId}/admin/members`,
      Icon: Shield,
      title: "Admin",
      body: "Members, roles, invites, audit log.",
    },
  ];

  return (
    <div className="flex h-full flex-col overflow-hidden">
      {/* Header */}
      <header className="flex h-[52px] shrink-0 items-center gap-3 border-b border-line px-6">
        <span className="text-[15px] font-semibold text-ink">Home</span>
        <span className="text-[13px] text-ink-3">— {ws?.name ?? "…"}</span>
      </header>

      <div className="flex-1 overflow-y-auto">
        <div className="mx-auto w-full max-w-3xl px-8 py-10">
          {/* Greeting */}
          <section className="mb-10 space-y-2">
            <div className="inline-flex items-center gap-2 rounded-full border border-line bg-app-2 px-3 py-1 text-[12px] font-medium text-ink-2">
              <span className="h-1.5 w-1.5 rounded-full bg-status-green" />
              Connected to real-time stream
            </div>
            <h1 className="text-[28px] font-semibold text-ink">
              Welcome back, {firstName}.
            </h1>
            <p className="text-sm text-ink-2">
              Pick a channel from the sidebar or jump into a task below.
            </p>
          </section>

          {/* Quick links */}
          <section className="mb-10 grid gap-3 sm:grid-cols-2">
            {quickLinks.map((q) => (
              <Link
                key={q.href}
                href={q.href}
                className="group flex items-start gap-3 rounded-xl border border-line bg-app p-4 shadow-sm transition hover:border-accent hover:shadow-md"
              >
                <div className="grid h-10 w-10 shrink-0 place-items-center rounded-lg bg-accent-dim text-accent transition group-hover:scale-105">
                  <q.Icon className="h-5 w-5" />
                </div>
                <div className="min-w-0">
                  <div className="text-[15px] font-semibold text-ink">
                    {q.title}
                  </div>
                  <div className="text-[13px] text-ink-2">{q.body}</div>
                </div>
              </Link>
            ))}
          </section>

          {/* Channels */}
          <section>
            <div className="mb-2 flex items-center justify-between">
              <h2 className="text-[11px] font-semibold uppercase tracking-wider text-ink-3">
                Channels
              </h2>
              <span className="text-[12px] text-ink-3">
                {channels.length} total
              </span>
            </div>
            <ul className="divide-y divide-line rounded-xl border border-line bg-app">
              {channels.length === 0 ? (
                <li className="flex items-center gap-3 p-5 text-sm text-ink-2">
                  <div className="grid h-9 w-9 place-items-center rounded-lg bg-app-2 text-ink-3">
                    <Plus className="h-4 w-4" />
                  </div>
                  <div className="flex-1">
                    <div className="font-medium text-ink">
                      No channels yet
                    </div>
                    <div className="text-[12px] text-ink-3">
                      Create one from the sidebar to start a conversation.
                    </div>
                  </div>
                </li>
              ) : (
                channels.map((c) => (
                  <li key={c.id}>
                    <Link
                      href={`/w/${wsId}/c/${c.id}`}
                      className="flex items-center gap-3 p-4 text-[13px] text-ink transition hover:bg-app-2"
                    >
                      <Hash className="h-4 w-4 text-ink-3" />
                      <span className="font-medium">{c.name}</span>
                      {c.topic ? (
                        <span className="ml-3 truncate text-[12px] text-ink-3">
                          {c.topic}
                        </span>
                      ) : null}
                    </Link>
                  </li>
                ))
              )}
            </ul>
          </section>
        </div>
      </div>
    </div>
  );
}
