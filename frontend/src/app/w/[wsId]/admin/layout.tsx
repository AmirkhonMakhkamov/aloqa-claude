"use client";

import Link from "next/link";
import { useParams, usePathname } from "next/navigation";
import { cn } from "@/lib/utils";

export default function AdminLayout({ children }: { children: React.ReactNode }) {
  const { wsId } = useParams<{ wsId: string }>();
  const pathname = usePathname();

  const tabs = [
    { label: "Members", href: `/w/${wsId}/admin/members` },
    { label: "Invites", href: `/w/${wsId}/admin/invites` },
    { label: "Audit log", href: `/w/${wsId}/admin/audit` },
  ];

  return (
    <div className="flex h-full flex-col">
      <header className="border-b border-ink-line bg-navy-950/40 px-6 pt-4">
        <h1 className="text-xl font-semibold text-white">Admin</h1>
        <p className="mb-3 text-xs text-slate-500">
          Workspace settings. Some actions are limited to owners and admins.
        </p>
        <nav className="flex gap-1">
          {tabs.map((t) => {
            const active = pathname.startsWith(t.href);
            return (
              <Link
                key={t.href}
                href={t.href}
                className={cn(
                  "rounded-t-md px-3 py-2 text-sm transition-colors",
                  active
                    ? "bg-ink text-white"
                    : "text-slate-400 hover:bg-white/5 hover:text-white",
                )}
              >
                {t.label}
              </Link>
            );
          })}
        </nav>
      </header>
      <div className="flex-1 overflow-y-auto bg-ink">{children}</div>
    </div>
  );
}
