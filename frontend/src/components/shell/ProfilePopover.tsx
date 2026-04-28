"use client";

import { useEffect, useRef } from "react";
import Link from "next/link";
import { LogOut, Settings, ShieldCheck, UserCircle2 } from "lucide-react";
import { Avatar } from "@/components/ui/Avatar";
import { useAuth } from "@/stores/auth";

/*
 * Profile popover anchored above the rail avatar. Same dismissal pattern as
 * the notifications popover: outside-click + Escape, with the anchor element
 * exempted so clicking the avatar that opened us doesn't immediately re-close.
 *
 * The action surface is intentionally tiny — Aloqa shows account identity at
 * the top, then Settings + Security shortcuts, then Sign out. The richer
 * preferences UI lives at /settings; this is just the launchpad.
 */
interface Props {
  onClose: () => void;
  anchor?: React.RefObject<HTMLElement | null>;
  /** When provided, deep-links Settings/Security straight into the workspace
   *  scope; otherwise we fall back to the bare /settings paths. */
  wsId?: string;
}

export function ProfilePopover({ onClose, anchor, wsId }: Props) {
  const user = useAuth((s) => s.user);
  const logout = useAuth((s) => s.logout);
  const cardRef = useRef<HTMLDivElement | null>(null);

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

  const settingsHref = wsId ? `/w/${wsId}/settings` : "/settings";
  const securityHref = wsId
    ? `/w/${wsId}/settings/security`
    : "/settings/security";

  return (
    <div
      ref={cardRef}
      role="dialog"
      aria-label="Profile menu"
      className="fixed bottom-3 left-[72px] z-50 w-[260px] rounded-xl border border-line bg-app p-1 text-ink shadow-lg"
    >
      <div className="flex items-center gap-3 rounded-lg px-3 py-3">
        <Avatar
          name={user?.display_name}
          src={user?.avatar_url ?? undefined}
          size={40}
        />
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13px] font-semibold text-ink">
            {user?.display_name ?? "Signed in"}
          </div>
          <div className="truncate text-[11px] text-ink-3">
            {user?.email ?? ""}
          </div>
        </div>
      </div>

      <div className="mx-2 my-1 h-px bg-line" />

      <Link
        href="/account"
        onClick={onClose}
        className="flex items-center gap-2 rounded-md px-3 py-2 text-[13px] text-ink-2 transition hover:bg-app-2 hover:text-ink"
      >
        <UserCircle2 className="h-4 w-4 text-ink-3" />
        Account
      </Link>
      <Link
        href={settingsHref}
        onClick={onClose}
        className="flex items-center gap-2 rounded-md px-3 py-2 text-[13px] text-ink-2 transition hover:bg-app-2 hover:text-ink"
      >
        <Settings className="h-4 w-4 text-ink-3" />
        Settings
      </Link>
      <Link
        href={securityHref}
        onClick={onClose}
        className="flex items-center gap-2 rounded-md px-3 py-2 text-[13px] text-ink-2 transition hover:bg-app-2 hover:text-ink"
      >
        <ShieldCheck className="h-4 w-4 text-ink-3" />
        Security
      </Link>

      <div className="mx-2 my-1 h-px bg-line" />

      <button
        type="button"
        onClick={() => {
          onClose();
          void logout();
        }}
        className="flex w-full items-center gap-2 rounded-md px-3 py-2 text-left text-[13px] text-status-red transition hover:bg-status-red/10"
      >
        <LogOut className="h-4 w-4" />
        Sign out
      </button>
    </div>
  );
}
