// CallEngine — the WebRTC client for a single call session.
//
// Responsibility split:
//   - The HTTP control plane (callsApi.join/leave/end/updateMedia) is handled
//     by the detail page. That's what tells the *server* who's in the room.
//   - This engine owns the *media plane*: getUserMedia, the RTCPeerConnection
//     to the SFU, the token/offer/ice-candidate signaling dance, and the
//     plumbing that turns incoming tracks into MediaStreams the UI can render.
//
// Architecture reminder: the backend runs a Pion-based SFU (not mesh/MCU).
// Every peer talks to the SFU once; the SFU fans out. So our PeerConnection
// has bidirectional transceivers — we push our mic/cam up, and the SFU
// pushes every other participant's tracks down. We tell participants apart
// by the MediaStream.id (StreamID), which the SFU labels with the remote
// participant_id. See internal/media/sfu/room.go.
//
// Signaling sequence on join():
//   1. POST /media-session/token          — get per-call auth + routing hints
//   2. getUserMedia({ audio, video })     — degrade gracefully if denied
//   3. new RTCPeerConnection(iceServers)  — STUN + any TURN the token implies
//   4. addTrack for every local track     — sets up send transceivers
//   5. createOffer + setLocalDescription
//   6. POST /media-session/offer          — server returns answer SDP
//   7. setRemoteDescription(answer)
//   8. onicecandidate   → POST /media-session/ice-candidate (trickle up)
//   9. ontrack          → emit "remote-track" with stream + participantId
//
// Design choice: ICE candidates from the server arrive over the WebSocket
// signaling channel addressed to `aloqa.signal.{userId}` as `signal.candidate`
// events (see internal/service/call/media.go publishICECandidate). Pion
// answers with a `c=IN IP4 0.0.0.0` SDP and trickles the real candidates
// asynchronously, so without subscribing to that room the browser ICE agent
// never learns where to send packets and the connection sits in "checking"
// forever. We bridge the WS event into RTCPeerConnection.addIceCandidate
// here, buffering on both sides so neither end-of-the-race wins matters.

import { callsApi, type MediaSessionToken } from "@/lib/api/endpoints";
import type { UUID } from "@/lib/types";
import { rt, type RealtimeClient } from "@/lib/ws/client";
import { rooms, WS, type ServerEvent, type SignalPayload } from "@/lib/ws/events";

// Default STUN. The SFU token response may declare a turn_strategy that
// forces us to rely on a TURN server; when that wiring lands we'll merge
// the server-supplied credentials into this list.
const DEFAULT_ICE: RTCIceServer[] = [
  { urls: ["stun:stun.l.google.com:19302"] },
];

export type RemoteTrackKind = "audio" | "video";

export interface RemoteTrack {
  // The SFU labels each forwarded stream with the remote participant's id,
  // so this maps cleanly to `CallParticipant.id`.
  participantId: string;
  stream: MediaStream;
  kind: RemoteTrackKind;
}

export type EngineConnectionState =
  | "idle"
  | "acquiring-media"
  | "signaling"
  | "connected"
  | "reconnecting"
  | "closed"
  | "failed";

export interface EngineErrorPayload {
  stage: "token" | "media" | "signaling" | "ice" | "unknown";
  message: string;
  cause?: unknown;
}

export interface EngineEventMap {
  "local-stream": MediaStream | null;
  "remote-track": RemoteTrack;
  "remote-track-removed": { participantId: string; streamId: string };
  "connection-state": EngineConnectionState;
  error: EngineErrorPayload;
}

type Listener<K extends keyof EngineEventMap> = (
  payload: EngineEventMap[K],
) => void;

export interface JoinOptions {
  audio: boolean;
  video: boolean;
}

export interface MediaControls {
  audio?: boolean;
  video?: boolean;
  screen?: boolean;
}

export class CallEngine {
  private readonly wsId: UUID;
  private readonly callId: UUID;
  private readonly userId: UUID;
  private readonly signalRoom: string;

