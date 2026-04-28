// Event type constants mirrored from internal/domain/event.
// Keep the union narrow: only types the UI actually reacts to.

export const WS = {
  MessageCreated: "message.created",
  MessageUpdated: "message.updated",
  MessageDeleted: "message.deleted",
  ReactionAdded: "reaction.added",
  ReactionRemoved: "reaction.removed",
  MessagePinned: "message.pinned",
  MessageUnpinned: "message.unpinned",
  TypingStarted: "typing.started",
  ChannelCreated: "channel.created",
  ChannelUpdated: "channel.updated",
  MemberJoined: "member.joined",
  MemberLeft: "member.left",
  PresenceChanged: "presence.changed",
  CallStarted: "call.started",
  CallEnded: "call.ended",
  CallParticipantJoined: "call.participant.joined",
  CallParticipantLeft: "call.participant.left",
  CallParticipantUpdated: "call.participant.updated",
  // Emitted by the SFU when it auto-downgrades/upgrades a peer based on
  // live RTCP metrics. The payload carries the new target quality and the
  // reasons[] so the UI can surface a badge like "Quality: Low (packet loss)".
  CallQualityAdapted: "call.quality.adapted",
  // WebRTC signaling fan-out. The SFU's Pion peer trickles ICE candidates
  // server → client over NATS → WS, addressed to a per-user "signal room"
  // (aloqa.signal.{userId}). The CallEngine subscribes to this room so it
  // can feed those candidates into RTCPeerConnection.addIceCandidate.
  SignalCandidate: "signal.candidate",
  SignalOffer: "signal.offer",
  SignalAnswer: "signal.answer",
} as const;

export type WSEventType = (typeof WS)[keyof typeof WS];

export interface ServerEvent<T = unknown> {
  type: string; // one of WSEventType + others
  payload: T;
  sequence?: number;
  subject?: string;
  workspace_id?: string;
  channel_id?: string;
  user_id?: string;
  timestamp?: string;
  version?: number;
  delivery_semantic?: "ephemeral" | "best_effort" | "at_least_once";
  replayable?: boolean;
}

// Client → server envelopes.
export type ClientMessage =
  | { type: "subscribe"; payload: { channel: string } }
  | { type: "unsubscribe"; payload: { channel: string } }
  | { type: "typing"; payload: { channel_id: string } };

// Room helpers.
export const rooms = {
  workspace: (wsId: string) => `aloqa.ws.${wsId}`,
  channel: (chId: string) => `aloqa.chat.${chId}`,
  typing: (chId: string) => `channel:${chId}`,
  // Per-user signaling fan-out. The backend authorizes only when the
  // requested userId matches the authenticated client.
  signal: (userId: string) => `aloqa.signal.${userId}`,
};

// SignalPayload mirrors internal/domain/event.SignalPayload — used for both
// WebRTC offer/answer and ICE candidate fan-out from the SFU.
export interface SignalPayload {
  call_id: string;
  from_user: string;
  to_user: string;
  sdp?: string;
  type?: string;
  candidate?: string;
  sdp_mid?: string;
  sdp_mline_index?: number | null;
}
