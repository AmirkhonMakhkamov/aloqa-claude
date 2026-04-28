// Caches workspace members so messages can render display names / avatars.
//
// The backend has no public "get user by id" endpoint, but any workspace member
// can call /admin/members (RBAC grants `member.read` to role=member). We load
// the full roster once per active workspace and look up by user_id from there.
// On 403 (guests have no member.read? — actually they do, but be defensive) we
// silently fall back to an empty cache; messages will show truncated user IDs.

import { create } from "zustand";
import { adminApi } from "@/lib/api/endpoints";
import type { User, UUID } from "@/lib/types";

interface MembersState {
  // workspaceId → (userId → User)
  byWorkspace: Record<UUID, Record<UUID, User>>;
  loading: Record<UUID, boolean>;
  loadedAt: Record<UUID, number>;

  ensureLoaded: (wsId: UUID) => Promise<void>;
  upsert: (wsId: UUID, user: User) => void;
  get: (wsId: UUID, userId: UUID) => User | null;
  reset: () => void;
}

// Full refresh at most every 5 minutes per workspace.
const STALE_MS = 5 * 60 * 1000;

export const useMembers = create<MembersState>((set, get) => ({
  byWorkspace: {},
  loading: {},
  loadedAt: {},

  async ensureLoaded(wsId) {
    const state = get();
    if (state.loading[wsId]) return;
    const age = Date.now() - (state.loadedAt[wsId] ?? 0);
    if (state.byWorkspace[wsId] && age < STALE_MS) return;

    set({ loading: { ...state.loading, [wsId]: true } });
    try {
      // Pull in chunks; 200 is a reasonable ceiling for most workspaces.
      // Backend returns a bare `[]WorkspaceMember` (no `items` envelope).
      const roster = (await adminApi.members(wsId, 200, 0)) ?? [];
      const map: Record<UUID, User> = { ...(get().byWorkspace[wsId] ?? {}) };
      for (const m of roster) {
        if (m.user) map[m.user_id] = m.user;
      }
      set({
        byWorkspace: { ...get().byWorkspace, [wsId]: map },
        loadedAt: { ...get().loadedAt, [wsId]: Date.now() },
      });
    } catch {
      // Silent fail — UI falls back to truncated user ids.
    } finally {
      set({ loading: { ...get().loading, [wsId]: false } });
    }
  },

  upsert(wsId, user) {
    const cur = get().byWorkspace[wsId] ?? {};
    if (cur[user.id] && cur[user.id].updated_at === user.updated_at) return;
    set({
      byWorkspace: {
        ...get().byWorkspace,
        [wsId]: { ...cur, [user.id]: user },
      },
    });
  },

  get(wsId, userId) {
    return get().byWorkspace[wsId]?.[userId] ?? null;
  },

  reset() {
    set({ byWorkspace: {}, loading: {}, loadedAt: {} });
  },
}));

export function shortId(id: UUID): string {
  // Fallback when we can't resolve a user — first 8 chars of the UUID.
  return id.slice(0, 8);
}
