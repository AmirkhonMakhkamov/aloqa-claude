"use client";

import { useEffect, useRef } from "react";
import Link from "next/link";
import {
  AtSign,
  Bell,
  CheckCheck,
  Hash,
  MessageSquare,
  Phone,
  Sparkles,
  X,
} from "lucide-react";
import { cn } from "@/lib/utils";
import type { Notification, UUID } from "@/lib/types";
import { useNotifications } from "@/stores/notifications";

/*
 * A floating card anchored to the rail bell. 360×520 with a scrollable body,
 * "Mark all read" shortcut in the header, and per-row dismiss + link.
 *
 * We fetch the full list only when the popover opens — the badge poller has
 * already been keeping the count fresh so the user sees an accurate dot
 * without us eagerly loading 50 rows.
 */
interface Props {
  wsId: UUID;
  onClose: () => void;
  anchor?: React.RefObject<HTMLElement | null>;
}

export function NotificationsPopover({ wsId, onClose, anchor }: Props) {
  const items = useNotifications((s) => s.items);
  const loading = useNotifications((s) => s.loading);
  const loadedOnce = useNotifications((s) => s.loadedOnce);
  const refresh = useNotifications((s) => s.refresh);
  const markRead = useNotifications((s) => s.markRead);
  const markAllRead = useNotifications((s) => s.markAllRead);

  const cardRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    void refresh(wsId);
  }, [wsId, refresh]);

  // Close on outside click and on Escape. We check against the anchor element
  // too so clicking the bell that opened us doesn't instantly re-close.
  useEffect(() => {
    function onDown(e: MouseEvent) {
      const card = cardRef.current;
      const anchorEl = anchor?.current;
      const target = e.target as Node;
      if (card && card.contains(target)) return;
      if (anchorEl && anchorEl.contains(target)) return;
      onClose();
    }
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") onClose();
    }
    window.addEventListener("mousedown", onDown);
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("mousedown", onDown);
      window.removeEventListener("keydown", onKey);
    };
  }, [onClose, anchor]);

  const unread = items.filter((n) => !n.read_at).length;

  return (
    <div
      ref={cardRef}
      role="dialog"
      aria-label="Notifications"
      className="fixed bottom-20 left-[72px] z-50 flex max-h-[520px] w-[360px] flex-col rounded-xl border border-line bg-app text-ink shadow-lg"
    >
      <header className="flex h-12 shrink-0 items-center gap-2 border-b border-line px-3">
        <Bell className="h-4 w-4 text-ink-2" />
        <div className="text-[14px] font-semibold">Notifications</div>
        {unread > 0 ? (
          <span className="ml-1 rounded-full bg-accent-dim px-1.5 py-0.5 text-[11px] font-medium text-accent">
            {unread}
          </span>
        ) : null}
        <button
          type="button"
          onClick={() => void markAllRead(wsId)}
          disabled={unread === 0}
          className="ml-auto inline-flex items-center gap-1 rounded-md px-2 py-1 text-[12px] text-ink-2 transition hover:bg-app-2 hover:text-ink disabled:cursor-not-allowed disabled:opacity-40"
        >
          <CheckCheck className="h-3.5 w-3.5" />
          Mark all read
        </button>
        <button
          type="button"
          onClick={onClose}
          aria-label="Close"
          className="grid h-7 w-7 place-items-center rounded-md text-ink-2 transition hover:bg-app-2 hover:text-ink"
        >
          <X className="h-4 w-4" />
        </button>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {!loadedOnce && loading ? (
          <div className="flex items-center justify-center gap-2 p-8 text-[12px] text-ink-3">
            <span className="h-3 w-3 animate-spin rounded-full border-2 border-line border-t-accent" />
            Loading…
          </div>
        ) : items.length === 0 ? (
          <EmptyState />
        ) : (
          <ul className="divide-y divide-line">
            {items.map((n) => (
              <Row
                key={n.id}
                notification={n}
                onRead={() => void markRead(wsId, n.id)}
                onNavigate={onClose}
              />
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

function EmptyState() {
  return (
    <div className="flex flex-col items-center gap-2 p-10 text-center text-ink-3">
      <div className="grid h-10 w-10 place-items-center rounded-full bg-app-2">
        <Bell className="h-4 w-4" />
      </div>
      <div className="text-[13px] font-medium text-ink-2">You&apos;re all caught up</div>
      <div className="text-[12px]">
        Mentions, DMs, and call invites will show up here.
      </div>
    </div>
  );
}

function Row({
  notification,
  onRead,
  onNavigate,
}: {
  notification: Notification;
  onRead: () => void;
  onNavigate: () => void;
}) {
  const unread = !notification.read_at;
  const Icon = iconFor(notification.type);
  const body = (
    <div
      className={cn(
        "flex gap-3 px-3 py-2.5 transition",
        unread ? "bg-accent-dim/30 hover:bg-accent-dim/50" : "hover:bg-app-2",
      )}
    >
      <div
        className={cn(
          "mt-0.5 grid h-8 w-8 shrink-0 place-items-center rounded-lg",
          unread ? "bg-accent-dim text-accent" : "bg-app-2 text-ink-2",
        )}
      >
        <Icon className="h-4 w-4" />
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span
            className={cn(
              "truncate text-[13px]",
              unread ? "font-semibold text-ink" : "text-ink-2",
            )}
          >
            {notification.title}
          </span>
          {unread ? (
            <span
              className="h-1.5 w-1.5 shrink-0 rounded-full bg-accent"
              aria-hidden
            />
          ) : null}
        </div>
        {notification.body ? (
          <div className="mt-0.5 line-clamp-2 text-[12px] text-ink-3">
            {notification.body}
          </div>
        ) : null}
        <div className="mt-1 text-[11px] text-ink-3">
          {formatRelative(notification.created_at)}
        </div>
      </div>
    </div>
  );

  return (
    <li className="group relative">
      {notification.link ? (
        <Link
          href={notification.link}
          onClick={() => {
            if (unread) onRead();
            onNavigate();
          }}
          className="block"
        >
          {body}
        </Link>
      ) : (
        <button
          type="button"
          onClick={() => unread && onRead()}
          className="block w-full text-left"
        >
          {body}
        </button>
      )}
      {unread ? (
        <button
          type="button"
          onClick={(e) => {
            e.preventDefault();
            e.stopPropagation();
            onRead();
          }}
          title="Mark as read"
          className="absolute right-2 top-2 hidden h-6 w-6 place-items-center rounded-md text-ink-3 transition hover:bg-app-3 hover:text-ink group-hover:grid"
        >
          <CheckCheck className="h-3.5 w-3.5" />
        </button>
      ) : null}
    </li>
  );
}

/*
 * Tiny type-string → icon mapper. The backend emits free-form strings like
 * `mention.channel` or `call.invite` — we match on prefix to stay resilient
 * if new subtypes appear. The catch-all is a bell.
 */
function iconFor(type: string): React.ComponentType<{ className?: string }> {
  if (type.startsWith("mention")) return AtSign;
  if (type.startsWith("dm")) return MessageSquare;
  if (type.startsWith("channel")) return Hash;
  if (type.startsWith("call")) return Phone;
  if (type.startsWith("system") || type.startsWith("announcement")) return Sparkles;
  return Bell;
}

function formatRelative(iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "—";
  const diffSec = Math.max(0, Math.round((Date.now() - t) / 1000));
  if (diffSec < 30) return "just now";
  if (diffSec < 60) return `${diffSec}s ago`;
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
