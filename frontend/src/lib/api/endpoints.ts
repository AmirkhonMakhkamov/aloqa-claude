// Typed wrappers around each backend endpoint used by the UI.
// Keep these small; business logic lives in stores/components.

import { http } from "./client";
import type {
  AuditEntry,
  BreakoutRoom,
  Call,
  CallParticipant,
  CallSettings,
  Channel,
  GuestInvite,
  Message,
  Notification,
  Paginated,
  PresenceInfo,
  PresenceStatus,
  Recording,
  RecordingArtifact,
  RecordingStrategy,
  SearchResults,
  Session,
  TokenPair,
  UnreadCount,
  User,
  UUID,
  Workspace,
  WorkspaceMember,
} from "../types";

// ── Auth ──────────────────────────────────────────────────────────────
// Sessions endpoint returns a bare array of `auth.Session`; Go marshals
// an empty slice as `null`, so callers should coerce `?? []`.
export const authApi = {
  register: (email: string, password: string, display_name: string) =>
    http.post<User>("/api/v1/auth/register", { email, password, display_name }, { anonymous: true }),
  login: (email: string, password: string, device_info?: string) =>
    http.post<TokenPair>("/api/v1/auth/login", { email, password, device_info }, { anonymous: true }),
  // Logout the current session. Pass `session_id` to revoke a *different*
  // session (e.g. from the Security panel) — the backend's Logout handler
  // honours the explicit id when provided.
  logout: (session_id?: string) =>
    http.post<void>("/api/v1/auth/logout", session_id ? { session_id } : undefined),
  logoutAll: () => http.post<void>("/api/v1/auth/logout-all"),
  sessions: () => http.get<Session[] | null>("/api/v1/auth/sessions"),
};

// ── Account ───────────────────────────────────────────────────────────
export const accountApi = {
  me: () => http.get<User>("/api/v1/users/me"),
  updateMe: (patch: Partial<Pick<User, "display_name" | "avatar_url" | "locale">>) =>
    http.patch<User>("/api/v1/users/me", patch),
};

// ── Workspaces ────────────────────────────────────────────────────────
export const workspacesApi = {
  list: () => http.get<Workspace[]>("/api/v1/workspaces"),
  get: (id: UUID) => http.get<Workspace>(`/api/v1/workspaces/${id}`),
  create: (name: string, slug: string, avatar_url?: string) =>
    http.post<Workspace>("/api/v1/workspaces", { name, slug, avatar_url }),
  personal: () => http.get<Workspace>("/api/v1/personal"),
};

// ── Channels ──────────────────────────────────────────────────────────
export const channelsApi = {
  list: (wsId: UUID) => http.get<Channel[]>(`/api/v1/workspaces/${wsId}/channels`),
  get: (wsId: UUID, chId: UUID) =>
    http.get<Channel>(`/api/v1/workspaces/${wsId}/channels/${chId}`),
  create: (wsId: UUID, input: { name: string; topic?: string; type: Channel["type"] }) =>
    http.post<Channel>(`/api/v1/workspaces/${wsId}/channels`, input),
  update: (wsId: UUID, chId: UUID, input: { name?: string; topic?: string }) =>
    http.put<Channel>(`/api/v1/workspaces/${wsId}/channels/${chId}`, input),
  join: (wsId: UUID, chId: UUID) =>
    http.post<void>(`/api/v1/workspaces/${wsId}/channels/${chId}/join`),
  leave: (wsId: UUID, chId: UUID) =>
    http.post<void>(`/api/v1/workspaces/${wsId}/channels/${chId}/leave`),
  markRead: (wsId: UUID, chId: UUID) =>
    http.post<void>(`/api/v1/workspaces/${wsId}/channels/${chId}/read`),
  unread: (wsId: UUID) =>
    http.get<UnreadCount[]>(`/api/v1/workspaces/${wsId}/channels/unread`),
  dm: (wsId: UUID, userId: UUID) =>
    http.post<Channel>(`/api/v1/workspaces/${wsId}/channels/dm`, { user_id: userId }),
};

