import { create } from "zustand";
import { authApi, accountApi } from "@/lib/api/endpoints";
import { clearTokens, loadTokens, saveTokens } from "@/lib/auth";
import { describeCurrentDevice } from "@/lib/device";
import type { Session, User } from "@/lib/types";

/*
 * Single source of truth for authentication state + active session roster.
 *
 * Why sessions live here rather than their own store: they're a read of the
 * *same* principal the rest of the UI already reads via `user`, the cost of
 * putting them behind another store is an extra subscription boundary for
 * every consumer. The Security panel only re-renders when `sessions` or
 * `sessionsLoading` change (selector-scoped), so there's no practical cost
 * to bundling them.
 *
 * Revoke / logoutAll flows:
 *  - revoking the *current* session (we identify it by the session_id saved
 *    at login) is a full client logout — tokens get cleared, user becomes
 *    null, router will bounce to /login via the guard
 *  - revoking any other session is server-side only; we just drop it from
 *    the local list so the UI updates without waiting on a refresh call
 *  - logoutAll is always a full client logout because *all* sessions for
 *    the user are gone, including the current one
 */

interface AuthState {
  user: User | null;
  loading: boolean;
  error: string | null;

  sessions: Session[];
  sessionsLoading: boolean;

  hydrate: () => Promise<void>;
  login: (email: string, password: string) => Promise<void>;
  register: (email: string, password: string, name: string) => Promise<void>;
  logout: () => Promise<void>;

  refreshSessions: () => Promise<void>;
  revokeSession: (sessionId: string) => Promise<void>;
  logoutAll: () => Promise<void>;
}

export const useAuth = create<AuthState>((set, get) => ({
  user: null,
  loading: true,
  error: null,

  sessions: [],
  sessionsLoading: false,

  async hydrate() {
    if (!loadTokens()) {
      set({ loading: false });
      return;
    }
    try {
      const me = await accountApi.me();
      set({ user: me, loading: false, error: null });
    } catch {
      clearTokens();
      set({ user: null, loading: false });
    }
  },

  async login(email, password) {
    set({ loading: true, error: null });
    try {
      const pair = await authApi.login(email, password, describeCurrentDevice());
      saveTokens(pair);
      const me = await accountApi.me();
      set({ user: me, loading: false });
    } catch (e) {
      set({ loading: false, error: e instanceof Error ? e.message : "login failed" });
      throw e;
    }
  },

  async register(email, password, name) {
    set({ loading: true, error: null });
    try {
      await authApi.register(email, password, name);
      const pair = await authApi.login(email, password, describeCurrentDevice());
      saveTokens(pair);
      const me = await accountApi.me();
      set({ user: me, loading: false });
    } catch (e) {
      set({
        loading: false,
        error: e instanceof Error ? e.message : "registration failed",
      });
      throw e;
    }
  },

  async logout() {
    try {
      await authApi.logout();
    } catch {
      // ignore; we clear locally regardless
    }
    clearTokens();
    set({ user: null, sessions: [] });
  },

  async refreshSessions() {
    set({ sessionsLoading: true });
    try {
      // Backend Go nil slice -> null; coerce.
      const list = (await authApi.sessions()) ?? [];
      set({ sessions: list, sessionsLoading: false });
    } catch {
      // On failure, keep whatever list we had — the UI shows a retry path.
      set({ sessionsLoading: false });
    }
  },

  async revokeSession(sessionId) {
    const tokens = loadTokens();
    const isSelf = tokens?.sessionId === sessionId;
    try {
      await authApi.logout(sessionId);
    } catch {
      // Even if the server call fails, fall through: for the current session
      // we still want to drop local tokens so the UI doesn't lie.
    }
    if (isSelf) {
      clearTokens();
      set({ user: null, sessions: [] });
      return;
    }
    set({ sessions: get().sessions.filter((s) => s.id !== sessionId) });
  },

  async logoutAll() {
    try {
      await authApi.logoutAll();
    } catch {
      // ignore
    }
    clearTokens();
    set({ user: null, sessions: [] });
  },
}));
