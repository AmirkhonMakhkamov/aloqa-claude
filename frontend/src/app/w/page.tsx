"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { AuthError } from "@/components/auth/AuthShell";
import { Button } from "@/components/ui/Button";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { workspacesApi } from "@/lib/api/endpoints";
import { useAuth } from "@/stores/auth";
import { useWorkspace } from "@/stores/workspace";

/*
 * Workspace picker sits between login and the shell. If the user has
 * exactly one workspace we auto-redirect — the picker is only worth
 * rendering when there's a real choice to make. The "create new" form
 * is inline (not a modal) because this is a dedicated page: the user
 * is already here to pick or create, nothing to dismiss.
 */
export default function WorkspacePickerPage() {
  const router = useRouter();
  const user = useAuth((s) => s.user);
  const loadingAuth = useAuth((s) => s.loading);
  const workspaces = useWorkspace((s) => s.workspaces);
  const loadWorkspaces = useWorkspace((s) => s.loadWorkspaces);

  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (loadingAuth) return;
    if (!user) {
      router.replace("/login");
      return;
    }
    void loadWorkspaces();
  }, [loadingAuth, user, loadWorkspaces, router]);

  useEffect(() => {
    if (workspaces.length === 1 && !creating) {
      router.replace(`/w/${workspaces[0].id}`);
    }
  }, [workspaces, creating, router]);

  async function onCreate(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      const ws = await workspacesApi.create(name, slug || slugify(name));
      router.replace(`/w/${ws.id}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not create workspace");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <main className="mx-auto flex min-h-full max-w-3xl flex-col gap-8 px-6 py-14">
      <header className="space-y-2">
        <span className="inline-block rounded-full bg-accent-dim px-3 py-1 text-[11px] font-semibold uppercase tracking-wider text-accent">
          Workspaces
        </span>
        <h1 className="text-[28px] font-semibold text-ink">Pick a workspace</h1>
        <p className="text-sm text-ink-2">
          Jump into an existing workspace, or create a new one for your team.
        </p>
      </header>

      {workspaces.length > 0 ? (
        <ul className="grid gap-3 sm:grid-cols-2">
          {workspaces.map((ws) => (
            <li key={ws.id}>
              <button
                type="button"
                onClick={() => router.push(`/w/${ws.id}`)}
                className="group flex w-full items-center gap-3 rounded-xl border border-line bg-app p-4 text-left transition hover:border-accent hover:bg-app-2 hover:shadow-sm"
              >
                <div className="grid h-10 w-10 shrink-0 place-items-center rounded-lg bg-accent text-sm font-semibold text-white transition group-hover:scale-105">
                  {ws.name.slice(0, 2).toUpperCase()}
                </div>
                <div className="min-w-0 flex-1">
                  <div className="truncate text-[15px] font-semibold text-ink">
                    {ws.name}
                  </div>
                  <div className="truncate text-[12px] text-ink-3">/{ws.slug}</div>
                </div>
              </button>
            </li>
          ))}
        </ul>
      ) : (
        <div className="rounded-xl border border-dashed border-line bg-app p-8 text-center text-sm text-ink-2">
          You&apos;re not a member of any workspace yet. Create your first one
          below to get started.
        </div>
      )}

      <section className="space-y-4 rounded-xl border border-line bg-app p-6">
        <div className="flex items-center justify-between gap-3">
          <div>
            <h2 className="text-base font-semibold text-ink">
              Create a workspace
            </h2>
            <p className="text-[13px] text-ink-2">
              You&apos;ll become the owner and can invite teammates later.
            </p>
          </div>
          {!creating ? (
            <Button variant="outline" onClick={() => setCreating(true)}>
              New workspace
            </Button>
          ) : null}
        </div>

        {creating ? (
          <form className="grid gap-4 sm:grid-cols-2" onSubmit={onCreate}>
            <Field label="Name" className="sm:col-span-2" htmlFor="ws-name">
              <Input
                id="ws-name"
                required
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="Acme Inc."
                autoFocus
              />
            </Field>
            <Field label="Slug" htmlFor="ws-slug" hint="Used in URLs. Lowercase, hyphen-separated.">
              <Input
                id="ws-slug"
                value={slug}
                onChange={(e) => setSlug(e.target.value)}
                placeholder="acme"
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
                  setCreating(false);
                  setError(null);
                }}
                disabled={submitting}
              >
                Cancel
              </Button>
            </div>
            {error ? (
              <div className="sm:col-span-2">
                <AuthError message={error} />
              </div>
            ) : null}
          </form>
        ) : null}
      </section>
    </main>
  );
}

function slugify(s: string): string {
  return s
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 48);
}
