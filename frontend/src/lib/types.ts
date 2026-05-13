// Mirror of the Go backend's JSON shapes. Kept narrow: only the fields
// the UI actually reads. Add more as needed; don't `any`.

export type UUID = string;
export type ISODate = string;

export interface User {
  id: UUID;
  email: string;
  display_name: string;
  avatar_url?: string;
  status: "active" | "suspended" | "deactivated";
  locale: string;
  created_at: ISODate;
  updated_at: ISODate;
}

export interface TokenPair {
  access_token: string;
  refresh_token: string;
  session_id: string;
  expires_in: number; // seconds
}

// Mirrors `auth.Session` as emitted by GET /api/v1/auth/sessions. The
// backend strips `RefreshToken` via the `json:"-"` tag, so only the public
// metadata fields are available here. The UI uses this to render the
// Security panel (device list, last-active) and to drive per-session
// revoke actions.
export interface Session {
  id: UUID;
  user_id: UUID;
  device_info: string;
  ip_address: string;
  created_at: ISODate;
  expires_at: ISODate;
  last_active_at: ISODate;
}

export type WorkspaceKind = "personal" | "organization";

export interface Workspace {
  id: UUID;
  name: string;
  slug: string;
  kind?: WorkspaceKind;
  avatar_url?: string;
  created_by?: UUID;
  created_at: ISODate;
  updated_at: ISODate;
}

// Matches the backend enum in internal/domain/entity/channel.go. "dm" is
// a 1:1 direct message; "group_dm" is an ad-hoc multi-user conversation
// without a shared channel. The frontend treats them the same visually
// (UserIcon + truncated title) but renders different icons/list headings.
export type ChannelType = "public" | "private" | "dm" | "group_dm";

export interface Channel {
  id: UUID;
  workspace_id: UUID;
  name: string;
  topic?: string;
  type: ChannelType;
  created_by?: UUID;
  archived?: boolean;
  created_at: ISODate;
  updated_at: ISODate;
}

// Backend emits one Reaction row per (user, emoji). We aggregate on the
// client — see `aggregateReactions` below.
export interface Reaction {
  id: UUID;
  message_id: UUID;
  user_id: UUID;
  emoji: string;
  created_at: ISODate;
}

export interface AggregatedReaction {
  emoji: string;
  user_ids: UUID[];
  count: number;
}

export interface Attachment {
  id: UUID;
  message_id?: UUID;
  file_name: string;
  file_size: number;
  mime_type: string;
  url?: string;
  created_at: ISODate;
}

export type MessageType = "text" | "system" | "file";

export interface Message {
  id: UUID;
  channel_id: UUID;
  user_id: UUID;
  content: string;
  type?: MessageType;
  parent_id?: UUID | null;
  reply_count?: number;
  reactions?: Reaction[];
  attachments?: Attachment[];
  pinned?: boolean;
  pinned_by?: UUID | null;
  pinned_at?: ISODate | null;
  edited?: boolean;
  edited_at?: ISODate | null;
  deleted_at?: ISODate | null;
  created_at: ISODate;
  updated_at: ISODate;
  // Backend embeds the author inline on list/get responses.
  user?: User;
}

export interface Paginated<T> {
  items: T[];
  next_cursor?: string;
  has_more: boolean;
}

export type CallType = "one_to_one" | "group" | "meeting" | "webinar" | "selector";
// Matches the backend state machine in internal/domain/entity/call.go:
// a call is "ringing" until the first participant joins the SFU, at which
// point the service transitions it to "active", and finally "ended".
export type CallStatus = "ringing" | "active" | "ended";

// Mirror of internal/domain/entity/CallSettings — narrowed to the toggles
// the UI actually reads (recording gate, screen-share gate, etc.). The
// backend tolerates extra fields so this can stay incremental.
export interface CallSettings {
  recording?: boolean;
  screen_sharing?: boolean;
  chat?: boolean;
  waiting_room?: boolean;
  mute_on_join?: boolean;
  breakout_rooms?: boolean;
  e2ee?: boolean;
  watermark?: boolean;
  max_participants?: number;
}

export interface Call {
  id: UUID;
  workspace_id: UUID;
  channel_id?: UUID | null;
  type: CallType;
  title: string;
  status: CallStatus;
  started_by: UUID;
  started_at: ISODate;
  ended_at?: ISODate | null;
  settings?: CallSettings;
}

// ── Breakout rooms ──────────────────────────────────────────────────
// Breakouts are sub-rooms inside an active call. Backend gates: only
// host/co_host can create/close/broadcast; the call must be active and
// have settings.breakout_rooms enabled. Participants self-join via the
// /join endpoint (no host-driven assignment in the current API).
export type BreakoutRoomStatus = "active" | "closed";

export interface BreakoutRoom {
  id: UUID;
  call_id: UUID;
  name: string;
  created_by: UUID;
  time_limit?: number; // seconds
  status: BreakoutRoomStatus;
  created_at: ISODate;
  closed_at?: ISODate | null;
}

