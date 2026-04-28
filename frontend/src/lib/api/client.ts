// Low-level HTTP client. Every network call in the app funnels through `api()`:
// - Adds Authorization header
// - Handles 401 once by refreshing, then replays the request
// - Normalizes error envelopes into `ApiError`
// - Correlates requests with X-Request-ID

import { clearTokens, isExpired, loadTokens, refreshTokens } from "../auth";
import type { ApiErrorBody } from "../types";

export const API_BASE = process.env.NEXT_PUBLIC_API_BASE ?? "";

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
    public code?: string,
    public details?: unknown,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

interface RequestOptions extends Omit<RequestInit, "body"> {
  body?: unknown;
  query?: Record<string, string | number | boolean | undefined | null>;
  // If true, do not attach auth token.
  anonymous?: boolean;
  // If true, do not attempt a refresh on 401 (for the refresh endpoint itself).
  skipRefresh?: boolean;
}

function buildUrl(path: string, query?: RequestOptions["query"]): string {
  const base = path.startsWith("http") ? path : `${API_BASE}${path}`;
  if (!query) return base;
  const qs = new URLSearchParams();
  for (const [k, v] of Object.entries(query)) {
    if (v === undefined || v === null) continue;
    qs.set(k, String(v));
  }
  const s = qs.toString();
  return s ? `${base}?${s}` : base;
}

function newRequestId(): string {
  // Short correlation id; backend logs will reflect it under "request_id".
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
    return crypto.randomUUID();
  }
  return Math.random().toString(36).slice(2);
}

async function raw(path: string, opts: RequestOptions = {}): Promise<Response> {
  const headers = new Headers(opts.headers);
  headers.set("X-Request-ID", newRequestId());

  if (opts.body !== undefined && !(opts.body instanceof FormData)) {
    headers.set("Content-Type", "application/json");
  }
  headers.set("Accept", "application/json");

  if (!opts.anonymous) {
    let tokens = loadTokens();
    if (tokens && isExpired(tokens) && !opts.skipRefresh) {
      try {
        tokens = await refreshTokens(API_BASE);
      } catch {
        clearTokens();
        tokens = null;
      }
    }
    if (tokens) headers.set("Authorization", `Bearer ${tokens.accessToken}`);
  }

  const body =
    opts.body === undefined
      ? undefined
      : opts.body instanceof FormData
        ? opts.body
        : JSON.stringify(opts.body);

  return fetch(buildUrl(path, opts.query), {
    ...opts,
    headers,
    body,
  });
}

async function parseError(res: Response): Promise<ApiError> {
  const text = await res.text();
  try {
    const data = JSON.parse(text) as ApiErrorBody;
    // Backend shape: `{error: {code, message}}`. We also accept legacy flat
    // `{error: "msg", code, details}` in case that appears.
    if (typeof data.error === "object" && data.error !== null) {
      const inner = data.error;
      return new ApiError(
        res.status,
        inner.message ?? res.statusText,
        inner.code ?? data.code,
        data.details,
      );
    }
    return new ApiError(
      res.status,
      (data.error as string | undefined) ?? res.statusText,
      data.code,
      data.details,
    );
  } catch {
    return new ApiError(res.status, text || res.statusText);
  }
}

// 429 retries: the dev rate limiter trips often when several stores warm up
// in parallel after a navigation. Honour Retry-After if present, otherwise
// back off exponentially. Capped so a stuck endpoint doesn't stall the UI.
const MAX_RETRIES_429 = 2;

function parseRetryAfter(res: Response): number {
  const h = res.headers.get("Retry-After");
  if (!h) return 0;
  const n = Number(h);
  if (Number.isFinite(n)) return Math.max(0, n * 1000);
  const t = Date.parse(h);
  return Number.isNaN(t) ? 0 : Math.max(0, t - Date.now());
}

export async function api<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  let res = await raw(path, opts);

  // If we got 401 but have a refresh token, try once.
  if (res.status === 401 && !opts.anonymous && !opts.skipRefresh) {
    try {
      await refreshTokens(API_BASE);
      res = await raw(path, opts);
    } catch {
      clearTokens();
    }
  }

  // 429 retry. We only retry idempotent verbs to avoid double-submitting
  // mutations like message creates or session revocations.
  const method = (opts.method ?? "GET").toUpperCase();
  const retriable429 = method === "GET" || method === "HEAD";
  let attempt = 0;
  while (res.status === 429 && retriable429 && attempt < MAX_RETRIES_429) {
    const wait = parseRetryAfter(res) || 400 * Math.pow(2, attempt);
    await new Promise((r) => setTimeout(r, wait));
    res = await raw(path, opts);
    attempt += 1;
  }

  if (res.status === 204) return undefined as T;

  if (!res.ok) throw await parseError(res);

  const ct = res.headers.get("Content-Type") ?? "";
  if (ct.includes("application/json")) return (await res.json()) as T;
  return (await res.text()) as unknown as T;
}

// Convenience verbs.
export const http = {
  get: <T>(path: string, opts?: RequestOptions) => api<T>(path, { ...opts, method: "GET" }),
  post: <T>(path: string, body?: unknown, opts?: RequestOptions) =>
    api<T>(path, { ...opts, method: "POST", body }),
  put: <T>(path: string, body?: unknown, opts?: RequestOptions) =>
    api<T>(path, { ...opts, method: "PUT", body }),
  patch: <T>(path: string, body?: unknown, opts?: RequestOptions) =>
    api<T>(path, { ...opts, method: "PATCH", body }),
  del: <T>(path: string, opts?: RequestOptions) => api<T>(path, { ...opts, method: "DELETE" }),
};
