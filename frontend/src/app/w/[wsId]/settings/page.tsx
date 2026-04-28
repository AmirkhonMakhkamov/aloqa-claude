"use client";

import { useState, type ComponentType } from "react";
import {
  Accessibility,
  Bell,
  Info,
  Palette,
  Phone,
  Shield,
  Sliders,
  User as UserIcon,
} from "lucide-react";
import { SessionsPanel } from "@/components/settings/SessionsPanel";
import { cn } from "@/lib/utils";

/*
 * Settings chrome with an inline left nav + section panel. The left nav
 * mirrors the eight-section structure the Aloqa reference uses; only
 * Security is wired end-to-end in Phase 3 (it hosts the sessions list),
 * the others render a "coming soon" stub so the navigation works without
 * leading to a 404. Phase 13 fleshes out the remaining panels in place.
 */
type SectionId =
  | "profile"
  | "notifications"
  | "appearance"
  | "calls"
  | "security"
  | "privacy"
  | "accessibility"
  | "advanced"
  | "about";

interface SectionDef {
  id: SectionId;
  label: string;
  body: string;
  Icon: ComponentType<{ className?: string }>;
}

const SECTIONS: SectionDef[] = [
  { id: "profile", label: "Profile", body: "Name, avatar, locale.", Icon: UserIcon },
  { id: "notifications", label: "Notifications", body: "Desktop, mobile, email cadence.", Icon: Bell },
  { id: "appearance", label: "Appearance", body: "Accent, density, font size.", Icon: Palette },
  { id: "calls", label: "Calls", body: "Default devices, auto-quality, layout.", Icon: Phone },
  { id: "security", label: "Security", body: "Active sessions, sign-out everywhere.", Icon: Shield },
  { id: "privacy", label: "Privacy", body: "Who can see your profile and presence.", Icon: Shield },
  { id: "accessibility", label: "Accessibility", body: "Motion, contrast, captions.", Icon: Accessibility },
  { id: "advanced", label: "Advanced", body: "Developer flags and experiments.", Icon: Sliders },
  { id: "about", label: "About", body: "Version, legal, credits.", Icon: Info },
];

export default function SettingsPage() {
  const [active, setActive] = useState<SectionId>("security");
  const section = SECTIONS.find((s) => s.id === active) ?? SECTIONS[0];

  return (
    <div className="flex h-full flex-col overflow-hidden">
      <header className="flex h-[52px] shrink-0 items-center gap-3 border-b border-line px-6">
        <span className="text-[15px] font-semibold text-ink">Settings</span>
        <span className="text-[13px] text-ink-3">— {section.label}</span>
      </header>

      <div className="flex min-h-0 flex-1">
        {/* Section nav */}
        <nav className="w-[220px] shrink-0 overflow-y-auto border-r border-line bg-app-2 p-3">
          <ul className="space-y-0.5">
            {SECTIONS.map((s) => (
              <li key={s.id}>
                <button
                  type="button"
                  onClick={() => setActive(s.id)}
                  className={cn(
                    "flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-[13px] transition",
                    active === s.id
                      ? "bg-accent-dim font-medium text-accent"
                      : "text-ink-2 hover:bg-app-3 hover:text-ink",
                  )}
                >
                  <s.Icon className="h-4 w-4" />
                  {s.label}
                </button>
              </li>
            ))}
          </ul>
        </nav>

        {/* Section content */}
        <div className="min-w-0 flex-1 overflow-y-auto">
          <div className="mx-auto w-full max-w-3xl px-8 py-10">
            {section.id === "security" ? (
              <SessionsPanel />
            ) : (
              <PlaceholderSection section={section} />
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

function PlaceholderSection({ section }: { section: SectionDef }) {
  return (
    <div className="space-y-6">
      <header>
        <h2 className="text-lg font-semibold text-ink">{section.label}</h2>
        <p className="mt-1 text-[13px] text-ink-2">{section.body}</p>
      </header>
      <div className="flex items-center gap-3 rounded-xl border border-dashed border-line bg-app p-6 text-sm text-ink-3">
        <section.Icon className="h-5 w-5 shrink-0" />
        <div>
          This panel arrives in the settings phase of the rebuild. The
          sidebar navigation is live — only the contents haven&apos;t been
          filled in yet.
        </div>
      </div>
    </div>
  );
}
