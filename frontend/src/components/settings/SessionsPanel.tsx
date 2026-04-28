"use client";

import { useEffect, useState } from "react";
import {
  Chrome,
  Globe,
  Laptop,
  LogOut,
  RefreshCw,
  Shield,
  Smartphone,
  X,
} from "lucide-react";
import { Button } from "@/components/ui/Button";
import { loadTokens } from "@/lib/auth";
import { cn } from "@/lib/utils";
import { useAuth } from "@/stores/auth";
import type { Session } from "@/lib/types";

/*
 * Security panel: lists active sessions backed by Redis on the server,
 * lets the user revoke individual sessions or blow them all away.
 *
 * - Each row shows a human label (browser + OS from device_info), an IP
 *   address, the session's last-active timestamp, and an X to revoke.
 * - The row for the current session is tagged "This device" and the
 *   revoke button is still wired — the store transparently handles the
 *   self-revoke case by clearing local tokens, and the layout guard
 *   kicks the user back to /login.
 * - `Sign out of all devices` is a red-ghost button sitting below the
 *   list. We confirm with a plain confirm() for now; a modal would be
 *   overkill for an internal-facing action this rare.
 */
export function SessionsPanel() {
  const sessions = useAuth((s) => s.sessions);
  const loading = useAuth((s) => s.sessionsLoading);
  const refresh = useAuth((s) => s.refreshSessions);
  const revoke = useAuth((s) => s.revokeSession);
  const logoutAll = useAuth((s) => s.logoutAll);

  // Fetch on mount. We don't poll — a stale "last-active" a minute off is
  // fine; users can hit Refresh when they want fresh data.
  useEffect(() => {
    void refresh();
  }, [refresh]);

  // Remember the current session id on the client so we can tag it in the
  // list and disable the revoke button from blowing it away silently.
  const [currentSessionId, setCurrentSessionId] = useState<string | null>(null);
  useEffect(() => {
    const t = loadTokens();
    setCurrentSessionId(t?.sessionId ?? null);
  }, [sessions]);

  const [revoking, setRevoking] = useState<string | null>(null);
  const [confirmAll, setConfirmAll] = useState(false);

  async function handleRevoke(sessionId: string) {
    setRevoking(sessionId);
    try {
      await revoke(sessionId);
    } finally {
      setRevoking(null);
    }
  }

  async function handleLogoutAll() {
    await logoutAll();
  }

  return (
    <div className="space-y-6">
      <header className="flex items-start justify-between gap-4">
        <div>
          <h2 className="text-lg font-semibold text-ink">Active sessions</h2>
          <p className="mt-1 text-[13px] text-ink-2">
            These are the devices currently signed in to your account. Revoke
            any session you don&apos;t recognise.
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={() => void refresh()}
          disabled={loading}
        >
          <RefreshCw className={cn("h-3.5 w-3.5", loading && "animate-spin")} />
          Refresh
        </Button>
      </header>

      {loading && sessions.length === 0 ? (
        <div className="rounded-xl border border-line bg-app p-6 text-center text-sm text-ink-3">
          Loading sessions…
        </div>
      ) : sessions.length === 0 ? (
        <div className="rounded-xl border border-dashed border-line bg-app p-8 text-center text-sm text-ink-3">
          No other active sessions.
        </div>
      ) : (
        <ul className="divide-y divide-line overflow-hidden rounded-xl border border-line bg-app">
          {sessions.map((s) => (
            <SessionRow
              key={s.id}
              session={s}
              current={s.id === currentSessionId}
              revoking={revoking === s.id}
              onRevoke={() => handleRevoke(s.id)}
            />
          ))}
        </ul>
      )}

      <section className="rounded-xl border border-line bg-app p-5">
        <div className="flex items-start gap-3">
          <div className="grid h-10 w-10 shrink-0 place-items-center rounded-lg bg-status-red/10 text-status-red">
            <Shield className="h-5 w-5" />
          </div>
          <div className="min-w-0 flex-1">
            <h3 className="text-[15px] font-semibold text-ink">
              Sign out of all devices
            </h3>
            <p className="mt-0.5 text-[13px] text-ink-2">
              Immediately ends every session, including this one. Use this if
              you think your account has been compromised.
            </p>
          </div>
        </div>
        <div className="mt-4 flex items-center gap-2">
          {confirmAll ? (
            <>
              <Button variant="danger" size="sm" onClick={() => void handleLogoutAll()}>
                <LogOut className="h-3.5 w-3.5" />
                Yes, sign out everywhere
              </Button>
              <Button variant="ghost" size="sm" onClick={() => setConfirmAll(false)}>
                Cancel
              </Button>
            </>
          ) : (
            <Button variant="outline" size="sm" onClick={() => setConfirmAll(true)}>
              Sign out all sessions
            </Button>
          )}
        </div>
      </section>
    </div>
  );
}

function SessionRow({
  session,
  current,
  revoking,
  onRevoke,
}: {
  session: Session;
  current: boolean;
  revoking: boolean;
  onRevoke: () => void;
}) {
  const Icon = iconForDevice(session.device_info);
  return (
    <li className="flex items-center gap-4 p-4">
      <div
        className={cn(
          "grid h-10 w-10 shrink-0 place-items-center rounded-lg",
          current ? "bg-accent-dim text-accent" : "bg-app-2 text-ink-2",
        )}
      >
        <Icon className="h-5 w-5" />
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="truncate text-[14px] font-semibold text-ink">
            {session.device_info || "Unknown device"}
          </span>
          {current ? (
            <span className="rounded-full bg-accent-dim px-2 py-0.5 text-[11px] font-medium text-accent">
              This device
            </span>
          ) : null}
        </div>
        <div className="mt-0.5 flex flex-wrap gap-x-3 gap-y-0.5 text-[12px] text-ink-3">
          <span>IP {session.ip_address || "—"}</span>
          <span>·</span>
          <span>Active {formatRelative(session.last_active_at)}</span>
          <span>·</span>
          <span>Started {formatRelative(session.created_at)}</span>
        </div>
      </div>
      <button
        type="button"
        onClick={onRevoke}
        disabled={revoking}
        title={current ? "Sign out this device" : "Revoke session"}
        aria-label={current ? "Sign out this device" : "Revoke session"}
        className={cn(
          "grid h-8 w-8 shrink-0 place-items-center rounded-md text-ink-3 transition",
          "hover:bg-status-red/10 hover:text-status-red",
          "disabled:cursor-not-allowed disabled:opacity-50",
        )}
      >
        {revoking ? (
          <span className="h-3.5 w-3.5 animate-spin rounded-full border-2 border-line border-t-status-red" />
        ) : (
          <X className="h-4 w-4" />
        )}
      </button>
    </li>
  );
}

function iconForDevice(info: string) {
  const s = (info || "").toLowerCase();
  if (s.includes("ios") || s.includes("android")) return Smartphone;
  if (s.includes("chrome") && !s.includes("chromeos")) return Chrome;
  if (s.includes("macos") || s.includes("windows") || s.includes("linux"))
    return Laptop;
  return Globe;
}

/*
 * Relative time that speaks in human terms ("just now", "5m ago", "Apr 12").
 * The backend returns UTC ISO strings; Date.parse handles them natively.
 * We cap at 7 days — older stamps become calendar dates so rows don't start
 * to say "78 days ago".
 */
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
