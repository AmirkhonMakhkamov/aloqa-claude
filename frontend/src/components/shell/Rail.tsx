"use client";

import Link from "next/link";
import { useParams, usePathname } from "next/navigation";
import { useEffect, useRef, useState } from "react";
import {
  Bell,
  Folder,
  Grid3x3,
  MessageSquare,
  Phone,
  Settings,
  Shield,
  Smartphone,
  Sparkles,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { useAuth } from "@/stores/auth";
import { useNotifications } from "@/stores/notifications";
import { useWorkspace } from "@/stores/workspace";
import { NotificationsPopover } from "@/components/notifications/NotificationsPopover";
import { ProfilePopover } from "@/components/shell/ProfilePopover";

/*
 * The 64px dark vertical rail — Aloqa's primary nav surface. It lives at
 * the left edge of every authenticated page, with workspace brand up top,
 * view icons in the middle, and notifications/settings/avatar stacked at
 * the bottom.
 *
 * The "active view" is derived purely from pathname; we don't persist view
 * state in a store because Next.js URL is already the source of truth.
 */

type RailView =
  | "chat"
  | "calls"
  | "ai"
  | "files"
  | "telephony"
  | "marketplace"
  | "admin"
  | "settings";

function deriveView(pathname: string, wsId: string): RailView {
  if (!pathname || !wsId) return "chat";
  const base = `/w/${wsId}`;
  if (pathname.startsWith(`${base}/calls`)) return "calls";
  if (pathname.startsWith(`${base}/ai`)) return "ai";
  if (pathname.startsWith(`${base}/files`)) return "files";
  if (pathname.startsWith(`${base}/phone`)) return "telephony";
  if (pathname.startsWith(`${base}/apps`)) return "marketplace";
  if (pathname.startsWith(`${base}/admin`)) return "admin";
  if (pathname.startsWith(`${base}/settings`)) return "settings";
  return "chat";
}

interface RailEntry {
  id: RailView;
  label: string;
  Icon: React.ComponentType<{ className?: string }>;
  href: (wsId: string) => string;
}

const TOP_ENTRIES: RailEntry[] = [
  { id: "chat", label: "Chat", Icon: MessageSquare, href: (ws) => `/w/${ws}` },
  { id: "calls", label: "Meetings", Icon: Phone, href: (ws) => `/w/${ws}/calls` },
  { id: "ai", label: "AI", Icon: Sparkles, href: (ws) => `/w/${ws}/ai` },
  { id: "files", label: "Files", Icon: Folder, href: (ws) => `/w/${ws}/files` },
  { id: "telephony", label: "Phone", Icon: Smartphone, href: (ws) => `/w/${ws}/phone` },
  { id: "marketplace", label: "Apps", Icon: Grid3x3, href: (ws) => `/w/${ws}/apps` },
];

export function Rail() {
  const { wsId } = useParams<{ wsId: string }>();
  const pathname = usePathname() ?? "";
  const active = deriveView(pathname, wsId);

  const workspaces = useWorkspace((s) => s.workspaces);
  const ws = workspaces.find((w) => w.id === wsId);
  const user = useAuth((s) => s.user);
  const unread = useNotifications((s) => s.unread);
  const startPolling = useNotifications((s) => s.startPolling);

  const [notifOpen, setNotifOpen] = useState(false);
  const [profileOpen, setProfileOpen] = useState(false);
  const bellRef = useRef<HTMLButtonElement | null>(null);
  const avatarRef = useRef<HTMLButtonElement | null>(null);

  // Keep the bell badge fresh without opening the drawer.
  useEffect(() => {
    if (!wsId) return;
    return startPolling(wsId);
  }, [wsId, startPolling]);

  return (
    <aside className="dark-surface flex h-full w-rail shrink-0 flex-col items-center gap-1 bg-rail py-3 text-white/90">
      {/* Workspace brand (click → switcher). */}
      <Link
        href="/w"
        className="mb-2 grid h-10 w-10 place-items-center rounded-lg bg-accent text-sm font-bold text-white shadow-sm transition hover:bg-accent-hover"
        title={ws?.name ? `Switch workspace — currently ${ws.name}` : "Workspace"}
      >
        {(ws?.name ?? "A").slice(0, 1).toUpperCase()}
      </Link>
      <div className="mb-1 h-px w-6 bg-white/10" />

      {/* Primary views */}
      {TOP_ENTRIES.map((v) => (
        <RailIcon
          key={v.id}
          href={v.href(wsId)}
          title={v.label}
          Icon={v.Icon}
          active={active === v.id}
        />
      ))}

      <div className="flex-1" />

      {/* Admin is a role-gated peer to Settings in the footer area. */}
      <RailIcon
        href={`/w/${wsId}/admin/members`}
        title="Admin"
        Icon={Shield}
        active={active === "admin"}
      />
      <RailIcon
        href={`/w/${wsId}/settings`}
        title="Settings"
        Icon={Settings}
        active={active === "settings"}
      />

      {/* Notifications drawer anchor — bell pops a panel rather than routes. */}
      <button
        ref={bellRef}
        type="button"
        title="Notifications"
        onClick={() => setNotifOpen((v) => !v)}
        className={cn(
          "relative grid h-10 w-10 place-items-center rounded-lg transition",
          notifOpen
            ? "bg-white/10 text-white"
            : "text-white/60 hover:bg-white/10 hover:text-white",
        )}
      >
        <Bell className="h-5 w-5" />
        {unread > 0 ? (
          <span
            className="absolute -right-0.5 -top-0.5 grid h-4 min-w-4 place-items-center rounded-full bg-accent px-1 text-[10px] font-semibold leading-none text-white ring-2 ring-rail"
            aria-label={`${unread} unread notifications`}
          >
            {unread > 99 ? "99+" : unread}
          </span>
        ) : null}
      </button>

      {/* Avatar with status dot. Presence wiring lands in Phase 14. */}
      <button
        ref={avatarRef}
        type="button"
        onClick={() => setProfileOpen((v) => !v)}
        title={user?.display_name ?? "You"}
        className={cn(
          "relative mt-1 grid h-9 w-9 place-items-center rounded-lg text-xs font-bold uppercase transition",
          profileOpen
            ? "bg-white/20 text-white"
            : "bg-white/10 text-white/90 hover:bg-white/15",
        )}
      >
        {(user?.display_name ?? "?").slice(0, 1)}
        <span
          className="absolute -bottom-0.5 -right-0.5 h-2.5 w-2.5 rounded-full border-2 border-rail bg-status-green"
          aria-hidden
        />
      </button>

      {notifOpen && wsId ? (
        <NotificationsPopover
          wsId={wsId}
          anchor={bellRef}
          onClose={() => setNotifOpen(false)}
        />
      ) : null}
      {profileOpen ? (
        <ProfilePopover
          anchor={avatarRef}
          onClose={() => setProfileOpen(false)}
          wsId={wsId}
        />
      ) : null}
    </aside>
  );
}

function RailIcon({
  href,
  title,
  Icon,
  active,
}: {
  href: string;
  title: string;
  Icon: React.ComponentType<{ className?: string }>;
  active?: boolean;
}) {
  return (
    <Link
      href={href}
      title={title}
      className={cn(
        "grid h-10 w-10 place-items-center rounded-lg transition",
        active
          ? "bg-accent-dim text-white"
          : "text-white/60 hover:bg-white/10 hover:text-white",
      )}
    >
      <Icon className="h-5 w-5" />
    </Link>
  );
}