  private pc: RTCPeerConnection | null = null;
  private localStream: MediaStream | null = null;
  private screenStream: MediaStream | null = null;
  private token: MediaSessionToken | null = null;
  private state: EngineConnectionState = "idle";
  private disposed = false;
  // ICE candidates produced before we POST the offer would 404 against
  // the SFU (no session yet). We buffer them until offer/answer completes
  // and then drain in order.
  private pendingIce: RTCIceCandidate[] = [];
  private offerCompleted = false;
  // Inbound candidates from the SFU can arrive over WS *before* we finish
  // setRemoteDescription(answer) — addIceCandidate would throw "remote
  // description is null". Mirror the buffering pattern on the receive side.
  private remoteIceBuffer: RTCIceCandidateInit[] = [];
  private remoteDescriptionSet = false;

  // WebSocket subscription handles. We subscribe lazily in join() and
  // tear down in leave() so the room only stays open while we're in a
  // live call.
  private wsClient: RealtimeClient | null = null;
  private wsUnsub: (() => void) | null = null;

  // Stable senders. We allocate one sendrecv audio + one sendrecv video
  // transceiver at join time and never add or remove tracks after that.
  // All toggles (mic mute, camera on/off, screen-share start/stop) are
  // implemented as `sender.replaceTrack(...)` against these senders. This
  // is what avoids renegotiation entirely — the SFU side sees the same
  // m-lines for the lifetime of the call. The previous "renegotiate after
  // addTrack" approach tripped Pion's HandleMediaOffer with "invalid
  // remote offer" because it recreates the peer per offer and the
  // re-offer SDP didn't survive that round-trip.
  private audioSender: RTCRtpSender | null = null;
  private videoSender: RTCRtpSender | null = null;

  // Track which remote streams we've surfaced so we can emit removal once
  // the SFU terminates them (ontrack's "onended" is unreliable cross-browser
  // — we also watch the PeerConnection's track events).
  private seenStreams = new Map<string, RemoteTrack>();

  // Stored as the widest shape TS will let us put every kind of listener
  // into one map; the `on`/`emit` boundaries keep the public API type-safe.
  private listeners = new Map<keyof EngineEventMap, Set<(payload: unknown) => void>>();

  constructor(wsId: UUID, callId: UUID, userId: UUID) {
    this.wsId = wsId;
    this.callId = callId;
    this.userId = userId;
    this.signalRoom = rooms.signal(userId);
  }

  // ── Event subscription ───────────────────────────────────────────
  on<K extends keyof EngineEventMap>(
    type: K,
    listener: Listener<K>,
  ): () => void {
    let set = this.listeners.get(type);
    if (!set) {
      set = new Set();
      this.listeners.set(type, set);
    }
    set.add(listener as (payload: unknown) => void);
    return () => {
      this.listeners.get(type)?.delete(listener as (payload: unknown) => void);
    };
  }

  private emit<K extends keyof EngineEventMap>(
    type: K,
    payload: EngineEventMap[K],
  ): void {
    const set = this.listeners.get(type);
    if (!set) return;
    for (const l of set) {
      try {
        (l as Listener<K>)(payload);
      } catch (err) {
        // Listener errors must not take down the engine.
        console.error("[CallEngine] listener threw", err);
      }
    }
  }

  private setState(next: EngineConnectionState): void {
    if (this.state === next) return;
    this.state = next;
    this.emit("connection-state", next);
  }

