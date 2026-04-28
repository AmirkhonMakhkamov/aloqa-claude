"use client";

import { useEffect, useState } from "react";
import { useParams } from "next/navigation";
import { Copy, Plus, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/Button";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { invitesApi } from "@/lib/api/endpoints";
import type { GuestInvite } from "@/lib/types";

export default function InvitesAdminPage() {
  const { wsId } = useParams<{ wsId: string }>();
  const [invites, setInvites] = useState<GuestInvite[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState<string | null>(null);
  const [open, setOpen] = useState(false);

  const [email, setEmail] = useState("");
  const [maxUses, setMaxUses] = useState(1);
  const [ttlHours, setTtlHours] = useState(72);
  const [submitting, setSubmitting] = useState(false);

  async function refresh() {
    setLoading(true);
    try {
      // Go nil slice serializes to `null` — coerce for safe iteration.
      const list = (await invitesApi.list(wsId)) ?? [];
      setInvites(list);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "failed to load invites");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wsId]);

  async function create(e: React.FormEvent) {
    e.preventDefault();
    setSubmitting(true);
    try {
      await invitesApi.create(wsId, {
        email: email || undefined,
        max_uses: maxUses,
        ttl_hours: ttlHours,
      });
      setOpen(false);
      setEmail("");
      setMaxUses(1);
      setTtlHours(72);
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : "create failed");
    } finally {
      setSubmitting(false);
    }
  }

  async function revoke(id: string) {
    if (!confirm("Revoke this invite? Existing links will stop working.")) return;
    try {
      await invitesApi.revoke(wsId, id);
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : "revoke failed");
    }
  }

  function copyLink(token: string) {
    const url = `${window.location.origin}/invite/${token}`;
    void navigator.clipboard.writeText(url).then(() => {
      setCopied(token);
      window.setTimeout(() => setCopied(null), 1500);
    });
  }

  return (
    <div className="mx-auto max-w-4xl space-y-4 px-6 py-6">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-base font-semibold text-white">Guest invites</h2>
          <p className="text-xs text-slate-500">
            Share a link to let someone join a subset of this workspace.
          </p>
        </div>
        {!open ? (
          <Button onClick={() => setOpen(true)}>
            <Plus className="h-4 w-4" /> New invite
          </Button>
        ) : null}
      </div>

      {open ? (
        <form
          onSubmit={create}
          className="grid gap-3 rounded-lg border border-ink-line bg-ink-muted/40 p-4 sm:grid-cols-3"
        >
          <Field label="Email (optional)">
            <Input
              value={email}
              type="email"
              onChange={(e) => setEmail(e.target.value)}
              placeholder="name@example.com"
            />
          </Field>
          <Field label="Max uses">
            <Input
              type="number"
              min={1}
              max={100}
              value={maxUses}
              onChange={(e) => setMaxUses(Number(e.target.value))}
            />
          </Field>
          <Field label="Expires (hours)">
            <Input
              type="number"
              min={1}
              max={720}
              value={ttlHours}
              onChange={(e) => setTtlHours(Number(e.target.value))}
            />
          </Field>
          <div className="flex items-end justify-end gap-2 sm:col-span-3">
            <Button type="button" variant="ghost" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button type="submit" loading={submitting}>
              Create
            </Button>
          </div>
        </form>
      ) : null}

      {error ? (
        <div className="rounded-md border border-rose-900/60 bg-rose-950/40 p-3 text-sm text-rose-200">
          {error}
        </div>
      ) : null}

      <div className="overflow-hidden rounded-lg border border-ink-line">
        <table className="w-full text-sm">
          <thead className="bg-ink-muted/60 text-[11px] uppercase tracking-wide text-slate-500">
            <tr>
              <th className="py-2 pl-4 pr-2 text-left font-medium">Token</th>
              <th className="px-2 py-2 text-left font-medium">Email</th>
              <th className="px-2 py-2 text-left font-medium">Uses</th>
              <th className="px-2 py-2 text-left font-medium">Expires</th>
              <th className="py-2 pl-2 pr-4 text-right font-medium">Actions</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-ink-line">
            {loading ? (
              <tr>
                <td className="p-4 text-slate-500" colSpan={5}>
                  Loading…
                </td>
              </tr>
            ) : invites.length === 0 ? (
              <tr>
                <td className="p-4 text-slate-500" colSpan={5}>
                  No invites.
                </td>
              </tr>
            ) : (
              invites.map((inv) => {
                const expired = Date.now() > new Date(inv.expires_at).getTime();
                const revoked = Boolean(inv.revoked_at);
                return (
                  <tr key={inv.id}>
                    <td className="py-2 pl-4 pr-2 font-mono text-xs text-slate-300">
                      {inv.token.slice(0, 10)}…
                    </td>
                    <td className="px-2 py-2 text-slate-400">{inv.email ?? "—"}</td>
                    <td className="px-2 py-2 text-slate-400">
                      {inv.use_count}/{inv.max_uses}
                    </td>
                    <td className="px-2 py-2 text-slate-400">
                      {new Date(inv.expires_at).toLocaleString()}
                      {revoked ? (
                        <span className="ml-2 rounded-full border border-amber-700/50 bg-amber-900/20 px-1.5 py-0.5 text-[10px] text-amber-300">
                          revoked
                        </span>
                      ) : expired ? (
                        <span className="ml-2 rounded-full border border-slate-700 px-1.5 py-0.5 text-[10px] text-slate-500">
                          expired
                        </span>
                      ) : null}
                    </td>
                    <td className="py-2 pl-2 pr-4 text-right">
                      <div className="inline-flex items-center gap-1">
                        <button
                          onClick={() => copyLink(inv.token)}
                          className="rounded-md border border-ink-line px-2 py-1 text-[11px] text-slate-300 hover:bg-white/5"
                        >
                          <Copy className="inline h-3 w-3" />{" "}
                          {copied === inv.token ? "Copied!" : "Copy link"}
                        </button>
                        {!revoked ? (
                          <button
                            onClick={() => revoke(inv.id)}
                            className="rounded-md border border-ink-line px-2 py-1 text-[11px] text-rose-300 hover:bg-rose-950/40"
                          >
                            <Trash2 className="inline h-3 w-3" /> Revoke
                          </button>
                        ) : null}
                      </div>
                    </td>
                  </tr>
                );
              })
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}
