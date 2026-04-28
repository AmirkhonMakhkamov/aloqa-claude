"use client";

import Link from "next/link";
import { useParams, usePathname, useRouter } from "next/navigation";
import { useEffect, useMemo, useState } from "react";
import {
  ChevronDown,
  ChevronRight,
  Hash,
  Lock,
  LogOut,
  Plus,
  Search as SearchIcon,
  User as UserIcon,
} from "lucide-react";
import { Avatar } from "@/components/ui/Avatar";
import { Button } from "@/components/ui/Button";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { channelsApi } from "@/lib/api/endpoints";
import { cn } from "@/lib/utils";
import { useAuth } from "@/stores/auth";
import { useWorkspace } from "@/stores/workspace";

/*
 * The 248px dark contextual sidebar. Chat is the "home" context — it lists
 * channels and DMs, which is what the Aloqa reference shows persistently.
 * Other top-level views (Calls, Files, AI, etc.) still live under this same
 * shell, but their main panels use the full remaining width, so we keep a
 * thin view-aware sidebar that stays useful: "Jump to channel" search +
 * the channel list stays visible so chat context is never a context-switch
 * away.
 *
 * The workspace switcher and the sign-out affordance collapse into the
 * sidebar footer next to the current user — matching the Aloqa reference.
 */
export function ContextSidebar() {
  const { wsId } = useParams<{ wsId: string }>();
  const pathname = usePathname() ?? "";
  const router = useRouter();

  const workspaces = useWorkspace((s) => s.workspaces);
  const channels = useWorkspace((s) => s.channels);
  const unread = useWorkspace((s) => s.unread);
  const refresh = useWorkspace((s) => s.refreshChannels);

  const ws = workspaces.find((w) => w.id === wsId);
  const user = useAuth((s) => s.user);
  const logout = useAuth((s) => s.logout);

  const [filter, setFilter] = useState("");

  const visible = useMemo(() => {
    // Defensive against a momentary null channels (e.g. when the backend's
    // Go nil slice serialized through before the store coerced it).
    const list = channels ?? [];
    const q = filter.trim().toLowerCase();
    if (!q) return list;
    return list.filter(
      (c) =>
        c.name.toLowerCase().includes(q) ||
        (c.topic ?? "").toLowerCase().includes(q),
    );
  }, [filter, channels]);

  const publicChannels = visible.filter((c) => c.type === "public");
  const privateChannels = visible.filter((c) => c.type === "private");
  const dms = visible.filter((c) => c.type === "dm" || c.type === "group_dm");

  return (
    <aside className="dark-surface flex h-full w-sidebar shrink-0 flex-col bg-sidebar text-white/90">
      {/* Header: workspace brand + Search bar */}
      <div className="flex h-[52px] shrink-0 items-center gap-2 border-b border-white/5 px-4">
        <div className="grid h-7 w-7 place-items-center rounded-md bg-accent text-[11px] font-bold text-white">
          {(ws?.name ?? "A").slice(0, 2).toUpperCase()}
        </div>
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm font-semibold text-white">
            {ws?.name ?? "Workspace"}
          </div>
          <div className="truncate text-[11px] text-white/50">
            {channels.length} channel{channels.length === 1 ? "" : "s"}
          </div>
        </div>
        <button
          onClick={() => router.push("/w")}
          className="rounded-md p-1 text-white/60 transition hover:bg-white/10 hover:text-white"
          title="Switch workspace"
        >
          <ChevronDown className="h-4 w-4" />
        </button>
      </div>

      {/* Jump-to search */}
      <div className="px-3 pb-2 pt-3">
        <div className="flex items-center gap-2 rounded-md bg-white/5 px-2.5 py-1.5 text-sm text-white/70 ring-1 ring-inset ring-white/5 focus-within:ring-white/20">
          <SearchIcon className="h-3.5 w-3.5" />
          <input
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder="Jump to channel or DM…"
            className="min-w-0 flex-1 bg-transparent text-[13px] outline-none placeholder:text-white/40"
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                const first = visible[0];
                if (first) router.push(`/w/${wsId}/c/${first.id}`);
              }
            }}
          />
        </div>
      </div>

      {/* Quick links */}
      <nav className="space-y-0.5 px-2 pb-2">
        <SidebarLink
          href={`/w/${wsId}/search`}
          label="Search"
          Icon={SearchIcon}
          active={pathname.startsWith(`/w/${wsId}/search`)}
        />
      </nav>

      {/* Channel list */}
      <div className="flex-1 overflow-y-auto px-2 pb-3">
        <Section
          title="Channels"
          action={<NewChannelButton onCreated={refresh} />}
          defaultOpen
        >
          {publicChannels.length === 0 ? (
            <Empty>No channels yet.</Empty>
          ) : (
            publicChannels.map((c) => (
              <ChannelRow
                key={c.id}
                href={`/w/${wsId}/c/${c.id}`}
                name={c.name}
                Icon={Hash}
                active={pathname === `/w/${wsId}/c/${c.id}`}
                badge={unread[c.id]}
              />
            ))
          )}
        </Section>

        {privateChannels.length > 0 ? (
          <Section title="Private" defaultOpen>
            {privateChannels.map((c) => (
              <ChannelRow
                key={c.id}
                href={`/w/${wsId}/c/${c.id}`}
                name={c.name}
                Icon={Lock}
                active={pathname === `/w/${wsId}/c/${c.id}`}
                badge={unread[c.id]}
              />
            ))}
          </Section>
        ) : null}

        {dms.length > 0 ? (
          <Section title="Direct messages" defaultOpen>
            {dms.map((c) => (
              <ChannelRow
                key={c.id}
                href={`/w/${wsId}/c/${c.id}`}
                name={c.name}
                Icon={UserIcon}
                active={pathname === `/w/${wsId}/c/${c.id}`}
                badge={unread[c.id]}
              />
            ))}
          </Section>
        ) : null}
      </div>

      {/* User footer with sign-out */}
      <div className="flex shrink-0 items-center gap-2 border-t border-white/5 px-3 py-2.5">
        <Avatar name={user?.display_name} size={32} />
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13px] font-medium text-white">
            {user?.display_name}
          </div>
          <div className="truncate text-[11px] text-white/50">
            {user?.email}
          </div>
        </div>
        <button
          onClick={async () => {
            await logout();
            router.replace("/login");
          }}
          className="rounded-md p-1.5 text-white/60 transition hover:bg-white/10 hover:text-white"
          title="Sign out"
        >
          <LogOut className="h-4 w-4" />
        </button>
      </div>
    </aside>
  );
}