  // ── Public lifecycle ─────────────────────────────────────────────
  async join(opts: JoinOptions): Promise<void> {
    if (this.disposed) return;
    if (this.pc) return; // already joined; no-op

    // 0. Capability check. Safari Lockdown Mode strips RTCPeerConnection
    // (and related types) from the global scope; without this guard the
    // first `new RTCPeerConnection(...)` throws a cryptic
    // "Can't find variable: RTCPeerConnection" that bubbles up as a raw
    // ReferenceError. Bail early with an actionable message instead.
    if (!isWebRTCSupported()) {
      const message =
        "This browser doesn't support WebRTC calls. " +
        (isLikelySafariLockdown()
          ? "Safari Lockdown Mode is enabled — disable it for this site (Settings → Privacy & Security → Lockdown Mode → Configure for Safari) and reload."
          : "Try a recent version of Chrome, Edge, Firefox, or Safari.");
      this.emitError("unknown", new Error(message));
      this.setState("failed");
      throw new Error(message);
    }

    try {
      // 1. Acquire a media-session token first. If this fails, we never
      // touched the mic/cam — clean UX for permission-less failures.
      const token = await callsApi.mediaToken(this.wsId, this.callId);
      if (this.disposed) return;
      this.token = token;

      // 2. Media acquisition. If video/audio is requested but denied, we
      // downgrade gracefully: a viewer can still sit in audio-only, and
      // an audio-denied peer can still share video. The participant row
      // in the HTTP control plane will reflect the real state because the
      // page calls updateMedia() based on `getTracks()`.
      this.setState("acquiring-media");
      const local = await this.acquireLocalMedia(opts);
      this.localStream = local;
      this.emit("local-stream", local);

      // 3. Peer connection. The token *may* carry turn_strategy in the
      // future; for now we use STUN-only.
      this.setState("signaling");
      const iceServers = this.pickIceServers(token);
      const pc = new RTCPeerConnection({ iceServers });
      this.pc = pc;

      // Subscribe to the WS signal room *before* we send the offer. The SFU
      // starts trickling ICE the instant SetLocalDescription runs server-side,
      // and those events can arrive over NATS → WS faster than the HTTP
      // /media-session/offer response — if we subscribe after the response,
      // we miss the early candidates and ICE never converges.
      this.wsClient = rt();
      this.wsClient.start();
      this.wsClient.subscribe(this.signalRoom);
      this.wsUnsub = this.wsClient.on((evt) => this.onSignalEvent(evt));

      pc.ontrack = (ev) => this.onRemoteTrack(ev);
      pc.onicecandidate = (ev) => this.onLocalIceCandidate(ev);
      pc.oniceconnectionstatechange = () =>
        this.onIceStateChange(pc.iceConnectionState);
      pc.onconnectionstatechange = () =>
        this.onPeerStateChange(pc.connectionState);

      // Allocate one sendrecv transceiver per kind, with the current local
      // track (or null) attached. Caching the senders here lets every
      // future toggle (mic, camera, screen) use replaceTrack against the
      // same m-line — no renegotiation, no SDP changes, no SFU reset.
      const audioTrack = this.localStream?.getAudioTracks()[0] ?? null;
      const videoTrack = this.localStream?.getVideoTracks()[0] ?? null;
      const audioTransceiver = pc.addTransceiver(audioTrack ?? "audio", {
        direction: "sendrecv",
        streams: this.localStream ? [this.localStream] : [],
      });
      const videoTransceiver = pc.addTransceiver(videoTrack ?? "video", {
        direction: "sendrecv",
        streams: this.localStream ? [this.localStream] : [],
      });
      this.audioSender = audioTransceiver.sender;
      this.videoSender = videoTransceiver.sender;

      // 4. Offer/answer dance.
      const offer = await pc.createOffer({
        offerToReceiveAudio: true,
        offerToReceiveVideo: true,
      });
      await pc.setLocalDescription(offer);

      // Wait for at least one ICE candidate gathering tick so the offer
      // carries useful local candidates; trickle picks up the rest.
      await this.waitForInitialIce(pc, 250);
      const localSdp = pc.localDescription?.sdp ?? offer.sdp ?? "";

      const answer = await callsApi.mediaOffer(this.wsId, this.callId, {
        token: token.token,
        sdp: localSdp,
      });
      if (this.disposed) return;
      await pc.setRemoteDescription({ type: "answer", sdp: answer.sdp });
      this.remoteDescriptionSet = true;

      // Now the SFU has a session; flush any ICE candidates we buffered
      // while the offer was in flight, then let onicecandidate stream
      // late candidates straight through.
      this.offerCompleted = true;
      const drainLocal = this.pendingIce.splice(0);
      for (const c of drainLocal) this.sendIceCandidate(c);

      // Drain any inbound candidates that beat the answer to our doorstep.
      // Re-add via Promise.all to surface failures without blocking the join.
      const drainRemote = this.remoteIceBuffer.splice(0);
      for (const c of drainRemote) {
        void pc
          .addIceCandidate(c)
          .catch((err) => this.emitError("ice", err));
      }

      // At this point connection-state will flip to "connected" via the
      // state change listener once ICE completes.
    } catch (err) {
      this.emitError("signaling", err);
      this.setState("failed");
      await this.teardown();
      throw err;
    }
  }