// ── Recordings ──────────────────────────────────────────────────────
// Mirrors internal/domain/entity/Recording. Status flow is:
//   recording → processing → ready (or → failed)
// We surface ready+downloadable artifacts in the UI; "processing" shows
// a non-actionable in-progress chip.
export type RecordingStatus = "recording" | "processing" | "ready" | "failed";
export type RecordingStrategy = "composite" | "per_track" | "both";
export type RecordingFormat = "webm" | "mp4" | "bundle";
export type RecordingArtifactKind =
  | "audio_track"
  | "video_track"
  | "screen_track"
  | "composite_playback"
  | "transcript_audio"
  | "manifest"
  | "ai_manifest"
  | "session_bundle";

export interface Recording {
  id: UUID;
  call_id: UUID;
  workspace_id: UUID;
  started_by: UUID;
  strategy: RecordingStrategy;
  format: RecordingFormat;
  status: RecordingStatus;
  duration?: number;
  file_size?: number;
  storage_path?: string;
  downloadable: boolean;
  started_at: ISODate;
  stopped_at?: ISODate | null;
  ready_at?: ISODate | null;
  expires_at?: ISODate | null;
  created_at: ISODate;
}

export interface RecordingArtifact {
  id: UUID;
  recording_id: UUID;
  workspace_id: UUID;
  kind: RecordingArtifactKind;
  source_user_id?: UUID;
  format: string;
  mime_type?: string;
  storage_path: string;
  file_size: number;
  duration?: number;
  downloadable: boolean;
  created_at: ISODate;
}

export interface CallParticipant {
  id: UUID;
  call_id: UUID;
  user_id: UUID;
  role: "host" | "co_host" | "presenter" | "viewer" | "waiting";
  audio_muted: boolean;
  video_muted: boolean;
  screen_sharing: boolean;
  joined_at: ISODate;
  left_at?: ISODate | null;
}

export interface Notification {
  id: UUID;
  workspace_id: UUID;
  user_id: UUID;
  type: string;
  title: string;
  body?: string;
  link?: string;
  read_at?: ISODate | null;
  created_at: ISODate;
}

export interface WorkspaceMember {
  id: UUID;
  workspace_id: UUID;
  user_id: UUID;
  role: "owner" | "admin" | "member" | "guest";
  user?: User;
  joined_at: ISODate;
}

export interface GuestInvite {
  id: UUID;
  workspace_id: UUID;
  token: string;
  email?: string;
  channel_ids?: UUID[];
  created_by: UUID;
  max_uses: number;
  use_count: number;
  expires_at: ISODate;
  revoked_at?: ISODate | null;
  created_at: ISODate;
}

export interface AuditEntry {
  id: UUID;
  workspace_id: UUID;
  actor_id?: UUID;
  action: string;
  target_type?: string;
  target_id?: string;
  metadata?: Record<string, unknown>;
  created_at: ISODate;
}

// Mirrors `search.Result` in the backend.
export interface SearchResult {
  id: UUID;
  type: "message" | "channel" | "user" | "file";
  workspace_id: UUID;
  channel_id?: UUID;
  title: string;
  snippet: string;
  score: number;
  created_at: ISODate;
  updated_at: ISODate;
  user?: User;
}

// Envelope returned by GET /workspaces/{w}/search.
export interface SearchResults {
  results: SearchResult[];
  total: number;
  next_offset?: number;
}

export type PresenceStatus = "online" | "away" | "dnd" | "offline";

// Mirrors `presence.UserPresence` in the backend.
export interface PresenceInfo {
  user_id: UUID;
  workspace_id?: UUID;
  status: PresenceStatus;
  custom_status?: string;
  custom_emoji?: string;
  last_seen_at?: ISODate;
}

export interface UnreadCount {
  channel_id: UUID;
  count: number;
  last_read_at?: ISODate;
}

// Backend error envelope. The server returns `{error:{code,message}}` — our
// HTTP client flattens to `{error, code, details}` for the UI layer.
export interface ApiErrorBody {
  error: string | { code?: string; message?: string };
  code?: string;
  details?: unknown;
}

/** Collapse the flat `Reaction[]` emitted by the backend into per-emoji groups
 *  suitable for badge rendering. Preserves first-seen order of emojis. */
export function aggregateReactions(
  raw: Reaction[] | undefined,
): AggregatedReaction[] {
  if (!raw || raw.length === 0) return [];
  const order: string[] = [];
  const by: Record<string, AggregatedReaction> = {};
  for (const r of raw) {
    if (!by[r.emoji]) {
      by[r.emoji] = { emoji: r.emoji, user_ids: [], count: 0 };
      order.push(r.emoji);
    }
    if (!by[r.emoji].user_ids.includes(r.user_id)) {
      by[r.emoji].user_ids.push(r.user_id);
      by[r.emoji].count += 1;
    }
  }
  return order.map((e) => by[e]);
}
