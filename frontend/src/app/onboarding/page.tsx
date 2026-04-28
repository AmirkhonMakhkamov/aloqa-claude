"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { Briefcase, User as UserIcon } from "lucide-react";
import { AuthError } from "@/components/auth/AuthShell";
import { Button } from "@/components/ui/Button";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { workspacesApi } from "@/lib/api/endpoints";
import { cn } from "@/lib/utils";
import { useAuth } from "@/stores/auth";
import { useWorkspace } from "@/stores/workspace";

/*
 * Post-register onboarding chooser. The product spec calls out two entry
 * paths — "Create a workspace (becomes admin)" and "Sign up as a
 * standalone user" — but the auth flow only ever produced an account and
 * dropped the user into the workspace picker. Returning users with at
 * least one workspace skip this page (we send them straight to /w to
 * pick); first-time users land here so they can declare intent before we
 * provision a personal workspace under them.
 *
 * "Standalone" is implemented as the auto-created personal workspace
 * (entity.WorkspaceKindPersonal), not a separate route tree. That lets
 * us reuse the same channel/calls/files surface without duplicating the
 * shell — the only visible difference is a "Personal" pill in the rail
 * (handled in the workspace store layer).
 */

type Mode = "choose" | "create";

export default function OnboardingPage() {
  const router = useRouter();
  const user = useAuth((s) => s.user);
  const loadingAuth = useAuth((s) => s.loading);
  const workspaces = useWorkspace((s) => s.workspaces);
  const loadWorkspaces = useWorkspace((s) => s.loadWorkspaces);

  const [mode, setMode] = useState<Mode>("choose");
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Auth gate: if the user isn't signed in (e.g. they hit /onboarding
  // directly), bounce to login. If they ARE signed in but already belong
  // to a real (non-personal) workspace, skip onboarding entirely — the
  // chooser only makes sense for fresh accounts.
  useEffect(() => {
    if (loadingAuth) return;
    if (!user) {
      router.replace("/login");
      return;
    }
    void loadWorkspaces();
  }, [loadingAuth, user, loadWorkspaces, router]);

  useEffect(() => {
    const realOnes = workspaces.filter((w) => w.kind !== "personal");
    if (realOnes.length > 0) {
      router.replace("/w");
    }
  }, [workspaces, router]);

  async function chooseStandalone() {
    setError(null);
    setSubmitting(true);
    try {
      // GET /api/v1/personal lazily creates the personal workspace if one
      // doesn't exist yet. We then redirect into the standard shell using
      // the personal workspace's id — the backend mounts the same routes
      // under /api/v1/workspaces/{personalId}, so nothing else changes.
      const ws = await workspacesApi.personal();
      router.replace(`/w/${ws.id}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not set up your space");
      setSubmitting(false);
    }
  }

  async function chooseWorkspace(e: React.FormEvent) {
    e.preventDefault();
    if (!name) return;
    setError(null);
    setSubmitting(true);
    try {
      const ws = await workspacesApi.create(name, slug || slugify(name));
      router.replace(`/w/${ws.id}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not create workspace");
      setSubmitting(false);
    }
  }

  if (loadingAuth || !user) {
    return (
      <main className="flex h-full items-center justify-center text-sm text-ink-3">
        <span className="h-3.5 w-3.5 animate-spin rounded-full border-2 border-line border-t-accent" />
        <span className="ml-2">Loading…</span>
      </main>
    );
  }

  return (
    <main className="relative mx-auto flex min-h-full max-w-3xl flex-col gap-10 px-6 py-14">
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0"
        style={{
          background:
            "radial-gradient(900px 400px at 90% -10%, color-mix(in oklab, var(--accent) 12%, transparent) 0%, transparent 60%)",
        }}
      />
      <header className="relative space-y-3 text-center">
        <span className="inline-block rounded-full bg-accent-dim px-3 py-1 text-[11px] font-semibold uppercase tracking-wider text-accent">
          Welcome to Aloqa
        </span>
        <h1 className="text-[28px] font-semibold text-ink">
          How do you want to start, {firstName(user.display_name)}?
        </h1>
        <p className="text-sm text-ink-2">
          Pick one — you can always create a workspace later or invite teammates
          into your personal space.
        </p>
      </header>

      {mode === "choose" ? (
        <section className="relative grid gap-4 sm:grid-cols-2">
          <ChoiceCard
            Icon={Briefcase}
            title="Create a workspace"
            body="For teams. You become the workspace admin and can invite members, build channels, and host meetings."
            cta="Create workspace"
            onClick={() => setMode("create")}
            disabled={submitting}
          />
          <ChoiceCard
            Icon={UserIcon}
            title="Continue as standalone"
            body="For solo use. You get a personal space — channels, DMs, and meetings — without managing a team."
            cta="Use standalone"
            onClick={chooseStandalone}
            loading={submitting}
            secondary
          />
          {error ? (
            <div className="sm:col-span-2">
              <AuthError message={error} />
            </div>
          ) : null}
        </section>
      ) : (
        <section className="relative space-y-4 rounded-xl border border-line bg-app p-6 shadow-sm">
          <div>
            <h2 className="text-lg font-semibold text-ink">Name your workspace</h2>
            <p className="text-[13px] text-ink-2">
              You&apos;ll become the owner and can invite teammates from settings.
            </p>
          </div>
          <form className="grid gap-4 sm:grid-cols-2" onSubmit={chooseWorkspace}>
            <Field label="Name" className="sm:col-span-2" htmlFor="ob-name">
              <Input
                id="ob-name"
                required
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="Acme Inc."
                autoFocus
              />
            </Field>
            <Field
              label="Slug"
              htmlFor="ob-slug"
              hint="Used in URLs. Lowercase, hyphen-separated."
            >
              <Input
                id="ob-slug"
                value={slug}
                onChange={(e) => setSlug(e.target.value)}
                placeholder={name ? slugify(name) : "acme"}
              />
            </Field>
            <div className="flex items-end gap-2">
              <Button type="submit" loading={submitting} disabled={submitting || !name}>
                Create workspace
              </Button>
              <Button
                type="button"
                variant="ghost"
                onClick={() => {
                  setMode("choose");
                  setError(null);
                }}
                disabled={submitting}
              >
                Back
              </Button>
            </div>
            {error ? (
              <div className="sm:col-span-2">
                <AuthError message={error} />
              </div>
            ) : null}
          </form>
        </section>
      )}
    </main>
  );
}

function ChoiceCard({
  Icon,
  title,
  body,
  cta,
  onClick,
  disabled,
  loading,
  secondary,
}: {
  Icon: React.ComponentType<{ className?: string }>;
  title: string;
  body: string;
  cta: string;
  onClick: () => void;
  disabled?: boolean;
  loading?: boolean;
  secondary?: boolean;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled || loading}
      className={cn(
        "group flex h-full flex-col items-start gap-3 rounded-xl border p-6 text-left transition disabled:cursor-not-allowed disabled:opacity-60",
        secondary
          ? "border-line bg-app hover:border-accent hover:bg-app-2"
          : "border-line bg-app hover:border-accent hover:bg-app-2 hover:shadow-md",
      )}
    >
      <div
        className={cn(
          "grid h-11 w-11 place-items-center rounded-lg transition group-hover:scale-105",
          secondary ? "bg-app-2 text-ink" : "bg-accent text-white",
        )}
      >
        <Icon className="h-5 w-5" />
      </div>
      <div className="space-y-1">
        <div className="text-[16px] font-semibold text-ink">{title}</div>
        <p className="text-[13px] text-ink-2">{body}</p>
      </div>
      <span
        className={cn(
          "mt-auto inline-flex items-center gap-1 text-[13px] font-medium",
          secondary ? "text-ink" : "text-accent",
        )}
      >
        {loading ? (
          <span className="h-3 w-3 animate-spin rounded-full border-2 border-current border-t-transparent" />
        ) : null}
        {cta} →
      </span>
    </button>
  );
}

function firstName(displayName: string): string {
  const t = displayName.trim();
  if (!t) return "there";
  return t.split(/\s+/)[0];
}

function slugify(s: string): string {
  return s
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 48);
}