  async leave(): Promise<void> {
    await this.teardown();
    this.setState("closed");
  }

  // Toggle mic/cam/screen. Returns the updated state so the caller can
  // sync the HTTP control plane (PUT /calls/{id}/media).
  //
  // All three controls operate on the stable senders allocated in join().
  // Each toggle calls `sender.replaceTrack(...)` — which never changes the
  // SDP and never requires renegotiation. Mic mute/unmute swaps a real
  // audio track for null on the audio sender. Camera off/on swaps the
  // camera track for null on the video sender (and stops the device so
  // the LED goes off). Screen share start/stop swaps the camera track for
  // a getDisplayMedia track on the same video sender, then back. The SFU
  // sees a single offer for the lifetime of the call.
  async setMedia(
    controls: MediaControls,
  ): Promise<{ audio: boolean; video: boolean; screen: boolean }> {
    if (this.disposed || !this.pc) {
      return { audio: false, video: false, screen: false };
    }

    if (controls.audio !== undefined) {
      await this.applyAudioEnabled(controls.audio);
    }
    if (controls.video !== undefined) {
      await this.applyVideoEnabled(controls.video);
    }
    if (controls.screen !== undefined) {
      if (controls.screen) {
        await this.startScreenShare();
      } else {
        await this.stopScreenShare();
      }
    }

    return {
      audio: this.audioSender?.track !== null && this.audioSender?.track !== undefined,
      video: this.videoSender?.track !== null && this.videoSender?.track !== undefined,
      screen: this.screenStream !== null,
    };
  }

  // ── Toggle implementations ──────────────────────────────────────
  // All three follow the same shape: resolve which track to put on the
  // stored sender, then `replaceTrack`. After mutating we rebuild the
  // local MediaStream as a *new* object so the page's setLocalStream sees
  // a fresh reference and re-binds the <video> element. Keeping the same
  // MediaStream identity was the bug behind "camera doesn't come back
  // after off→on" — React's setState bailed out on identical refs.

  private async applyAudioEnabled(on: boolean): Promise<void> {
    if (!this.audioSender) return;
    if (on) {
      let track = this.audioSender.track;
      if (!track || track.readyState !== "live") {
        try {
          const ms = await navigator.mediaDevices.getUserMedia({ audio: true });
          track = ms.getAudioTracks()[0] ?? null;
        } catch (err) {
          this.emitError("media", err);
          return;
        }
      }
      try {
        await this.audioSender.replaceTrack(track);
      } catch (err) {
        this.emitError("media", err);
        return;
      }
    } else {
      // Stop the device first so the mic indicator goes off, then null
      // out the sender so the SFU forwards silence (no track on m-line).
      const existing = this.audioSender.track;
      try {
        await this.audioSender.replaceTrack(null);
      } catch (err) {
        this.emitError("media", err);
      }
      if (existing) existing.stop();
    }
    this.rebuildLocalStream();
  }

  private async applyVideoEnabled(on: boolean): Promise<void> {
    if (!this.videoSender) return;
    if (on) {
      // If we're currently in screen-share, "video on" is a no-op for
      // the sender (screen track already lives there). The control bar
      // handles the screen toggle separately.
      if (this.screenStream) return;
      let track = this.videoSender.track;
      if (!track || track.readyState !== "live") {
        try {
          const ms = await navigator.mediaDevices.getUserMedia({ video: true });
          track = ms.getVideoTracks()[0] ?? null;
        } catch (err) {
          this.emitError("media", err);
          return;
        }
      }
      try {
        await this.videoSender.replaceTrack(track);
      } catch (err) {
        this.emitError("media", err);
        return;
      }
    } else {
      const existing = this.videoSender.track;
      try {
        await this.videoSender.replaceTrack(null);
      } catch (err) {
        this.emitError("media", err);
      }
      if (existing) existing.stop();
    }
    this.rebuildLocalStream();
  }

