// RealtimeClient — one persistent WebSocket per session, with auto-reconnect,
// backoff, resume-on-reconnect, and pub/sub fanout to React components.
//
// Why not a library (socket.io, etc.)? The backend speaks a custom JSON protocol
// on raw WebSocket — thin custom client is ~100 lines and keeps the contract
// observable in one file.

import { loadTokens, refreshTokens } from "../auth";
import type { ClientMessage, ServerEvent } from "./events";

const WS_URL =
  process.env.NEXT_PUBLIC_WS_URL ??
  (typeof window !== "undefined"
    ? `${window.location.protocol === "https:" ? "wss" : "ws"}://${window.location.host}/ws`
    : "ws://localhost:8080/ws");

const API_BASE = process.env.NEXT_PUBLIC_API_BASE ?? "";

type Listener = (evt: ServerEvent) => void;

export class RealtimeClient {
  private ws: WebSocket | null = null;
  private listeners = new Set<Listener>();
  private subscriptions = new Set<string>();
  private resumeKey: string | null = null;
  private reconnectAttempt = 0;
  private reconnectTimer: number | null = null;
  private shouldRun = false;
  // Last delivered sequence per room; used for resume bookkeeping on the client.
  private lastSeq = new Map<string, number>();

  start(): void {
    if (this.shouldRun) return;
    this.shouldRun = true;
    this.connect();
  }

  stop(): void {
    this.shouldRun = false;
    if (this.reconnectTimer) {
      window.clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    this.ws?.close(1000, "client stop");
    this.ws = null;
  }

  on(fn: Listener): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  subscribe(channel: string): void {
    if (this.subscriptions.has(channel)) return;
    this.subscriptions.add(channel);
    this.send({ type: "subscribe", payload: { channel } });
  }

  unsubscribe(channel: string): void {
    if (!this.subscriptions.has(channel)) return;
    this.subscriptions.delete(channel);
    this.send({ type: "unsubscribe", payload: { channel } });
  }

  typing(channelId: string): void {
    this.send({ type: "typing", payload: { channel_id: channelId } });
  }

  isOpen(): boolean {
    return this.ws?.readyState === WebSocket.OPEN;
  }

  private send(msg: ClientMessage): void {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(msg));
    }
  }

  private async connect(): Promise<void> {
    const tokens = loadTokens();
    if (!tokens) return;

    // Use session id as resume key the first time; keep it stable across reconnects.
    if (!this.resumeKey) this.resumeKey = tokens.sessionId;

    let access = tokens.accessToken;
    // Proactively refresh an expired token before opening to avoid instant 401.
    if (tokens.expiresAt <= Date.now()) {
      try {
        const fresh = await refreshTokens(API_BASE);
        access = fresh.accessToken;
      } catch {
        return; // will retry via reconnect backoff below
      }
    }

    const url = `${WS_URL}?resume_key=${encodeURIComponent(this.resumeKey)}&token=${encodeURIComponent(access)}`;
    const ws = new WebSocket(url);
    this.ws = ws;

    ws.onopen = () => {
      this.reconnectAttempt = 0;
      // Re-establish all prior subscriptions (server also restores from state store;
      // this is a belt-and-braces so ephemeral rooms come back too).
      for (const sub of this.subscriptions) {
        this.send({ type: "subscribe", payload: { channel: sub } });
      }
    };

    ws.onmessage = (msg) => {
      let evt: ServerEvent;
      try {
        evt = JSON.parse(msg.data) as ServerEvent;
      } catch {
        return;
      }
      // Track last delivered sequence per subject, best effort.
      if (typeof evt.sequence === "number" && evt.subject) {
        const prev = this.lastSeq.get(evt.subject) ?? -1;
        if (evt.sequence > prev) this.lastSeq.set(evt.subject, evt.sequence);
      }
      for (const l of this.listeners) l(evt);
    };

    ws.onclose = () => {
      this.ws = null;
      if (!this.shouldRun) return;
      // Exponential backoff with jitter, capped at 30s.
      const delay = Math.min(30_000, 500 * 2 ** this.reconnectAttempt);
      const jitter = Math.random() * 500;
      this.reconnectAttempt += 1;
      this.reconnectTimer = window.setTimeout(() => this.connect(), delay + jitter);
    };

    ws.onerror = () => {
      // close() will be called after error; nothing else to do.
    };
  }
}

// Singleton for the app. Pages call `rt.start()` once and subscribe as needed.
let singleton: RealtimeClient | null = null;

export function rt(): RealtimeClient {
  if (!singleton) singleton = new RealtimeClient();
  return singleton;
}
