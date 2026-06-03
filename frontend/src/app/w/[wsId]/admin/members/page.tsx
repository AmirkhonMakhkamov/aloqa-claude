"use client";

import { useEffect, useState } from "react";
import { useParams } from "next/navigation";
import { Shield, UserMinus, UserX } from "lucide-react";
import { Avatar } from "@/components/ui/Avatar";
import { Input } from "@/components/ui/Input";
import { adminApi } from "@/lib/api/endpoints";
import type { WorkspaceMember } from "@/lib/types";

type Role = WorkspaceMember["role"];
const ROLES: Role[] = ["owner", "admin", "member", "guest"];

export default function MembersAdminPage() {
  const { wsId } = useParams<{ wsId: string }>();
  const [members, setMembers] = useState<WorkspaceMember[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [filter, setFilter] = useState("");
  const [err, setErr] = useState<string | null>(null);

  async function refresh() {
    setLoading(true);
    try {
      // Backend returns a bare array; tolerate `null` just in case.
      const roster = (await adminApi.members(wsId, 200, 0)) ?? [];
      setMembers(roster);
      setTotal(roster.length);
      setErr(null);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "failed to load");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wsId]);

  const rows = filter
    ? members.filter((m) => {
        const q = filter.toLowerCase();
        return (
          m.user?.display_name.toLowerCase().includes(q) ||
          m.user?.email.toLowerCase().includes(q) ||
          m.role.toLowerCase().includes(q)
        );
      })
    : members;

  async function setRole(userId: string, role: Role) {
    try {
      await adminApi.updateMemberRole(wsId, userId, role);
      await refresh();
    } catch (e) {
      setErr(e instanceof Error ? e.message : "role update failed");
    }
  }

  async function remove(userId: string) {
    if (!confirm("Remove this member from the workspace?")) return;
    try {
      await adminApi.removeMember(wsId, userId);
      await refresh();
    } catch (e) {
      setErr(e instanceof Error ? e.message : "remove failed");
    }
  }

  async function suspend(userId: string, currentlySuspended: boolean) {
    try {
      if (currentlySuspended) await adminApi.reactivate(wsId, userId);
      else await adminApi.suspend(wsId, userId);
      await refresh();
    } catch (e) {
      setErr(e instanceof Error ? e.message : "suspend/reactivate failed");
    }
  }

  return (
    <div className="mx-auto max-w-5xl space-y-4 px-6 py-6">
      <div className="flex items-center gap-3">
        <Input
          placeholder="Filter by name, email, or role"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          className="max-w-sm"
        />
        <div className="ml-auto text-xs text-slate-500">{total} total</div>
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
              <th className="py-2 pl-4 pr-2 text-left font-medium">Name</th>
              <th className="px-2 py-2 text-left font-medium">Email</th>
              <th className="px-2 py-2 text-left font-medium">Role</th>
              <th className="px-2 py-2 text-left font-medium">Status</th>
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
            ) : rows.length === 0 ? (
              <tr>
                <td className="p-4 text-slate-500" colSpan={5}>
                  No members match that filter.
                </td>
              </tr>
            ) : (
              rows.map((m) => {
                const u = m.user;
                const suspended = u?.status === "suspended";
                return (
                  <tr key={m.id} className="hover:bg-white/[0.02]">
                    <td className="py-2 pl-4 pr-2">
                      <div className="flex items-center gap-3">
                        <Avatar
                          name={u?.display_name}
                          src={u?.avatar_url ?? null}
                          color={u?.avatar_color}
                          size={28}
                        />
                        <span className="font-medium text-slate-100">
                          {u?.display_name ?? "—"}
                        </span>
                      </div>
                    </td>
                    <td className="px-2 py-2 text-slate-400">{u?.email ?? "—"}</td>
                    <td className="px-2 py-2">
                      <select
                        value={m.role}
                        onChange={(e) => setRole(m.user_id, e.target.value as Role)}
                        className="rounded-md border border-ink-line bg-ink-muted px-2 py-1 text-xs text-slate-100"
                      >
                        {ROLES.map((r) => (
                          <option key={r} value={r}>
                            {r}
                          </option>
                        ))}
                      </select>
                    </td>
                    <td className="px-2 py-2">
                      {suspended ? (
                        <span className="rounded-full border border-amber-700/50 bg-amber-900/20 px-2 py-0.5 text-[10px] text-amber-300">
                          Suspended
                        </span>
                      ) : (
                        <span className="rounded-full border border-emerald-800/50 bg-emerald-900/20 px-2 py-0.5 text-[10px] text-emerald-300">
                          Active
                        </span>
                      )}
                    </td>
                    <td className="py-2 pl-2 pr-4 text-right">
                      <div className="inline-flex items-center gap-1">
                        <button
                          onClick={() => suspend(m.user_id, suspended)}
                          className="rounded-md border border-ink-line px-2 py-1 text-[11px] text-slate-300 hover:bg-white/5"
                          title={suspended ? "Reactivate" : "Suspend"}
                        >
                          <Shield className="inline h-3 w-3" />{" "}
                          {suspended ? "Reactivate" : "Suspend"}
                        </button>
                        <button
                          onClick={() => remove(m.user_id)}
                          className="rounded-md border border-ink-line px-2 py-1 text-[11px] text-rose-300 hover:bg-rose-950/40"
                          title="Remove"
                        >
                          <UserMinus className="inline h-3 w-3" /> Remove
                        </button>
                      </div>
                    </td>
                  </tr>
                );
              })
            )}
          </tbody>
        </table>
      </div>
      <p className="text-[11px] text-slate-600">
        <UserX className="mr-1 inline h-3 w-3" />
        Owner role can only be held by one user; changing it requires owner
        privileges and may be rejected server-side.
      </p>
    </div>
  );
}