  // Rebuilds `this.localStream` from whatever the senders currently hold
  // and emits a fresh MediaStream object so React's setLocalStream sees a
  // changed reference. This is what makes the local <video> tile re-bind
  // after replaceTrack. Without this, removing the only video track from
  // an in-place stream leaves the <video> element bound to a now-empty
  // stream and it never renders the new track on the next replace.
  private rebuildLocalStream(): void {
    const tracks: MediaStreamTrack[] = [];
    if (this.audioSender?.track) tracks.push(this.audioSender.track);
    if (this.videoSender?.track) tracks.push(this.videoSender.track);
    const next = new MediaStream(tracks);
    this.localStream = next;
    this.emit("local-stream", next);
  }

  getLocalStream(): MediaStream | null {
    return this.localStream;
  }

  getConnectionState(): EngineConnectionState {
    return this.state;
  }

  // ── Internals ────────────────────────────────────────────────────
  private async acquireLocalMedia(opts: JoinOptions): Promise<MediaStream> {
    // Viewer mode: don't even prompt for devices.
    if (!opts.audio && !opts.video) {
      return new MediaStream();
    }

    // Try the full set first; on NotAllowedError fall back to whichever
    // permission was granted. Some browsers throw if both are denied,
    // others return an empty track list — handle both.
    try {
      return await navigator.mediaDevices.getUserMedia({
        audio: opts.audio,
        video: opts.video,
      });
    } catch (err) {
      // Graceful degradation: try audio-only, then video-only, then
      // surrender to viewer mode.
      if (opts.audio && opts.video) {
        try {
          return await navigator.mediaDevices.getUserMedia({ audio: true });
        } catch {
          /* fall through */
        }
      }
      this.emitError("media", err);
      return new MediaStream();
    }
  }

  private pickIceServers(token: MediaSessionToken): RTCIceServer[] {
    // Future: when the token starts carrying TURN creds, merge them here.
    // For now just use the default STUN list; turn_strategy === 'turn_only'
    // would require additional configuration we don't yet have.
    void token;
    return DEFAULT_ICE;
  }

  private onRemoteTrack(ev: RTCTrackEvent): void {
    if (this.disposed) return;
    const stream = ev.streams[0];
    if (!stream) return;

    // The SFU labels each forwarded stream with the source participant id
    // via MediaStream.id. We surface both raw kind tracks separately so
    // the UI can render audio+video in one tile.
    const key = `${stream.id}:${ev.track.kind}`;
    if (this.seenStreams.has(key)) return;

    const remote: RemoteTrack = {
      participantId: stream.id,
      stream,
      kind: ev.track.kind === "audio" ? "audio" : "video",
    };
    this.seenStreams.set(key, remote);
    this.emit("remote-track", remote);

    // When the stream ends (participant leaves / SFU cuts the flow),
    // emit a removal so the UI can tear down its <video> element.
    const onEnded = () => {
      this.seenStreams.delete(key);
      this.emit("remote-track-removed", {
        participantId: stream.id,
        streamId: stream.id,
      });
      ev.track.removeEventListener("ended", onEnded);
    };
    ev.track.addEventListener("ended", onEnded);
  }

  // Handle inbound WS events. We only care about signal.candidate addressed
  // to our user from the SFU (FromUser == uuid.Nil). Ignore offers/answers —
  // we drive those over HTTP, so a WS-delivered offer means a stale or
  // mis-routed packet.
  private onSignalEvent(evt: ServerEvent): void {
    if (this.disposed || !this.pc) return;
    if (evt.type !== WS.SignalCandidate) return;
    const payload = evt.payload as SignalPayload | undefined;
    if (!payload || !payload.candidate) return;
    if (payload.call_id !== this.callId) return;

    const init: RTCIceCandidateInit = {
      candidate: payload.candidate,
      sdpMid: payload.sdp_mid ?? null,
      sdpMLineIndex:
        payload.sdp_mline_index === undefined ? null : payload.sdp_mline_index,
    };

    if (!this.remoteDescriptionSet) {
      // addIceCandidate before setRemoteDescription throws "remote
      // description is null" — buffer until the answer is applied.
      this.remoteIceBuffer.push(init);
      return;
    }
    void this.pc
      .addIceCandidate(init)
      .catch((err) => this.emitError("ice", err));
  }