// ── Messages ──────────────────────────────────────────────────────────
export const messagesApi = {
  list: (wsId: UUID, chId: UUID, cursor?: string, limit = 50) =>
    http.get<Paginated<Message>>(
      `/api/v1/workspaces/${wsId}/channels/${chId}/messages`,
      { query: { cursor, limit } },
    ),
  send: (wsId: UUID, chId: UUID, content: string, parent_id?: UUID) =>
    http.post<Message>(`/api/v1/workspaces/${wsId}/channels/${chId}/messages`, {
      content,
      parent_id,
    }),
  edit: (wsId: UUID, chId: UUID, msgId: UUID, content: string) =>
    http.put<Message>(`/api/v1/workspaces/${wsId}/channels/${chId}/messages/${msgId}`, {
      content,
    }),
  delete: (wsId: UUID, chId: UUID, msgId: UUID) =>
    http.del<void>(`/api/v1/workspaces/${wsId}/channels/${chId}/messages/${msgId}`),
  thread: (wsId: UUID, chId: UUID, msgId: UUID, cursor?: string, limit = 50) =>
    http.get<Paginated<Message>>(
      `/api/v1/workspaces/${wsId}/channels/${chId}/messages/${msgId}/thread`,
      { query: { cursor, limit } },
    ),
  react: (wsId: UUID, chId: UUID, msgId: UUID, emoji: string) =>
    http.post<void>(
      `/api/v1/workspaces/${wsId}/channels/${chId}/messages/${msgId}/reactions`,
      { emoji },
    ),
  unreact: (wsId: UUID, chId: UUID, msgId: UUID, emoji: string) =>
    http.del<void>(
      `/api/v1/workspaces/${wsId}/channels/${chId}/messages/${msgId}/reactions/${encodeURIComponent(emoji)}`,
    ),
  pin: (wsId: UUID, chId: UUID, msgId: UUID) =>
    http.post<void>(`/api/v1/workspaces/${wsId}/channels/${chId}/messages/${msgId}/pin`),
  unpin: (wsId: UUID, chId: UUID, msgId: UUID) =>
    http.del<void>(`/api/v1/workspaces/${wsId}/channels/${chId}/messages/${msgId}/pin`),
};

// ── Files ─────────────────────────────────────────────────────────────
export const filesApi = {
  upload: async (wsId: UUID, chId: UUID, msgId: UUID, file: File) => {
    const fd = new FormData();
    fd.set("file", file);
    return http.post<{ id: UUID; storage_key: string; filename: string }>(
      `/api/v1/workspaces/${wsId}/channels/${chId}/messages/${msgId}/attachments`,
      fd,
    );
  },
};

