import clsx, { type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

export function initials(name?: string | null): string {
  if (!name) return "?";
  const parts = name.trim().split(/\s+/);
  return (
    (parts[0]?.[0] ?? "").toUpperCase() + (parts[1]?.[0] ?? "").toUpperCase()
  ).slice(0, 2);
}

// Deterministic per-user tint — picked from Tailwind's default palette so
// avatars match the Aloqa reference's bright, saturated swatches instead of
// the old navy/ink monochrome. Kept to seven stable colors so the same user
// always lands on the same tile.
const avatarColors = [
  "bg-blue-600",
  "bg-indigo-600",
  "bg-sky-600",
  "bg-emerald-600",
  "bg-rose-600",
  "bg-amber-600",
  "bg-violet-600",
  "bg-cyan-700",
];

export function avatarColor(seed: string): string {
  let h = 0;
  for (let i = 0; i < seed.length; i++) h = (h * 31 + seed.charCodeAt(i)) | 0;
  return avatarColors[Math.abs(h) % avatarColors.length];
}

/** Relative time label for chat messages. Keeps it short: "just now" / "3m" /
 * "Yesterday 14:05" / "Apr 3 at 09:12". Uses native Intl so we don't pull in
 * date-fns on the client hot path. */
export function formatChatTime(iso: string, now = new Date()): string {
  const d = new Date(iso);
  const diffMs = now.getTime() - d.getTime();
  if (diffMs < 30_000) return "just now";
  if (diffMs < 60 * 60_000) return `${Math.floor(diffMs / 60_000)}m`;
  const sameDay =
    d.getFullYear() === now.getFullYear() &&
    d.getMonth() === now.getMonth() &&
    d.getDate() === now.getDate();
  const time = d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  if (sameDay) return time;
  const yesterday = new Date(now);
  yesterday.setDate(now.getDate() - 1);
  const isYesterday =
    d.getFullYear() === yesterday.getFullYear() &&
    d.getMonth() === yesterday.getMonth() &&
    d.getDate() === yesterday.getDate();
  if (isYesterday) return `Yesterday ${time}`;
  const date = d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
  return `${date} at ${time}`;
}

/** Day-separator label: "Today" / "Yesterday" / "Apr 3". */
export function formatDayLabel(iso: string, now = new Date()): string {
  const d = new Date(iso);
  const sameYear = d.getFullYear() === now.getFullYear();
  const today = new Date(now);
  today.setHours(0, 0, 0, 0);
  const msPerDay = 86_400_000;
  const that = new Date(d);
  that.setHours(0, 0, 0, 0);
  const diffDays = Math.round((today.getTime() - that.getTime()) / msPerDay);
  if (diffDays === 0) return "Today";
  if (diffDays === 1) return "Yesterday";
  return d.toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    year: sameYear ? undefined : "numeric",
  });
}

export function dayKey(iso: string): string {
  return iso.slice(0, 10); // YYYY-MM-DD
}