  private onLocalIceCandidate(ev: RTCPeerConnectionIceEvent): void {
    if (this.disposed || !ev.candidate) return;
    // Buffer candidates that fire before the offer/answer completes; the
    // SFU has nothing to attach them to until then, so POSTing early
    // returns 404. Late candidates trickle straight through.
    if (!this.offerCompleted) {
      this.pendingIce.push(ev.candidate);
      return;
    }
    this.sendIceCandidate(ev.candidate);
  }

  private sendIceCandidate(c: RTCIceCandidate): void {
    if (!this.token) return;
    // Fire-and-forget. A failed ICE trickle shouldn't break the call —
    // the SFU already has our initial candidates from the offer SDP and
    // will fall back to those if trickling drops.
    void callsApi
      .mediaIceCandidate(this.wsId, this.callId, {
        token: this.token.token,
        candidate: c.candidate,
        sdp_mid: c.sdpMid ?? null,
        sdp_mline_index: c.sdpMLineIndex ?? null,
      })
      .catch((err) => this.emitError("ice", err));
  }

  private onIceStateChange(s: RTCIceConnectionState): void {
    if (this.disposed) return;
    switch (s) {
      case "checking":
        this.setState("signaling");
        break;
      case "connected":
      case "completed":
        this.setState("connected");
        break;
      case "disconnected":
        this.setState("reconnecting");
        break;
      case "failed":
        this.setState("failed");
        break;
      case "closed":
        this.setState("closed");
        break;
    }
  }

  private onPeerStateChange(s: RTCPeerConnectionState): void {
    if (this.disposed) return;
    if (s === "failed") this.setState("failed");
    if (s === "connected") this.setState("connected");
    if (s === "closed") this.setState("closed");
  }

  // Waits briefly for ICE gathering to produce local candidates so the
  // initial offer isn't empty. Returns as soon as gathering completes
  // *or* the deadline hits — whichever comes first.
  private waitForInitialIce(pc: RTCPeerConnection, ms: number): Promise<void> {
    if (pc.iceGatheringState === "complete") return Promise.resolve();
    return new Promise((resolve) => {
      const onChange = () => {
        if (pc.iceGatheringState === "complete") {
          pc.removeEventListener("icegatheringstatechange", onChange);
          resolve();
        }
      };
      pc.addEventListener("icegatheringstatechange", onChange);
      setTimeout(() => {
        pc.removeEventListener("icegatheringstatechange", onChange);
        resolve();
      }, ms);
    });
  }

  // Screen share: swap the camera track on the existing video sender for a
  // getDisplayMedia track (same m-line, no SDP change, no renegotiation).
  // Stash the camera track in `cameraTrackBeforeShare` so we can restore
  // it on stop. This is the same pattern Google Meet / Zoom Web SDK use.
  private cameraTrackBeforeShare: MediaStreamTrack | null = null;

  private async startScreenShare(): Promise<void> {
    if (this.screenStream || !this.pc || !this.videoSender) return;
    let stream: MediaStream;
    try {
      stream = await navigator.mediaDevices.getDisplayMedia({
        video: true,
        audio: false,
      });
    } catch (err) {
      this.emitError("media", err);
      return;
    }
    const screenTrack = stream.getVideoTracks()[0];
    if (!screenTrack) {
      stream.getTracks().forEach((t) => t.stop());
      return;
    }
    this.screenStream = stream;
    // Stash the camera track so we can put it back on stop. We don't
    // stop() the camera here — the user's camera LED stays on, and
    // putting a fresh track back via getUserMedia would cost a permission
    // round-trip on some browsers.
    this.cameraTrackBeforeShare = this.videoSender.track;
    try {
      await this.videoSender.replaceTrack(screenTrack);
    } catch (err) {
      this.emitError("media", err);
      stream.getTracks().forEach((t) => t.stop());
      this.screenStream = null;
      this.cameraTrackBeforeShare = null;
      return;
    }
    screenTrack.addEventListener("ended", () => {
      // User hit the browser's "Stop sharing" chip. Keep engine + UI
      // in sync — the caller should also pipe this back through the
      // HTTP control plane.
      void this.stopScreenShare();
    });
    this.rebuildLocalStream();
  }

