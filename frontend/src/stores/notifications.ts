// Notifications store. Backs the rail bell + drawer.
//
// The backend serves three endpoints we care about:
//   - GET  /workspaces/{ws}/notifications             — paginated list
//   - GET  /workspaces/{ws}/notifications/unread-count — just a number
//   - POST /workspaces/{ws}/notifications/{id}/read    — mark one
//   - POST /workspaces/{ws}/notifications/read-all     — mark all
//
// For now we poll `unread-count` cheaply every 60s so the bell badge stays
// fresh without a WS subscription (that arrives in Phase 14). The full list
// only loads when the user opens the drawer — no reason to fetch 50 rows just
// to render a dot.

import { create } from "zustand";
import { notificationsApi } from "@/lib/api/endpoints";
import type { Notification, UUID } from "@/lib/types";

interface NotificationsState {
  /** Last workspace we loaded for. Lets us reset when switching. */
  workspaceId: UUID | null;
  items: Notification[];
  unread: number;
  loading: boolean;
  loadedOnce: boolean;
  /** Returns a cleanup function that stops polling. */
  startPolling: (wsId: UUID) => () => void;
  /** Fetches the list. Usually called when the drawer opens. */
  refresh: (wsId: UUID) => Promise<void>;
  /** Fetches just the unread count. Used by the poller. */
  refreshUnread: (wsId: UUID) => Promise<void>;
  markRead: (wsId: UUID, id: UUID) => Promise<void>;
  markAllRead: (wsId: UUID) => Promise<void>;
  reset: () => void;
}

// Exported so the rail can mount the poller in exactly one place.
const POLL_MS = 60_000;

export const useNotifications = create<NotificationsState>((set, get) => ({
  workspaceId: null,
  items: [],
  unread: 0,
  loading: false,
  loadedOnce: false,

  startPolling(wsId) {
    // If we're switching workspaces we drop the previous state entirely so a
    // stale list from another workspace doesn't flash.
    if (get().workspaceId !== wsId) {
      set({ workspaceId: wsId, items: [], unread: 0, loadedOnce: false });
    }
    // Fire one now, then at interval.
    void get().refreshUnread(wsId);
    const t = window.setInterval(() => {
      void get().refreshUnread(wsId);
    }, POLL_MS);
    return () => window.clearInterval(t);
  },

  async refresh(wsId) {
    set({ loading: true });
    try {
      const items = (await notificationsApi.list(wsId, 50)) ?? [];
      const unread = items.filter((n) => !n.read_at).length;
      set({ workspaceId: wsId, items, unread, loadedOnce: true });
    } catch {
      // Keep whatever we had. A transient error shouldn't blank the drawer.
    } finally {
      set({ loading: false });
    }
  },

  async refreshUnread(wsId) {
    try {
      const res = await notificationsApi.unreadCount(wsId);
      // When the list isn't loaded yet, the count endpoint is canonical;
      // otherwise keep the list-derived count to avoid a flicker if the user
      // just clicked "mark all read" and the server count is a tick behind.
      if (!get().loadedOnce) set({ unread: res.unread_count });
      else set({ unread: Math.max(res.unread_count, 0) });
    } catch {
      // swallow — the badge just stays at whatever it was
    }
  },

  async markRead(wsId, id) {
    // Optimistic: flip the row's read_at before the server confirms.
    const prev = get().items;
    const now = new Date().toISOString();
    const next = prev.map((n) => (n.id === id ? { ...n, read_at: now } : n));
    set({
      items: next,
      unread: next.filter((n) => !n.read_at).length,
    });
    try {
      await notificationsApi.markRead(wsId, id);
    } catch {
      // Roll back on failure. Uncommon enough that a plain fetch is fine.
      set({ items: prev, unread: prev.filter((n) => !n.read_at).length });
    }
  },

  async markAllRead(wsId) {
    const prev = get().items;
    const now = new Date().toISOString();
    set({
      items: prev.map((n) => ({ ...n, read_at: n.read_at ?? now })),
      unread: 0,
    });
    try {
      await notificationsApi.readAll(wsId);
    } catch {
      set({ items: prev, unread: prev.filter((n) => !n.read_at).length });
    }
  },

  reset() {
    set({
      workspaceId: null,
      items: [],
      unread: 0,
      loading: false,
      loadedOnce: false,
    });
  },
}));