// ── Calls ─────────────────────────────────────────────────────────────
export const callsApi = {
  list: (wsId: UUID) => http.get<Call[]>(`/api/v1/workspaces/${wsId}/calls`),
  get: (wsId: UUID, callId: UUID) =>
    http.get<Call>(`/api/v1/workspaces/${wsId}/calls/${callId}`),
  start: (
    wsId: UUID,
    input: {
      type: Call["type"];
      title: string;
      channel_id?: UUID | null;
      // CallSettings is optional — when omitted the server keeps every
      // gate off (recording, screen-sharing, etc.). For a meeting created
      // through the UI we typically opt in to recording + screen-sharing
      // so the host has those buttons available without a follow-up
      // settings round-trip.
      settings?: CallSettings;
    },
  ) =>
    http.post<Call>(`/api/v1/workspaces/${wsId}/calls`, {
      type: input.type,
      title: input.title,
      channel_id: input.channel_id,
      settings: input.settings ?? {},
    }),
  join: (wsId: UUID, callId: UUID) =>
    http.post<CallParticipant>(`/api/v1/workspaces/${wsId}/calls/${callId}/join`),
  leave: (wsId: UUID, callId: UUID) =>
    http.post<void>(`/api/v1/workspaces/${wsId}/calls/${callId}/leave`),
  end: (wsId: UUID, callId: UUID) =>
    http.post<void>(`/api/v1/workspaces/${wsId}/calls/${callId}/end`),
  participants: (wsId: UUID, callId: UUID) =>
    http.get<CallParticipant[]>(`/api/v1/workspaces/${wsId}/calls/${callId}/participants`),
  updateMedia: (
    wsId: UUID,
    callId: UUID,
    input: { audio_muted?: boolean; video_muted?: boolean; screen_sharing?: boolean },
  ) => http.put<void>(`/api/v1/workspaces/${wsId}/calls/${callId}/media`, input),

  // ── SFU signaling ─────────────────────────────────────────────────
  // The client issues these three endpoints in sequence:
  //   1. mediaToken   → exchange the workspace JWT for a short-lived
  //      media-plane token that binds us to a specific SFU node/region.
  //   2. mediaOffer   → POST our RTCPeerConnection offer SDP and receive
  //      the SFU's answer SDP back.
  //   3. mediaIceCandidate → trickle our ICE candidates to the SFU as
  //      they're generated by the PeerConnection.
  // The token response also carries the routing/fanout policy so we can
  // pick the right ICE strategy (STUN-only vs TURN-always).
  mediaToken: (wsId: UUID, callId: UUID) =>
    http.post<MediaSessionToken>(
      `/api/v1/workspaces/${wsId}/calls/${callId}/media-session/token`,
    ),
  mediaOffer: (wsId: UUID, callId: UUID, input: { token: string; sdp: string }) =>
    http.post<{ sdp: string; type: "answer" }>(
      `/api/v1/workspaces/${wsId}/calls/${callId}/media-session/offer`,
      { token: input.token, sdp: input.sdp, type: "offer" },
    ),
  mediaIceCandidate: (
    wsId: UUID,
    callId: UUID,
    input: {
      token: string;
      candidate: string;
      sdp_mid?: string | null;
      sdp_mline_index?: number | null;
    },
  ) =>
    http.post<void>(
      `/api/v1/workspaces/${wsId}/calls/${callId}/media-session/ice-candidate`,
      input,
    ),
};

// ── Recordings ────────────────────────────────────────────────────────
// Each recording belongs to a single call. Lifecycle:
//   POST /recordings        → create (host/co_host only, status=recording)
//   POST /recordings/{id}/stop → stop (status flips to processing)
//   GET  /recordings        → list every recording for the call
//   GET  /recordings/{id}/artifacts → per-recording artifacts (audio/video/composite)
//   GET  /recordings/{id}/artifacts/{aid}/download → byte stream
// `downloadArtifactUrl` returns the URL string rather than fetching, so
// the UI can hand it to <a download> / window.open without re-streaming
// through fetch (which would buffer the whole file in memory).
// ── Breakout rooms ────────────────────────────────────────────────────
// Each call may host any number of breakout rooms. Lifecycle:
//   POST /breakout-rooms              → host creates one or more rooms
//   POST /breakout-rooms/{id}/join    → participant self-joins a room
//   POST /breakout-rooms/return       → return current participant to main
//   POST /breakout-rooms/{id}/close   → host closes one room
//   POST /breakout-rooms/close-all    → host closes every room
//   POST /breakout-rooms/broadcast    → host broadcasts a message to all
// Notable absence: no "assign user X to room Y" endpoint — assignment is
// participant-driven (everyone picks their own room from the list). The
// host can guide via the broadcast message.
export const breakoutsApi = {
  list: (wsId: UUID, callId: UUID) =>
    http.get<BreakoutRoom[] | null>(
      `/api/v1/workspaces/${wsId}/calls/${callId}/breakout-rooms`,
    ),
  create: (
    wsId: UUID,
    callId: UUID,
    rooms: Array<{ name: string; time_limit?: number }>,
  ) =>
    http.post<BreakoutRoom[]>(
      `/api/v1/workspaces/${wsId}/calls/${callId}/breakout-rooms`,
      { rooms },
    ),
  join: (wsId: UUID, callId: UUID, roomId: UUID) =>
    http.post<void>(
      `/api/v1/workspaces/${wsId}/calls/${callId}/breakout-rooms/${roomId}/join`,
    ),
  return: (wsId: UUID, callId: UUID) =>
    http.post<void>(
      `/api/v1/workspaces/${wsId}/calls/${callId}/breakout-rooms/return`,
    ),
  close: (wsId: UUID, callId: UUID, roomId: UUID) =>
    http.post<void>(
      `/api/v1/workspaces/${wsId}/calls/${callId}/breakout-rooms/${roomId}/close`,
    ),
  closeAll: (wsId: UUID, callId: UUID) =>
    http.post<void>(
      `/api/v1/workspaces/${wsId}/calls/${callId}/breakout-rooms/close-all`,
    ),
  broadcast: (wsId: UUID, callId: UUID, message: string) =>
    http.post<void>(
      `/api/v1/workspaces/${wsId}/calls/${callId}/breakout-rooms/broadcast`,
      { message },
    ),
  participants: (wsId: UUID, callId: UUID, roomId: UUID) =>
    http.get<CallParticipant[] | null>(
      `/api/v1/workspaces/${wsId}/calls/${callId}/breakout-rooms/${roomId}/participants`,
    ),
};