  private async stopScreenShare(): Promise<void> {
    if (!this.screenStream || !this.pc || !this.videoSender) return;
    // Restore the camera track (or null if camera was off when we started
    // sharing). No renegotiation — same m-line, just a different track.
    const restore = this.cameraTrackBeforeShare;
    this.cameraTrackBeforeShare = null;
    try {
      await this.videoSender.replaceTrack(restore);
    } catch (err) {
      this.emitError("media", err);
    }
    for (const t of this.screenStream.getTracks()) t.stop();
    this.screenStream = null;
    this.rebuildLocalStream();
  }

  private async teardown(): Promise<void> {
    if (this.disposed) return;
    this.disposed = true;
    // Drop WS subscription first so a late candidate doesn't arrive while
    // we're closing the PC. The unsubscribe message itself is fire-and-
    // forget; the singleton client survives across calls.
    try {
      this.wsUnsub?.();
    } catch {
      /* ignore */
    }
    this.wsUnsub = null;
    try {
      this.wsClient?.unsubscribe(this.signalRoom);
    } catch {
      /* ignore */
    }
    this.wsClient = null;
    try {
      // Stop every track we know about: the cached camera-before-share,
      // the active screen capture, plus whatever is currently sitting on
      // each sender (mic + cam/screen). Going through senders directly
      // catches the case where applyVideoEnabled stopped the device track
      // and replaced with null — the sender is the source of truth.
      for (const t of this.localStream?.getTracks() ?? []) t.stop();
      for (const t of this.screenStream?.getTracks() ?? []) t.stop();
      this.cameraTrackBeforeShare?.stop();
      if (this.audioSender?.track) this.audioSender.track.stop();
      if (this.videoSender?.track) this.videoSender.track.stop();
    } catch {
      /* ignore */
    }
    this.cameraTrackBeforeShare = null;
    this.audioSender = null;
    this.videoSender = null;
    this.localStream = null;
    this.screenStream = null;
    this.emit("local-stream", null);
    try {
      this.pc?.close();
    } catch {
      /* ignore */
    }
    this.pc = null;
    this.token = null;
    this.seenStreams.clear();
    this.pendingIce = [];
    this.remoteIceBuffer = [];
    this.offerCompleted = false;
    this.remoteDescriptionSet = false;
  }

  private emitError(stage: EngineErrorPayload["stage"], cause: unknown): void {
    const message =
      cause instanceof Error ? cause.message : String(cause ?? "unknown error");
    this.emit("error", { stage, message, cause });
  }
}

// React convenience: most callers only ever want one engine per call. We
// expose a tiny factory so the detail page doesn't hand-manage singleton
// lifetime. The userId is required so we can subscribe to the right
// per-user signal room (aloqa.signal.{userId}).
export function createCallEngine(
  wsId: UUID,
  callId: UUID,
  userId: UUID,
): CallEngine {
  return new CallEngine(wsId, callId, userId);
}

// Capability probes. Hoisted to module scope so the page can call them
// before showing the "Join meeting" CTA — no point lighting up the button
// if we know construction will throw.

/**
 * True iff the page can construct an RTCPeerConnection. Returns false on
 * (a) Safari Lockdown Mode, (b) ancient browsers without WebRTC, and
 * (c) SSR (window/RTCPeerConnection both undefined).
 */
export function isWebRTCSupported(): boolean {
  if (typeof window === "undefined") return false;
  return (
    typeof RTCPeerConnection !== "undefined" &&
    typeof navigator !== "undefined" &&
    !!navigator.mediaDevices
  );
}

/**
 * Heuristic for "user is on Safari with Lockdown Mode enabled". Used to
 * pick the right error message — there is no DOM API to query Lockdown
 * directly, so we infer it from "WebKit-based UA + RTCPeerConnection
 * missing", which is the visible symptom Lockdown produces.
 */
export function isLikelySafariLockdown(): boolean {
  if (typeof navigator === "undefined") return false;
  const ua = navigator.userAgent || "";
  // WebKit on iOS/macOS exposes "Safari/" but not "Chrome/" in the UA.
  // (Chromium WebView and others include both.)
  const isWebKit = /Safari\//.test(ua) && !/Chrome\/|Chromium\/|Edg\//.test(ua);
  return isWebKit && typeof RTCPeerConnection === "undefined";
}