function SidebarLink({
  href,
  label,
  Icon,
  active,
}: {
  href: string;
  label: string;
  Icon: React.ComponentType<{ className?: string }>;
  active?: boolean;
}) {
  return (
    <Link
      href={href}
      className={cn(
        "flex items-center gap-2 rounded-md px-2 py-1.5 text-[13px] transition",
        active
          ? "bg-white/10 font-medium text-white"
          : "text-white/70 hover:bg-white/5 hover:text-white",
      )}
    >
      <Icon className="h-4 w-4" />
      {label}
    </Link>
  );
}

function Section({
  title,
  action,
  defaultOpen = true,
  children,
}: {
  title: string;
  action?: React.ReactNode;
  defaultOpen?: boolean;
  children: React.ReactNode;
}) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <div className="pt-3">
      <div className="flex items-center gap-1 px-2 pb-1">
        <button
          onClick={() => setOpen((v) => !v)}
          className="flex items-center gap-1 text-[11px] font-semibold uppercase tracking-wider text-white/50 transition hover:text-white"
        >
          {open ? (
            <ChevronDown className="h-3 w-3" />
          ) : (
            <ChevronRight className="h-3 w-3" />
          )}
          {title}
        </button>
        <div className="ml-auto">{action}</div>
      </div>
      {open ? <div className="space-y-0.5">{children}</div> : null}
    </div>
  );
}

function Empty({ children }: { children: React.ReactNode }) {
  return (
    <div className="px-2 py-1 text-[12px] text-white/40">{children}</div>
  );
}

function ChannelRow({
  href,
  name,
  Icon,
  active,
  badge,
}: {
  href: string;
  name: string;
  Icon: React.ComponentType<{ className?: string }>;
  active?: boolean;
  badge?: number;
}) {
  return (
    <Link
      href={href}
      className={cn(
        "flex items-center gap-2 rounded-md px-2 py-1.5 text-[13px] transition",
        active
          ? "bg-accent text-white"
          : badge
            ? "font-semibold text-white hover:bg-white/10"
            : "text-white/75 hover:bg-white/10 hover:text-white",
      )}
    >
      <Icon className="h-3.5 w-3.5 opacity-70" />
      <span className="flex-1 truncate">{name}</span>
      {badge ? (
        <span
          className={cn(
            "rounded-full px-1.5 py-0.5 text-[10px] font-bold leading-none",
            active ? "bg-white/20 text-white" : "bg-status-red text-white",
          )}
        >
          {badge > 99 ? "99+" : badge}
        </span>
      ) : null}
    </Link>
  );
}

function NewChannelButton({ onCreated }: { onCreated: () => void }) {
  const { wsId } = useParams<{ wsId: string }>();
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [topic, setTopic] = useState("");
  const [type, setType] = useState<"public" | "private">("public");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) {
      setName("");
      setTopic("");
      setError(null);
    }
  }, [open]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setSubmitting(true);
    setError(null);
    try {
      const ch = await channelsApi.create(wsId, {
        name,
        topic: topic || undefined,
        type,
      });
      onCreated();
      setOpen(false);
      router.push(`/w/${wsId}/c/${ch.id}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not create");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <>
      <button
        className="rounded-md p-1 text-white/50 transition hover:bg-white/10 hover:text-white"
        onClick={() => setOpen(true)}
        aria-label="New channel"
        title="New channel"
      >
        <Plus className="h-3.5 w-3.5" />
      </button>

      {open ? (
        <div className="fixed inset-0 z-50 grid place-items-center bg-ink/60 p-4">
          <form
            onSubmit={submit}
            className="w-full max-w-md space-y-4 rounded-xl border border-line bg-app p-6 shadow-lg"
          >
            <h3 className="text-lg font-semibold text-ink">New channel</h3>
            <Field label="Name">
              <Input
                required
                value={name}
                onChange={(e) =>
                  setName(e.target.value.toLowerCase().replace(/\s+/g, "-"))
                }
                placeholder="eng-all-hands"
              />
            </Field>
            <Field label="Topic">
              <Input
                value={topic}
                onChange={(e) => setTopic(e.target.value)}
                placeholder="What's this channel about?"
              />
            </Field>
            <Field label="Type">
              <select
                value={type}
                onChange={(e) =>
                  setType(e.target.value as "public" | "private")
                }
                className="h-10 w-full rounded-md border border-line bg-app-2 px-3 text-sm text-ink"
              >
                <option value="public">Public — anyone can join</option>
                <option value="private">Private — invite only</option>
              </select>
            </Field>
            {error ? <p className="text-sm text-status-red">{error}</p> : null}
            <div className="flex justify-end gap-2">
              <Button type="button" variant="ghost" onClick={() => setOpen(false)}>
                Cancel
              </Button>
              <Button type="submit" loading={submitting} disabled={submitting || !name}>
                Create
              </Button>
            </div>
          </form>
        </div>
      ) : null}
    </>
  );
}