export const recordingsApi = {
  start: (wsId: UUID, callId: UUID, strategy?: RecordingStrategy) =>
    http.post<Recording>(
      `/api/v1/workspaces/${wsId}/calls/${callId}/recordings`,
      strategy ? { strategy } : {},
    ),
  stop: (wsId: UUID, callId: UUID, recordingId: UUID) =>
    http.post<Recording>(
      `/api/v1/workspaces/${wsId}/calls/${callId}/recordings/${recordingId}/stop`,
    ),
  listByCall: (wsId: UUID, callId: UUID) =>
    http.get<Recording[] | null>(
      `/api/v1/workspaces/${wsId}/calls/${callId}/recordings`,
    ),
  get: (wsId: UUID, callId: UUID, recordingId: UUID) =>
    http.get<Recording>(
      `/api/v1/workspaces/${wsId}/calls/${callId}/recordings/${recordingId}`,
    ),
  listArtifacts: (wsId: UUID, callId: UUID, recordingId: UUID) =>
    http.get<RecordingArtifact[] | null>(
      `/api/v1/workspaces/${wsId}/calls/${callId}/recordings/${recordingId}/artifacts`,
    ),
  // For artifact downloads, return the URL — the UI shows a real <a>
  // anchor with `download`, which lets the browser stream the bytes
  // straight to disk without going through our fetch wrapper. We do
  // include the access token in the query string would be visible in
  // logs; instead the existing /files/* download path uses the bearer
  // header on a fetch that pipes to a Blob, which is what we'll do here.
  downloadArtifactUrl: (
    wsId: UUID,
    callId: UUID,
    recordingId: UUID,
    artifactId: UUID,
  ) =>
    `/api/v1/workspaces/${wsId}/calls/${callId}/recordings/${recordingId}/artifacts/${artifactId}/download`,
};

// Media-plane token envelope emitted by POST /calls/{id}/media-session/token.
// `control_url` / `media_url` are currently the same API host but the backend
// may eventually split them (WS control, HTTPS media); we preserve both so a
// future split doesn't require a client change.
export interface MediaSessionToken {
  token: string;
  role: "presenter" | "viewer";
  expires_at: string;
  node_id: string;
  region?: string;
  control_url?: string;
  media_url?: string;
  routing_mode?: string;
  fanout_strategy?: string;
  overflow_policy?: string;
  screen_share_priority?: string;
  turn_strategy?: "stun_only" | "stun_first" | "turn_first" | "turn_only";
}

