"use client";

import { useEffect, useState } from "react";
import { useParams } from "next/navigation";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { adminApi } from "@/lib/api/endpoints";
import type { AuditEntry } from "@/lib/types";
import { useMembers, shortId } from "@/stores/members";

export default function AuditLogPage() {
  const { wsId } = useParams<{ wsId: string }>();
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [filter, setFilter] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const ensureMembers = useMembers((s) => s.ensureLoaded);

  const limit = 50;

  async function load(nextOffset: number) {
    setLoading(true);
    try {
      const resp = await adminApi.auditLog(wsId, limit, nextOffset);
      setEntries(resp.entries);
      setTotal(resp.total);
      setOffset(nextOffset);
      setErr(null);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "failed to load");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void ensureMembers(wsId);
    void load(0);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wsId]);

  const filtered = filter
    ? entries.filter((e) => {
        const q = filter.toLowerCase();
        return (
          e.action.toLowerCase().includes(q) ||
          (e.target_type?.toLowerCase().includes(q) ?? false) ||
          (e.target_id?.toLowerCase().includes(q) ?? false)
        );
      })
    : entries;

  return (
    <div className="mx-auto max-w-5xl space-y-4 px-6 py-6">
      <div className="flex items-center gap-3">
        <Input
          placeholder="Filter by action or target"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          className="max-w-sm"
        />
        <div className="ml-auto text-xs text-slate-500">
          Showing {offset + 1}–{Math.min(offset + limit, total)} of {total}
        </div>
      </div>

      {err ? (
        <div className="rounded-md border border-rose-900/60 bg-rose-950/40 p-3 text-sm text-rose-200">
          {err}
        </div>
      ) : null}

      <div className="overflow-hidden rounded-lg border border-ink-line">
        <table className="w-full text-sm">
          <thead className="bg-ink-muted/60 text-[11px] uppercase tracking-wide text-slate-500">
            <tr>
              <th className="py-2 pl-4 pr-2 text-left font-medium">When</th>
              <th className="px-2 py-2 text-left font-medium">Actor</th>
              <th className="px-2 py-2 text-left font-medium">Action</th>
              <th className="px-2 py-2 text-left font-medium">Target</th>
              <th className="py-2 pl-2 pr-4 text-left font-medium">Metadata</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-ink-line">
            {loading ? (
              <tr>
                <td className="p-4 text-slate-500" colSpan={5}>
                  Loading…
                </td>
              </tr>
            ) : filtered.length === 0 ? (
              <tr>
                <td className="p-4 text-slate-500" colSpan={5}>
                  Nothing yet.
                </td>
              </tr>
            ) : (
              filtered.map((e) => (
                <AuditRow key={e.id} entry={e} wsId={wsId} />
              ))
            )}
          </tbody>
        </table>
      </div>

      <div className="flex items-center justify-end gap-2">
        <Button
          size="sm"
          variant="outline"
          disabled={offset === 0 || loading}
          onClick={() => load(Math.max(0, offset - limit))}
        >
          Previous
        </Button>
        <Button
          size="sm"
          variant="outline"
          disabled={offset + limit >= total || loading}
          onClick={() => load(offset + limit)}
        >
          Next
        </Button>
      </div>
    </div>
  );
}

function AuditRow({ entry, wsId }: { entry: AuditEntry; wsId: string }) {
  const actor = useMembers((s) => (entry.actor_id ? s.get(wsId, entry.actor_id) : null));
  const actorLabel = actor?.display_name ?? (entry.actor_id ? shortId(entry.actor_id) : "system");
  return (
    <tr>
      <td className="py-2 pl-4 pr-2 text-slate-400 whitespace-nowrap">
        {new Date(entry.created_at).toLocaleString()}
      </td>
      <td className="px-2 py-2 text-slate-200">{actorLabel}</td>
      <td className="px-2 py-2">
        <span className="rounded-md bg-accent/10 px-1.5 py-0.5 font-mono text-xs text-accent-soft">
          {entry.action}
        </span>
      </td>
      <td className="px-2 py-2 text-slate-400">
        {entry.target_type ? (
          <span>
            {entry.target_type}
            {entry.target_id ? (
              <span className="ml-1 font-mono text-[11px] text-slate-500">
                {entry.target_id.slice(0, 12)}
              </span>
            ) : null}
          </span>
        ) : (
          "—"
        )}
      </td>
      <td className="py-2 pl-2 pr-4 text-[11px] text-slate-500">
        {entry.metadata ? (
          <code className="break-all font-mono">{JSON.stringify(entry.metadata)}</code>
        ) : (
          "—"
        )}
      </td>
    </tr>
  );
}
