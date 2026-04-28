import { create } from "zustand";
import { channelsApi, workspacesApi } from "@/lib/api/endpoints";
import type { Channel, UnreadCount, Workspace } from "@/lib/types";

interface WorkspaceState {
  workspaces: Workspace[];
  activeId: string | null;
  channels: Channel[];
  unread: Record<string, number>;
  loading: boolean;
  error: string | null;

  loadWorkspaces: () => Promise<void>;
  setActive: (id: string) => Promise<void>;
  refreshChannels: () => Promise<void>;
  refreshUnread: () => Promise<void>;
  upsertChannel: (ch: Channel) => void;
}

export const useWorkspace = create<WorkspaceState>((set, get) => ({
  workspaces: [],
  activeId: null,
  channels: [],
  unread: {},
  loading: false,
  error: null,

  async loadWorkspaces() {
    set({ loading: true, error: null });
    try {
      const list = await workspacesApi.list();
      set({ workspaces: list, loading: false });
      // Auto-select first workspace if none active.
      if (!get().activeId && list.length) {
        await get().setActive(list[0].id);
      }
    } catch (e) {
      set({ loading: false, error: e instanceof Error ? e.message : "load failed" });
    }
  },

  async setActive(id) {
    set({ activeId: id, channels: [], unread: {} });
    await Promise.all([get().refreshChannels(), get().refreshUnread()]);
  },

  async refreshChannels() {
    const id = get().activeId;
    if (!id) return;
    try {
      // Go serializes an empty slice as `null` rather than `[]`, which would
      // otherwise blow up downstream consumers that expect an array.
      const chs = (await channelsApi.list(id)) ?? [];
      set({ channels: chs });
    } catch {
      // ignore; keep whatever we had
    }
  },

  async refreshUnread() {
    const id = get().activeId;
    if (!id) return;
    try {
      const list = await channelsApi.unread(id);
      const map: Record<string, number> = {};
      for (const u of list as UnreadCount[]) map[u.channel_id] = u.count;
      set({ unread: map });
    } catch {
      // ignore
    }
  },

  upsertChannel(ch) {
    const existing = get().channels.find((c) => c.id === ch.id);
    if (existing) {
      set({ channels: get().channels.map((c) => (c.id === ch.id ? ch : c)) });
    } else {
      set({ channels: [...get().channels, ch] });
    }
  },
}));