// ── Presence ──────────────────────────────────────────────────────────
export const presenceApi = {
  set: (wsId: UUID, status: PresenceStatus, custom_status?: string, custom_emoji?: string) =>
    http.put<void>(`/api/v1/workspaces/${wsId}/presence`, {
      status,
      custom_status,
      custom_emoji,
    }),
  // Backend returns a bare array of `UserPresence`; may be `null` when empty.
  list: (wsId: UUID) =>
    http.get<PresenceInfo[] | null>(`/api/v1/workspaces/${wsId}/presence`),
};

// ── Notifications ─────────────────────────────────────────────────────
export const notificationsApi = {
  list: (wsId: UUID, limit = 50) =>
    http.get<Notification[]>(`/api/v1/workspaces/${wsId}/notifications`, {
      query: { limit },
    }),
  readAll: (wsId: UUID) =>
    http.post<void>(`/api/v1/workspaces/${wsId}/notifications/read-all`),
  unreadCount: (wsId: UUID) =>
    http.get<{ unread_count: number }>(
      `/api/v1/workspaces/${wsId}/notifications/unread-count`,
    ),
  markRead: (wsId: UUID, notifId: UUID) =>
    http.post<void>(`/api/v1/workspaces/${wsId}/notifications/${notifId}/read`),
};

// ── Search ────────────────────────────────────────────────────────────
export const searchApi = {
  // Backend returns `{results, total, next_offset?}` — always an object even
  // when the result set is empty.
  query: (
    wsId: UUID,
    q: string,
    opts: {
      type?: string;
      limit?: number;
      offset?: number;
      channel_id?: UUID;
      date_from?: string;
      date_to?: string;
    } = {},
  ) =>
    http.get<SearchResults>(`/api/v1/workspaces/${wsId}/search`, {
      query: { q, ...opts },
    }),
};

// ── Admin ─────────────────────────────────────────────────────────────
export const adminApi = {
  // Backend returns a bare array of `WorkspaceMember` (no envelope). The `user`
  // field is populated inline when the roster join succeeds.
  members: (wsId: UUID, limit = 50, offset = 0) =>
    http.get<WorkspaceMember[]>(`/api/v1/workspaces/${wsId}/admin/members`, {
      query: { limit, offset },
    }),
  updateMemberRole: (wsId: UUID, userId: UUID, role: WorkspaceMember["role"]) =>
    http.put<void>(`/api/v1/workspaces/${wsId}/admin/members/${userId}/role`, { role }),
  removeMember: (wsId: UUID, userId: UUID) =>
    http.del<void>(`/api/v1/workspaces/${wsId}/admin/members/${userId}`),
  suspend: (wsId: UUID, userId: UUID) =>
    http.post<void>(`/api/v1/workspaces/${wsId}/admin/members/${userId}/suspend`),
  reactivate: (wsId: UUID, userId: UUID) =>
    http.post<void>(`/api/v1/workspaces/${wsId}/admin/members/${userId}/reactivate`),
  auditLog: (wsId: UUID, limit = 50, offset = 0) =>
    http.get<{ entries: AuditEntry[]; total: number }>(
      `/api/v1/workspaces/${wsId}/admin/audit-log`,
      { query: { limit, offset } },
    ),
};

// ── Guests / Invites ──────────────────────────────────────────────────
export const invitesApi = {
  list: (wsId: UUID) => http.get<GuestInvite[]>(`/api/v1/workspaces/${wsId}/invites`),
  create: (
    wsId: UUID,
    input: { email?: string; channel_ids?: UUID[]; max_uses?: number; ttl_hours?: number },
  ) => http.post<GuestInvite>(`/api/v1/workspaces/${wsId}/invites`, input),
  revoke: (wsId: UUID, inviteId: UUID) =>
    http.del<void>(`/api/v1/workspaces/${wsId}/invites/${inviteId}`),
  redeem: (token: string, email?: string, display_name?: string) =>
    http.post<{ user: User; tokens: TokenPair }>(
      `/api/v1/invites/${token}/redeem`,
      { email, display_name },
      { anonymous: true },
    ),
};
