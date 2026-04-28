// Token storage + refresh lock. Single source of truth for the access token.
//
// Tradeoffs:
// - Tokens live in localStorage (XSS-exposed) because the backend returns them
//   in JSON rather than setting httpOnly cookies. For a production hardening
//   pass, move to httpOnly cookies + CSRF tokens. Kept as-is to stay 1:1 with
//   the current backend contract.
// - Refresh is serialized via a shared promise so 10 concurrent 401s don't fire
//   10 refresh requests.

import type { TokenPair } from "./types";

const ACCESS_KEY = "aloqa.access";
const REFRESH_KEY = "aloqa.refresh";
const SESSION_KEY = "aloqa.session";
const EXPIRES_AT_KEY = "aloqa.expires_at";

export interface StoredTokens {
  accessToken: string;
  refreshToken: string;
  sessionId: string;
  expiresAt: number; // epoch ms
}

function nowMs() {
  return Date.now();
}

export function loadTokens(): StoredTokens | null {
  if (typeof window === "undefined") return null;
  const access = localStorage.getItem(ACCESS_KEY);
  const refresh = localStorage.getItem(REFRESH_KEY);
  const session = localStorage.getItem(SESSION_KEY);
  const expiresAt = localStorage.getItem(EXPIRES_AT_KEY);
  if (!access || !refresh || !session || !expiresAt) return null;
  return {
    accessToken: access,
    refreshToken: refresh,
    sessionId: session,
    expiresAt: Number(expiresAt),
  };
}

export function saveTokens(pair: TokenPair): StoredTokens {
  const stored: StoredTokens = {
    accessToken: pair.access_token,
    refreshToken: pair.refresh_token,
    sessionId: pair.session_id,
    // Refresh 30s before expiry to avoid race with short-lived requests.
    expiresAt: nowMs() + Math.max(0, pair.expires_in - 30) * 1000,
  };
  if (typeof window !== "undefined") {
    localStorage.setItem(ACCESS_KEY, stored.accessToken);
    localStorage.setItem(REFRESH_KEY, stored.refreshToken);
    localStorage.setItem(SESSION_KEY, stored.sessionId);
    localStorage.setItem(EXPIRES_AT_KEY, String(stored.expiresAt));
  }
  return stored;
}

export function clearTokens() {
  if (typeof window === "undefined") return;
  localStorage.removeItem(ACCESS_KEY);
  localStorage.removeItem(REFRESH_KEY);
  localStorage.removeItem(SESSION_KEY);
  localStorage.removeItem(EXPIRES_AT_KEY);
}

export function isExpired(t: StoredTokens | null): boolean {
  if (!t) return true;
  return nowMs() >= t.expiresAt;
}

// Coordinate concurrent refreshes.
let inflightRefresh: Promise<StoredTokens> | null = null;

export async function refreshTokens(apiBase: string): Promise<StoredTokens> {
  if (inflightRefresh) return inflightRefresh;
  const stored = loadTokens();
  if (!stored) throw new Error("not authenticated");

  inflightRefresh = (async () => {
    try {
      const res = await fetch(`${apiBase}/api/v1/auth/refresh`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ refresh_token: stored.refreshToken }),
      });
      if (!res.ok) {
        clearTokens();
        throw new Error(`refresh failed: ${res.status}`);
      }
      const data = (await res.json()) as TokenPair;
      return saveTokens(data);
    } finally {
      inflightRefresh = null;
    }
  })();

  return inflightRefresh;
}
