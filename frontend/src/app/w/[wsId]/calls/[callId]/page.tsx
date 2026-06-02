"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useParams, useRouter } from "next/navigation";
import {
  ArrowLeft,
  Boxes,
  Check,
  Circle,
  Copy,
  Disc,
  Download,
  FileVideo,
  LogOut,
  Megaphone,
  Mic,
  MicOff,
  MonitorUp,
  MonitorX,
  PhoneOff,
  Plus,
  Radio,
  Trash2,
  Users,
  Video,
  VideoOff,
  Wifi,
  WifiOff,
  X,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { Avatar } from "@/components/ui/Avatar";
import { API_BASE } from "@/lib/api/client";
import { loadTokens } from "@/lib/auth";
import { breakoutsApi, callsApi, recordingsApi } from "@/lib/api/endpoints";
import {
  createCallEngine,
  isLikelySafariLockdown,
  isWebRTCSupported,
  type CallEngine,
  type EngineConnectionState,
} from "@/lib/webrtc/engine";
import { rt } from "@/lib/ws/client";
import { WS, type ServerEvent } from "@/lib/ws/events";
import type {
  BreakoutRoom,
  Call,
  CallParticipant,
  Recording,
  RecordingArtifact,
  UUID,
} from "@/lib/types";
import { useAuth } from "@/stores/auth";
import { useMembers, shortId } from "@/stores/members";

/*
 * Call room. Two planes need to stay in sync:
 *
 *   Control plane  — /calls/{id}/join|leave|end + /media (HTTP PUT).
 *                    The server is authoritative; WS events refresh this
 *                    view so mute states / joins / leaves from other tabs
 *                    appear immediately.
 *   Media plane    — WebRTC peer connection to the SFU via CallEngine.
 *                    The engine owns the <video> stream lifecycle; this
 *                    view just binds the streams it emits to <video>
 *                    elements keyed by participant id.
 *
 * We keep the visual language dark (bg-rail/bg-sidebar) so video tiles
 * pop — Aloqa's light main surface would blow out any webcam preview.
 */
export default function CallRoomPage() {
  const router = useRouter();
  const params = useParams<{ wsId: string; callId: string }>();
  const { wsId, callId } = params;
  const me = useAuth((s) => s.user);

  const [call, setCall] = useState<Call | null>(null);
  const [participants, setParticipants] = useState<CallParticipant[]>([]);
  const [joined, setJoined] = useState(false);
  const [joining, setJoining] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  // Media-plane state.
  const engineRef = useRef<CallEngine | null>(null);
  const [localStream, setLocalStream] = useState<MediaStream | null>(null);
  const [remoteStreams, setRemoteStreams] = useState<Map<string, MediaStream>>(
    new Map(),
  );
  const [engineState, setEngineState] = useState<EngineConnectionState>("idle");
  // Browser capability probe. Computed post-mount so SSR (which has no
  // `window`/RTCPeerConnection) doesn't render a permanent "unsupported"
  // banner. `null` = not yet checked; the PreJoinCard treats null as
  // "assume supported" so the loading flash is invisible.
  const [webrtcOk, setWebrtcOk] = useState<boolean | null>(null);
  const [lockdownLikely, setLockdownLikely] = useState(false);
  useEffect(() => {
    setWebrtcOk(isWebRTCSupported());
    setLockdownLikely(isLikelySafariLockdown());
  }, []);

  // ── Recording state ─────────────────────────────────────────────
  // The call may have a single in-flight recording (status="recording")
  // and any number of past ones (status="processing"|"ready"|"failed").
  // We split them so the header pill always reflects "is something
  // currently being captured" while the side panel shows everything.
  const [recordings, setRecordings] = useState<Recording[]>([]);
  const [recordingBusy, setRecordingBusy] = useState(false);
  const [showRecordings, setShowRecordings] = useState(false);

  const activeRecording = useMemo(
    () => recordings.find((r) => r.status === "recording") ?? null,
    [recordings],
  );

  const refreshRecordings = useCallback(async () => {
    try {
      const list = await recordingsApi.listByCall(wsId, callId);
      setRecordings(list ?? []);
    } catch {
      // Recording list is non-critical UI; failures are silent so an
      // empty backend (e.g. dev DB without recording capture configured)
      // doesn't block the rest of the call surface from rendering.
    }
  }, [wsId, callId]);

  useEffect(() => {
    void refreshRecordings();
  }, [refreshRecordings]);

  // ── Breakouts state ─────────────────────────────────────────────
  // Backend gates: host/co_host can create/close/broadcast; the call must
  // be active and have settings.breakout_rooms enabled. Participants
  // self-join via the /join endpoint — there's no host-driven
  // assignment, so the UI is "list rooms + per-room Join button" rather
  // than a drag-and-drop assigner.
  const [breakouts, setBreakouts] = useState<BreakoutRoom[]>([]);
  const [breakoutsBusy, setBreakoutsBusy] = useState(false);
  const [showBreakouts, setShowBreakouts] = useState(false);

  const refreshBreakouts = useCallback(async () => {
    try {
      const list = await breakoutsApi.list(wsId, callId);
      setBreakouts(list ?? []);
    } catch {
      /* non-critical */
    }
  }, [wsId, callId]);

  useEffect(() => {
    void refreshBreakouts();
  }, [refreshBreakouts]);

  const myP = participants.find((p) => p.user_id === me?.id && !p.left_at) ?? null;
  const muted = Boolean(myP?.audio_muted);
  const videoOff = Boolean(myP?.video_muted);
  const screen = Boolean(myP?.screen_sharing);

  // ── Control plane ────────────────────────────────────────────────
  const load = useCallback(async () => {
    try {
      const [c, rawPs] = await Promise.all([
        callsApi.get(wsId, callId),
        callsApi.participants(wsId, callId),
      ]);
      const ps = rawPs ?? [];
      setCall(c);
      setParticipants(ps);
      setJoined(ps.some((p) => p.user_id === me?.id && !p.left_at));
    } catch (e) {
      setError(e instanceof Error ? e.message : "failed to load");
    }
  }, [wsId, callId, me?.id]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    const client = rt();
    client.start();
    const handle = (evt: ServerEvent) => {
      if (
        evt.type !== WS.CallParticipantJoined &&
        evt.type !== WS.CallParticipantLeft &&
        evt.type !== WS.CallParticipantUpdated &&
        evt.type !== WS.CallEnded
      )
        return;
      const payload = evt.payload as { call_id?: UUID };
      if (payload.call_id && payload.call_id !== callId) return;
      void load();
    };
    const off = client.on(handle);
    return () => off();
  }, [callId, load]);

  // ── Media plane ──────────────────────────────────────────────────
  // Construct the engine lazily on first join. Always tear it down on
  // navigation away — the browser will otherwise keep the camera LED on.
  useEffect(() => {
    return () => {
      void engineRef.current?.leave();
      engineRef.current = null;
    };
  }, []);

  async function join() {
    if (joining || engineRef.current) return;
    if (!me?.id) {
      // We need our own user id to subscribe to the per-user WS signal
      // room (aloqa.signal.{userId}); the SFU trickles ICE candidates
      // there. Without it the engine would silently never connect.
      setError("Authenticating… please retry in a moment.");
      return;
    }
    setJoining(true);
    setError(null);
    try {
      // Tell the backend first so the SFU already has a participant row
      // to attach the inbound SDP to when mediaOffer arrives. Skip if we
      // already appear in the HTTP participants list (e.g. host who just
      // created the call, or a returning user who hasn't been kicked).
      if (!joined) {
        await callsApi.join(wsId, callId);
      }

      const engine = createCallEngine(wsId, callId, me.id);
      engineRef.current = engine;
      engine.on("local-stream", (s) => setLocalStream(s));
      engine.on("remote-track", (rt) => {
        setRemoteStreams((prev) => {
          const next = new Map(prev);
          const existing = next.get(rt.participantId);
          if (existing) {
            // Merge tracks into the existing stream so audio+video land in
            // a single <video> tile.
            for (const t of rt.stream.getTracks()) {
              if (!existing.getTracks().some((x) => x.id === t.id)) {
                existing.addTrack(t);
              }
            }
          } else {
            next.set(rt.participantId, rt.stream);
          }
          return next;
        });
      });
      engine.on("remote-track-removed", ({ participantId }) => {
        setRemoteStreams((prev) => {
          if (!prev.has(participantId)) return prev;
          const next = new Map(prev);
          next.delete(participantId);
          return next;
        });
      });
      engine.on("connection-state", (s) => setEngineState(s));
      engine.on("error", (err) => {
        // Only surface hard failures; a "media" stage error from a denied
        // mic doesn't mean the call is broken (we downgrade silently).
        if (err.stage === "signaling" || err.stage === "token") {
          setError(err.message);
        } else {
          console.warn("[CallEngine]", err.stage, err.message);
        }
      });

      await engine.join({ audio: true, video: true });

      // After we know which tracks actually came back from getUserMedia,
      // sync the control plane so other participants see the right mute
      // states. A denied mic → audio_muted = true, etc.
      const stream = engine.getLocalStream();
      const hasAudio =
        stream?.getAudioTracks().some((t) => t.enabled) ?? false;
      const hasVideo =
        stream?.getVideoTracks().some((t) => t.enabled) ?? false;
      await callsApi
        .updateMedia(wsId, callId, {
          audio_muted: !hasAudio,
          video_muted: !hasVideo,
          screen_sharing: false,
        })
        .catch(() => {
          /* control plane will re-sync on next WS event */
        });

      setJoined(true);
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : "join failed");
      engineRef.current = null;
    } finally {
      setJoining(false);
    }
  }

  async function leave() {
    try {
      await engineRef.current?.leave();
    } catch {
      /* ignore */
    }
    engineRef.current = null;
    setLocalStream(null);
    setRemoteStreams(new Map());
    try {
      await callsApi.leave(wsId, callId);
    } catch {
      /* ignore */
    }
    setJoined(false);
    router.push(`/w/${wsId}/calls`);
  }

  async function endMeeting() {
    if (!confirm("End the meeting for everyone?")) return;
    try {
      await engineRef.current?.leave();
    } catch {
      /* ignore */
    }
    engineRef.current = null;
    try {
      await callsApi.end(wsId, callId);
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not end");
      return;
    }
    router.push(`/w/${wsId}/calls`);
  }

  async function toggleMic() {
    const next = muted;
    // Mute in the engine first so we cut audio bytes before the server
    // event fans out to peers — gives us the snappier click-to-mute feel.
    await engineRef.current?.setMedia({ audio: next });
    try {
      await callsApi.updateMedia(wsId, callId, { audio_muted: !next });
      await load();
    } catch {
      /* ignore */
    }
  }

  async function toggleVideo() {
    const next = videoOff;
    await engineRef.current?.setMedia({ video: next });
    try {
      await callsApi.updateMedia(wsId, callId, { video_muted: !next });
      await load();
    } catch {
      /* ignore */
    }
  }

  async function toggleScreen() {
    const next = !screen;
    await engineRef.current?.setMedia({ screen: next });
    try {
      await callsApi.updateMedia(wsId, callId, { screen_sharing: next });
      await load();
    } catch {
      /* ignore */
    }
  }

  async function copyLink() {
    if (typeof window === "undefined") return;
    try {
      await navigator.clipboard.writeText(window.location.href);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      /* ignore — clipboard is best-effort */
    }
  }

  // Start the call's recording, or stop the in-flight one. The backend
  // gates this on participant.role being host/co_host AND on
  // call.settings.recording === true (the create-call form opts in to
  // recording by default; legacy calls without it surface a tooltip).
  async function toggleRecording() {
    if (recordingBusy) return;
    setRecordingBusy(true);
    setError(null);
    try {
      if (activeRecording) {
        await recordingsApi.stop(wsId, callId, activeRecording.id);
      } else {
        await recordingsApi.start(wsId, callId);
      }
      await refreshRecordings();
    } catch (e) {
      setError(e instanceof Error ? e.message : "recording failed");
    } finally {
      setRecordingBusy(false);
    }
  }

  // ── Breakout actions ────────────────────────────────────────────
  // Each handler is a thin wrapper over the API client + a refresh.
  // Failures surface in the page-level error banner; busy state is
  // shared across actions so the panel can show one global spinner
  // instead of fighting per-button states.
  async function createBreakouts(rooms: Array<{ name: string; time_limit?: number }>) {
    if (breakoutsBusy) return;
    setBreakoutsBusy(true);
    setError(null);
    try {
      await breakoutsApi.create(wsId, callId, rooms);
      await refreshBreakouts();
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not create rooms");
    } finally {
      setBreakoutsBusy(false);
    }
  }

  async function joinBreakout(roomId: UUID) {
    if (breakoutsBusy) return;
    setBreakoutsBusy(true);
    setError(null);
    try {
      await breakoutsApi.join(wsId, callId, roomId);
      await refreshBreakouts();
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not join room");
    } finally {
      setBreakoutsBusy(false);
    }
  }

  async function returnFromBreakout() {
    if (breakoutsBusy) return;
    setBreakoutsBusy(true);
    setError(null);
    try {
      await breakoutsApi.return(wsId, callId);
      await refreshBreakouts();
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not return");
    } finally {
      setBreakoutsBusy(false);
    }
  }

  async function closeBreakout(roomId: UUID) {
    if (breakoutsBusy) return;
    setBreakoutsBusy(true);
    setError(null);
    try {
      await breakoutsApi.close(wsId, callId, roomId);
      await refreshBreakouts();
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not close room");
    } finally {
      setBreakoutsBusy(false);
    }
  }

  async function closeAllBreakouts() {
    if (breakoutsBusy) return;
    if (!confirm("Close all breakout rooms and return everyone to the main room?")) return;
    setBreakoutsBusy(true);
    setError(null);
    try {
      await breakoutsApi.closeAll(wsId, callId);
      await refreshBreakouts();
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not close rooms");
    } finally {
      setBreakoutsBusy(false);
    }
  }

  async function broadcastBreakouts(message: string) {
    if (breakoutsBusy || !message.trim()) return;
    setBreakoutsBusy(true);
    setError(null);
    try {
      await breakoutsApi.broadcast(wsId, callId, message);
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not broadcast");
    } finally {
      setBreakoutsBusy(false);
    }
  }

  // Stream an artifact byte range to disk via fetch + blob — we can't use
  // a plain <a download> because the bearer token isn't on the URL, and
  // we don't want to leak it as a query string.
  async function downloadArtifact(rec: Recording, art: RecordingArtifact) {
    if (typeof window === "undefined") return;
    const tokens = loadTokens();
    if (!tokens) {
      setError("Sign in expired — please reload");
      return;
    }
    try {
      const url =
        API_BASE +
        recordingsApi.downloadArtifactUrl(wsId, callId, rec.id, art.id);
      const res = await fetch(url, {
        headers: { Authorization: `Bearer ${tokens.accessToken}` },
      });
      if (!res.ok) throw new Error(`download failed (${res.status})`);
      const blob = await res.blob();
      const objUrl = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = objUrl;
      a.download = filenameForArtifact(rec, art);
      document.body.appendChild(a);
      a.click();
      a.remove();
      URL.revokeObjectURL(objUrl);
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not download");
    }
  }

  // ── Derived ──────────────────────────────────────────────────────
  const activeParticipants = useMemo(
    () => participants.filter((p) => !p.left_at),
    [participants],
  );
  const isHost = myP?.role === "host" || myP?.role === "co_host";
  // Backend transitions ringing → active when the first participant actually
  // joins the SFU. From the host's perspective the meeting is "live" the
  // moment it's created — don't flash "ended" on a brand-new ringing call.
  const live = call ? call.status !== "ended" : false;
  const statusLabel =
    call?.status === "ringing" ? "Waiting" : live ? "Live" : "Ended";
  // The media engine is "connected" once it has at least begun signaling.
  // Idle/closed/failed all mean we owe the user a pre-join prompt before
  // we touch the camera again.
  const mediaConnected =
    engineState !== "idle" &&
    engineState !== "closed" &&
    engineState !== "failed";
  // Recording requires three things to align:
  //   1. The call's settings opted in (`call.settings.recording === true`).
  //      The create dialog flips this on by default; legacy calls without
  //      it surface a tooltip on the disabled button.
  //   2. The local user is host or co_host (server-enforced).
  //   3. The call status is "active" (server returns 403 otherwise — the
  //      service won't record a "ringing" call). A meeting transitions
  //      from "ringing" to "active" the moment the first participant
  //      joins the SFU; until then the button is disabled with a hint.
  const recordingAllowedBySettings = Boolean(call?.settings?.recording);
  const callIsActive = call?.status === "active";
  const showRecordButton = isHost && mediaConnected;
  const recordDisabled = !recordingAllowedBySettings || !callIsActive;
  const recordDisabledReason = !recordingAllowedBySettings
    ? "Recording is disabled in this call's settings"
    : !callIsActive
      ? "Recording starts once the meeting is active (a participant has joined the SFU)"
      : undefined;
  // Breakouts use the same gate triad as recording: settings opt-in,
  // host/co_host, call active. The chip appears for everyone in the call
  // (so participants can see room list and join), but the management
  // controls inside the panel are host-only.
  const breakoutsAllowedBySettings = Boolean(call?.settings?.breakout_rooms);
  const activeBreakouts = useMemo(
    () => breakouts.filter((r) => r.status === "active"),
    [breakouts],
  );

  if (error && !call) {
    return (
      <div className="flex h-full items-center justify-center bg-rail text-sm text-status-red">
        {error}
      </div>
    );
  }
  if (!call) {
    return (
      <div className="flex h-full items-center justify-center bg-rail text-sm text-white/60">
        Loading meeting…
      </div>
    );
  }

  return (
    <div className="flex h-full flex-col bg-rail">
      {/* Header */}
      <header className="flex h-[52px] items-center gap-4 border-b border-white/5 bg-sidebar px-6">
        <div className="min-w-0">
          <div className="truncate text-sm font-semibold text-white">
            {call.title}
          </div>
          <div className="mt-0.5 flex items-center gap-2 text-[11px] text-white/50">
            <span className="capitalize">{call.type.replace("_", " ")}</span>
            <span className="text-white/20">·</span>
            <span
              className={cn(
                "inline-flex items-center gap-1",
                live ? "text-status-green" : "text-white/50",
              )}
            >
              {live ? (
                <span className="relative flex h-1.5 w-1.5">
                  <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-status-green opacity-70" />
                  <span className="relative inline-flex h-1.5 w-1.5 rounded-full bg-status-green" />
                </span>
              ) : null}
              {statusLabel}
            </span>
            {joined ? (
              <>
                <span className="text-white/20">·</span>
                <EngineStateBadge state={engineState} />
              </>
            ) : null}
            {activeRecording ? (
              <>
                <span className="text-white/20">·</span>
                <span className="inline-flex items-center gap-1 text-status-red">
                  <span className="relative flex h-1.5 w-1.5">
                    <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-status-red opacity-70" />
                    <span className="relative inline-flex h-1.5 w-1.5 rounded-full bg-status-red" />
                  </span>
                  Recording
                </span>
              </>
            ) : null}
          </div>
        </div>

        <div className="ml-auto flex items-center gap-2">
          <span className="hidden items-center gap-1.5 rounded-md bg-white/5 px-2.5 py-1 text-[11px] text-white/60 sm:inline-flex">
            <Users className="h-3 w-3" />
            {activeParticipants.length} participant
            {activeParticipants.length === 1 ? "" : "s"}
          </span>
          {recordings.length > 0 ? (
            <button
              onClick={() => {
                setShowRecordings((s) => !s);
                setShowBreakouts(false);
              }}
              className={cn(
                "inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-[11px] transition-colors",
                showRecordings
                  ? "border-accent/50 bg-accent/15 text-accent"
                  : "border-white/10 bg-white/5 text-white/80 hover:bg-white/10",
              )}
              title="Recordings"
            >
              <FileVideo className="h-3 w-3" />
              {recordings.length}
            </button>
          ) : null}
          {breakoutsAllowedBySettings && (isHost || activeBreakouts.length > 0) ? (
            <button
              onClick={() => {
                setShowBreakouts((s) => !s);
                setShowRecordings(false);
              }}
              className={cn(
                "inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-[11px] transition-colors",
                showBreakouts
                  ? "border-accent/50 bg-accent/15 text-accent"
                  : "border-white/10 bg-white/5 text-white/80 hover:bg-white/10",
              )}
              title="Breakout rooms"
            >
              <Boxes className="h-3 w-3" />
              {activeBreakouts.length || "Rooms"}
            </button>
          ) : null}
          <button
            onClick={copyLink}
            className="inline-flex items-center gap-1.5 rounded-md border border-white/10 bg-white/5 px-2.5 py-1 text-[11px] text-white/80 transition-colors hover:bg-white/10"
            title="Copy meeting link"
          >
            {copied ? (
              <>
                <Check className="h-3 w-3" /> Copied
              </>
            ) : (
              <>
                <Copy className="h-3 w-3" /> Copy link
              </>
            )}
          </button>
        </div>
      </header>

      {/* Stage */}
      <div className="relative flex-1 overflow-hidden">
        {/* Recordings side panel — opens via the header chip. We render
            it as an overlay rather than a column so the stage layout
            doesn't reflow for a transient panel. */}
        {showRecordings ? (
          <RecordingsPanel
            recordings={recordings}
            wsId={wsId}
            callId={callId}
            onClose={() => setShowRecordings(false)}
            onDownload={downloadArtifact}
            onRefresh={refreshRecordings}
          />
        ) : null}
        {showBreakouts ? (
          <BreakoutsPanel
            rooms={breakouts}
            isHost={isHost}
            busy={breakoutsBusy}
            callIsActive={callIsActive}
            onClose={() => setShowBreakouts(false)}
            onRefresh={refreshBreakouts}
            onCreate={createBreakouts}
            onJoin={joinBreakout}
            onReturn={returnFromBreakout}
            onCloseRoom={closeBreakout}
            onCloseAll={closeAllBreakouts}
            onBroadcast={broadcastBreakouts}
          />
        ) : null}
      <div className="h-full overflow-auto px-6 py-6">
        {error && call ? (
          <div className="mx-auto mb-4 max-w-md rounded-lg border border-status-red/40 bg-status-red/10 px-4 py-2 text-center text-xs text-status-red">
            {error}
          </div>
        ) : null}

        {!mediaConnected ? (
          <PreJoinCard
            live={live}
            joining={joining}
            alreadyInRoom={joined}
            webrtcOk={webrtcOk}
            lockdownLikely={lockdownLikely}
            onJoin={join}
            onCancel={() => router.push(`/w/${wsId}/calls`)}
          />
        ) : activeParticipants.length === 0 ? (
          <EmptyStage live={live} />
        ) : (
          <div
            className={cn(
              "mx-auto grid w-full max-w-6xl gap-4",
              activeParticipants.length === 1
                ? "grid-cols-1"
                : activeParticipants.length === 2
                  ? "grid-cols-1 md:grid-cols-2"
                  : "grid-cols-1 sm:grid-cols-2 lg:grid-cols-3",
            )}
          >
            {activeParticipants.map((p) => {
              const isSelf = p.user_id === me?.id;
              const stream = isSelf ? localStream : remoteStreams.get(p.id);
              return (
                <ParticipantTile
                  key={p.id}
                  wsId={wsId}
                  participant={p}
                  stream={stream ?? null}
                  isSelf={isSelf}
                />
              );
            })}
          </div>
        )}
      </div>
      </div>

      {/* Control bar */}
      {mediaConnected ? (
        <footer className="flex h-[80px] items-center justify-center gap-3 border-t border-white/5 bg-sidebar px-6">
          <CircleButton
            active={!muted}
            dangerWhenOff
            onIcon={<Mic className="h-5 w-5" />}
            offIcon={<MicOff className="h-5 w-5" />}
            label={muted ? "Unmute" : "Mute"}
            onClick={toggleMic}
          />
          <CircleButton
            active={!videoOff}
            dangerWhenOff
            onIcon={<Video className="h-5 w-5" />}
            offIcon={<VideoOff className="h-5 w-5" />}
            label={videoOff ? "Start video" : "Stop video"}
            onClick={toggleVideo}
          />
          <CircleButton
            active={screen}
            tone="accent"
            onIcon={<MonitorUp className="h-5 w-5" />}
            offIcon={<MonitorX className="h-5 w-5" />}
            label={screen ? "Stop sharing" : "Share screen"}
            onClick={toggleScreen}
          />
          {showRecordButton ? (
            <RecordButton
              recording={Boolean(activeRecording)}
              busy={recordingBusy}
              // The "stop" path is always allowed (we have an in-flight
              // recording → backend lets the host stop). Only the "start"
              // path needs the gate.
              disabled={!activeRecording && recordDisabled}
              disabledReason={!activeRecording ? recordDisabledReason : undefined}
              onClick={toggleRecording}
            />
          ) : null}

          <div className="mx-2 h-8 w-px bg-white/10" />

          <button
            onClick={leave}
            title="Leave"
            className="inline-flex h-12 w-12 items-center justify-center rounded-full bg-status-red text-white transition-colors hover:bg-status-red/90"
          >
            <LogOut className="h-5 w-5" />
          </button>
          {isHost && live ? (
            <button
              onClick={endMeeting}
              title="End for all"
              className="inline-flex h-10 items-center gap-2 rounded-full border border-white/15 bg-transparent px-4 text-sm text-white/80 transition-colors hover:bg-white/10"
            >
              <PhoneOff className="h-4 w-4" />
              End for all
            </button>
          ) : null}
        </footer>
      ) : null}
    </div>
  );
}

// ── Pre-join card ────────────────────────────────────────────────
function PreJoinCard({
  live,
  joining,
  alreadyInRoom,
  webrtcOk,
  lockdownLikely,
  onJoin,
  onCancel,
}: {
  live: boolean;
  joining: boolean;
  alreadyInRoom: boolean;
  // null = capability check not yet completed (SSR / first paint). We
  // treat null as "assume supported" so the join CTA isn't suppressed by
  // a flash of the unsupported state.
  webrtcOk: boolean | null;
  lockdownLikely: boolean;
  onJoin: () => void;
  onCancel: () => void;
}) {
  // Capability gate. When the browser can't construct an
  // RTCPeerConnection (Safari Lockdown Mode being the realistic case),
  // refuse to even render the "Join meeting" button — clicking would
  // immediately fail with a cryptic ReferenceError. Instead, surface an
  // actionable message naming the exact toggle to flip.
  const blocked = webrtcOk === false;
  if (blocked) {
    return (
      <div className="mx-auto flex h-full max-w-md flex-col items-center justify-center gap-4 text-center">
        <div className="grid h-16 w-16 place-items-center rounded-2xl bg-status-red/15 text-status-red">
          <WifiOff className="h-7 w-7" />
        </div>
        <div>
          <div className="text-lg font-semibold text-white">
            WebRTC unavailable in this browser
          </div>
          <p className="mt-1 text-sm text-white/60">
            {lockdownLikely
              ? "Safari Lockdown Mode is enabled, which blocks WebRTC. Disable it for this site under Settings → Privacy & Security → Lockdown Mode → Configure for Safari, then reload."
              : "This browser doesn't expose RTCPeerConnection. Try the latest Chrome, Edge, Firefox, or Safari (with Lockdown Mode off)."}
          </p>
        </div>
        <button
          onClick={onCancel}
          className="inline-flex h-11 items-center justify-center rounded-lg border border-white/15 bg-transparent px-5 text-sm text-white/80 transition-colors hover:bg-white/10"
        >
          Back to calls
        </button>
      </div>
    );
  }
  const headline = !live
    ? "This meeting has ended."
    : alreadyInRoom
      ? "Reconnect to media?"
      : "Ready to join?";
  const subline = !live
    ? "The host ended this meeting. You can review recent calls or start a new one."
    : alreadyInRoom
      ? "You're listed in this room but media isn't connected. Reconnect to see and hear participants."
      : "We'll ask for your microphone and camera. You can mute before others see you.";
  return (
    <div className="mx-auto flex h-full max-w-md flex-col items-center justify-center gap-4 text-center">
      <div className="grid h-16 w-16 place-items-center rounded-2xl bg-white/5">
        <Radio className="h-7 w-7 text-white/70" />
      </div>
      <div>
        <div className="text-lg font-semibold text-white">{headline}</div>
        <p className="mt-1 text-sm text-white/60">{subline}</p>
      </div>
      <div className="mt-2 flex items-center gap-2">
        {live ? (
          <button
            onClick={onJoin}
            disabled={joining}
            className="inline-flex h-11 items-center justify-center gap-2 rounded-lg bg-accent px-5 text-sm font-medium text-white shadow-sm transition-colors hover:bg-accent-hover disabled:cursor-not-allowed disabled:bg-accent/40"
          >
            {joining ? (
              <span className="h-4 w-4 animate-spin rounded-full border-2 border-white/60 border-t-transparent" />
            ) : (
              <Video className="h-4 w-4" />
            )}
            {joining ? "Connecting…" : "Join meeting"}
          </button>
        ) : null}
        <button
          onClick={onCancel}
          className="inline-flex h-11 items-center justify-center rounded-lg border border-white/15 bg-transparent px-5 text-sm text-white/80 transition-colors hover:bg-white/10"
        >
          {live ? "Cancel" : "Back to calls"}
        </button>
      </div>
    </div>
  );
}

function EmptyStage({ live }: { live: boolean }) {
  return (
    <div className="mx-auto flex h-full max-w-md flex-col items-center justify-center gap-2 text-center">
      <div className="grid h-14 w-14 place-items-center rounded-xl bg-white/5">
        <Users className="h-6 w-6 text-white/50" />
      </div>
      <div className="text-sm font-medium text-white">
        {live ? "You're first in." : "This meeting has ended."}
      </div>
      <p className="text-xs text-white/50">
        {live
          ? "Share the link and participants will appear here as they join."
          : "Participants have left the room."}
      </p>
    </div>
  );
}

// ── Participant tile ─────────────────────────────────────────────
function ParticipantTile({
  wsId,
  participant,
  stream,
  isSelf,
}: {
  wsId: string;
  participant: CallParticipant;
  stream: MediaStream | null;
  isSelf: boolean;
}) {
  const user = useMembers((s) => s.get(wsId, participant.user_id));
  const name = user?.display_name ?? shortId(participant.user_id);
  const videoRef = useRef<HTMLVideoElement | null>(null);

  // Bind stream to the <video> element imperatively — srcObject can't be
  // set declaratively in React. We also call play() explicitly because
  // Chrome can leave the element paused after the previous stream's only
  // video track was stopped (e.g., camera off → on cycle): autoPlay only
  // fires on initial bind, not when srcObject is re-set on the same node.
  useEffect(() => {
    const el = videoRef.current;
    if (!el) return;
    if (el.srcObject !== stream) {
      el.srcObject = stream;
    }
    if (stream) {
      el.play().catch(() => {
        /* fine: play() rejects if the element was paused mid-bind, the
         * next user gesture or the next stream change will resume it */
      });
    }
  }, [stream]);

  // hasVideoTrack drives the avatar-vs-video swap. We accept tracks that
  // haven't reached "live" yet ("new" is the brief readyState immediately
  // after replaceTrack) — gating on "live" only would render the avatar
  // for the first paint after camera-on and then never re-render because
  // no React state changes once the track flips to "live".
  const hasVideoTrack =
    (stream?.getVideoTracks().some(
      (t) => t.enabled && t.readyState !== "ended",
    ) ?? false) && !participant.video_muted;

  const roleLabel =
    participant.role === "host"
      ? "Host"
      : participant.role === "co_host"
        ? "Co-host"
        : participant.role === "presenter"
          ? "Presenter"
          : null;

  return (
    <div className="relative aspect-video overflow-hidden rounded-xl border border-white/5 bg-sidebar">
      {hasVideoTrack ? (
        // key on stream.id forces a fresh <video> node whenever the
        // engine swaps the local MediaStream (camera off→on, screen
        // share start/stop). React reuses the node by default and Chrome
        // sometimes refuses to repaint after the only video track was
        // stopped + re-added on the same element.
        <video
          key={stream?.id ?? "no-stream"}
          ref={videoRef}
          autoPlay
          playsInline
          muted={isSelf}
          className="absolute inset-0 h-full w-full object-cover"
        />
      ) : (
        <div className="absolute inset-0 grid place-items-center">
          <Avatar name={name} src={user?.avatar_url ?? null} color={user?.avatar_color} size={80} />
        </div>
      )}

      {/* Top-left role badge */}
      {roleLabel ? (
        <div className="absolute left-2 top-2 rounded-md bg-black/50 px-2 py-0.5 text-[10px] font-medium uppercase tracking-wide text-white/80 backdrop-blur">
          {roleLabel}
        </div>
      ) : null}

      {/* Bottom: name + media badges */}
      <div className="absolute inset-x-2 bottom-2 flex items-center gap-2">
        <span className="inline-flex min-w-0 items-center gap-1.5 rounded-md bg-black/55 px-2 py-0.5 text-xs text-white backdrop-blur">
          {participant.audio_muted ? (
            <MicOff className="h-3 w-3 text-status-red" />
          ) : null}
          <span className="truncate">{name}</span>
          {isSelf ? <span className="text-white/50">(you)</span> : null}
        </span>
        <div className="ml-auto flex gap-1">
          {participant.video_muted ? (
            <span className="grid h-6 w-6 place-items-center rounded-full bg-black/55 text-white/80 backdrop-blur">
              <VideoOff className="h-3 w-3" />
            </span>
          ) : null}
          {participant.screen_sharing ? (
            <span className="grid h-6 w-6 place-items-center rounded-full bg-accent text-white">
              <MonitorUp className="h-3 w-3" />
            </span>
          ) : null}
        </div>
      </div>
    </div>
  );
}

// ── Control bar button ───────────────────────────────────────────
function CircleButton({
  active,
  onIcon,
  offIcon,
  label,
  onClick,
  tone = "neutral",
  dangerWhenOff = false,
}: {
  active: boolean;
  onIcon: React.ReactNode;
  offIcon: React.ReactNode;
  label: string;
  onClick: () => void;
  tone?: "neutral" | "accent";
  // When the feature being off is *itself* a problem (mic muted, video
  // off), flag it red so the user notices. For opt-in features like
  // screen-share, "off" is just the default — keep the pill neutral.
  dangerWhenOff?: boolean;
}) {
  const off = !active;
  const dangerStyle = "bg-status-red text-white hover:bg-status-red/90";
  const accentStyle = "bg-accent text-white hover:bg-accent-hover";
  const neutralStyle = "bg-white/10 text-white hover:bg-white/15";
  const style = off
    ? dangerWhenOff
      ? dangerStyle
      : neutralStyle
    : tone === "accent"
      ? accentStyle
      : neutralStyle;
  return (
    <button
      onClick={onClick}
      title={label}
      aria-label={label}
      className={cn(
        "inline-flex h-12 w-12 items-center justify-center rounded-full transition-colors",
        style,
      )}
    >
      {active ? onIcon : offIcon}
    </button>
  );
}

// ── Recording controls ──────────────────────────────────────────
// The control-bar pill: red-pulsing "Recording" when active, neutral
// "Record" when idle. We keep it as its own component so the disabled
// state (call settings forbid recording) carries the right tooltip
// without forcing the parent to manage hover state.
function RecordButton({
  recording,
  busy,
  disabled,
  disabledReason,
  onClick,
}: {
  recording: boolean;
  busy: boolean;
  disabled?: boolean;
  disabledReason?: string;
  onClick: () => void;
}) {
  const label = recording ? "Stop recording" : "Record";
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled || busy}
      title={disabled && disabledReason ? disabledReason : label}
      aria-label={label}
      className={cn(
        "inline-flex h-10 items-center gap-2 rounded-full border px-4 text-sm transition-colors",
        recording
          ? "border-status-red/40 bg-status-red/15 text-status-red hover:bg-status-red/25"
          : "border-white/15 bg-transparent text-white/80 hover:bg-white/10",
        (disabled || busy) && "cursor-not-allowed opacity-50 hover:bg-transparent",
      )}
    >
      {recording ? (
        <Disc className="h-4 w-4 animate-pulse" />
      ) : (
        <Circle className="h-4 w-4 fill-status-red text-status-red" />
      )}
      {busy ? "…" : label}
    </button>
  );
}

// ── Recordings overlay panel ────────────────────────────────────
// Slides in from the right edge of the stage. Each recording row
// expands to its artifacts on click; ready-and-downloadable artifacts
// get a Download button that streams via the page's downloadArtifact()
// (bearer token attached, blob piped to disk). Keeps `processing`
// recordings non-actionable but visible so the host knows their stop
// click is being worked on.
function RecordingsPanel({
  recordings,
  wsId,
  callId,
  onClose,
  onDownload,
  onRefresh,
}: {
  recordings: Recording[];
  wsId: UUID;
  callId: UUID;
  onClose: () => void;
  onDownload: (rec: Recording, art: RecordingArtifact) => void | Promise<void>;
  onRefresh: () => void | Promise<void>;
}) {
  const [openId, setOpenId] = useState<UUID | null>(
    recordings[0]?.id ?? null,
  );
  const [artifacts, setArtifacts] = useState<Record<UUID, RecordingArtifact[]>>({});
  const [loadingId, setLoadingId] = useState<UUID | null>(null);

  const loadArtifacts = useCallback(
    async (recId: UUID) => {
      if (artifacts[recId]) return;
      setLoadingId(recId);
      try {
        const list = await recordingsApi.listArtifacts(wsId, callId, recId);
        setArtifacts((p) => ({ ...p, [recId]: list ?? [] }));
      } finally {
        setLoadingId(null);
      }
    },
    [artifacts, wsId, callId],
  );

  useEffect(() => {
    if (openId) void loadArtifacts(openId);
  }, [openId, loadArtifacts]);

  return (
    <aside className="absolute inset-y-0 right-0 z-10 flex w-full max-w-sm flex-col border-l border-white/5 bg-sidebar shadow-xl">
      <header className="flex h-[44px] items-center gap-2 border-b border-white/5 px-4">
        <FileVideo className="h-4 w-4 text-white/60" />
        <div className="text-sm font-semibold text-white">Recordings</div>
        <span className="text-[11px] text-white/40">
          {recordings.length}
        </span>
        <button
          onClick={onRefresh}
          className="ml-auto rounded-md px-2 py-0.5 text-[11px] text-white/60 transition-colors hover:bg-white/5"
        >
          Refresh
        </button>
        <button
          onClick={onClose}
          className="rounded-md p-1 text-white/60 transition-colors hover:bg-white/5"
          aria-label="Close recordings"
        >
          <X className="h-3.5 w-3.5" />
        </button>
      </header>
      <div className="flex-1 overflow-auto">
        {recordings.length === 0 ? (
          <div className="p-6 text-center text-[12px] text-white/50">
            No recordings yet. Start one with the Record button.
          </div>
        ) : (
          <ul className="divide-y divide-white/5">
            {recordings.map((rec) => {
              const isOpen = openId === rec.id;
              const arts = artifacts[rec.id] ?? [];
              return (
                <li key={rec.id}>
                  <button
                    onClick={() =>
                      setOpenId((prev) => (prev === rec.id ? null : rec.id))
                    }
                    className={cn(
                      "flex w-full items-center gap-2 px-4 py-3 text-left transition-colors hover:bg-white/5",
                      isOpen && "bg-white/5",
                    )}
                  >
                    <RecordingStatusDot status={rec.status} />
                    <div className="min-w-0 flex-1">
                      <div className="text-[13px] font-medium text-white">
                        {formatRecordingTitle(rec)}
                      </div>
                      <div className="mt-0.5 text-[11px] text-white/40">
                        {humanRecordingMeta(rec)}
                      </div>
                    </div>
                  </button>
                  {isOpen ? (
                    <div className="space-y-1 px-4 pb-3">
                      {loadingId === rec.id ? (
                        <div className="text-[11px] text-white/40">Loading artifacts…</div>
                      ) : arts.length === 0 ? (
                        <div className="text-[11px] text-white/40">
                          {rec.status === "recording"
                            ? "Capture is in progress — artifacts will appear after stop."
                            : rec.status === "processing"
                              ? "Composing artifacts…"
                              : "No artifacts available."}
                        </div>
                      ) : (
                        arts.map((a) => (
                          <div
                            key={a.id}
                            className="flex items-center gap-2 rounded-md bg-white/5 px-2 py-1.5"
                          >
                            <span className="text-[11px] uppercase tracking-wide text-white/50">
                              {a.kind.replace(/_/g, " ")}
                            </span>
                            <span className="ml-auto text-[10px] text-white/40">
                              {humanBytes(a.file_size)}
                            </span>
                            <button
                              onClick={() => onDownload(rec, a)}
                              disabled={!a.downloadable}
                              className={cn(
                                "rounded-md p-1 transition-colors",
                                a.downloadable
                                  ? "text-accent hover:bg-accent/15"
                                  : "cursor-not-allowed text-white/30",
                              )}
                              title={
                                a.downloadable
                                  ? "Download"
                                  : "Not downloadable yet"
                              }
                            >
                              <Download className="h-3.5 w-3.5" />
                            </button>
                          </div>
                        ))
                      )}
                    </div>
                  ) : null}
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </aside>
  );
}

function RecordingStatusDot({ status }: { status: Recording["status"] }) {
  const tint =
    status === "recording"
      ? "bg-status-red"
      : status === "processing"
        ? "bg-status-yellow"
        : status === "ready"
          ? "bg-status-green"
          : "bg-white/30";
  return (
    <span className="relative flex h-2 w-2 shrink-0">
      {status === "recording" ? (
        <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-status-red opacity-60" />
      ) : null}
      <span className={cn("relative inline-flex h-2 w-2 rounded-full", tint)} />
    </span>
  );
}

function formatRecordingTitle(rec: Recording): string {
  const t = new Date(rec.started_at);
  const sameDay = isSameDay(t, new Date());
  const time = t.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  if (sameDay) return `Today, ${time}`;
  return `${t.toLocaleDateString()} · ${time}`;
}

function humanRecordingMeta(rec: Recording): string {
  const parts: string[] = [statusLabel(rec.status)];
  if (rec.duration) parts.push(`${formatDuration(rec.duration)}`);
  if (rec.file_size) parts.push(humanBytes(rec.file_size));
  return parts.join(" · ");
}

function statusLabel(status: Recording["status"]): string {
  switch (status) {
    case "recording":
      return "Recording";
    case "processing":
      return "Processing";
    case "ready":
      return "Ready";
    case "failed":
      return "Failed";
  }
}

function formatDuration(seconds: number): string {
  const m = Math.floor(seconds / 60);
  const s = Math.floor(seconds % 60);
  return m > 0 ? `${m}m ${s}s` : `${s}s`;
}

function humanBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB"];
  let size = bytes / 1024;
  let i = 0;
  while (size >= 1024 && i < units.length - 1) {
    size /= 1024;
    i += 1;
  }
  return `${size.toFixed(size >= 10 ? 0 : 1)} ${units[i]}`;
}

function isSameDay(a: Date, b: Date): boolean {
  return (
    a.getFullYear() === b.getFullYear() &&
    a.getMonth() === b.getMonth() &&
    a.getDate() === b.getDate()
  );
}

function filenameForArtifact(rec: Recording, art: RecordingArtifact): string {
  const stamp = new Date(rec.started_at)
    .toISOString()
    .replace(/[:.]/g, "-")
    .slice(0, 19);
  const ext = inferExtension(art);
  return `recording-${stamp}-${art.kind}.${ext}`;
}

function inferExtension(art: RecordingArtifact): string {
  if (art.format) return art.format.toLowerCase();
  if (art.mime_type?.includes("webm")) return "webm";
  if (art.mime_type?.includes("mp4")) return "mp4";
  return "bin";
}

// ── Breakouts overlay panel ─────────────────────────────────────
// Slides in from the right edge of the stage just like RecordingsPanel.
// The panel has three modes:
//   - Empty (no rooms yet) — host sees "Create rooms" form, others see
//     a polite "no breakouts yet" placeholder.
//   - List (rooms exist) — every participant sees a Join button per
//     active room and a Return-to-main button at the top; the host
//     additionally sees a per-room Close button and a "Close all" /
//     "Broadcast" pair pinned to the footer.
//   - Inactive call — disabled buttons with a "active call required"
//     hint, matching the backend's contract.
function BreakoutsPanel({
  rooms,
  isHost,
  busy,
  callIsActive,
  onClose,
  onRefresh,
  onCreate,
  onJoin,
  onReturn,
  onCloseRoom,
  onCloseAll,
  onBroadcast,
}: {
  rooms: BreakoutRoom[];
  isHost: boolean;
  busy: boolean;
  callIsActive: boolean;
  onClose: () => void;
  onRefresh: () => void | Promise<void>;
  onCreate: (rooms: Array<{ name: string; time_limit?: number }>) => void | Promise<void>;
  onJoin: (roomId: UUID) => void | Promise<void>;
  onReturn: () => void | Promise<void>;
  onCloseRoom: (roomId: UUID) => void | Promise<void>;
  onCloseAll: () => void | Promise<void>;
  onBroadcast: (message: string) => void | Promise<void>;
}) {
  const active = rooms.filter((r) => r.status === "active");
  const closed = rooms.filter((r) => r.status === "closed");
  const [showCreate, setShowCreate] = useState(false);
  const [draft, setDraft] = useState<string[]>(["Room 1", "Room 2"]);
  const [broadcast, setBroadcast] = useState("");

  return (
    <aside className="absolute inset-y-0 right-0 z-10 flex w-full max-w-sm flex-col border-l border-white/5 bg-sidebar shadow-xl">
      <header className="flex h-[44px] items-center gap-2 border-b border-white/5 px-4">
        <Boxes className="h-4 w-4 text-white/60" />
        <div className="text-sm font-semibold text-white">Breakout rooms</div>
        <span className="text-[11px] text-white/40">{active.length}</span>
        <button
          onClick={onRefresh}
          className="ml-auto rounded-md px-2 py-0.5 text-[11px] text-white/60 transition-colors hover:bg-white/5"
        >
          Refresh
        </button>
        <button
          onClick={onClose}
          className="rounded-md p-1 text-white/60 transition-colors hover:bg-white/5"
          aria-label="Close breakouts"
        >
          <X className="h-3.5 w-3.5" />
        </button>
      </header>

      <div className="flex-1 overflow-auto">
        {!callIsActive ? (
          <div className="p-6 text-center text-[12px] text-white/50">
            Breakout rooms are available once the meeting is active.
          </div>
        ) : active.length === 0 && !showCreate ? (
          <div className="p-6 text-center">
            <div className="text-[12px] text-white/50">
              {isHost
                ? "No breakout rooms yet. Create some to split the meeting up."
                : "No breakout rooms yet. The host will set them up if needed."}
            </div>
            {isHost ? (
              <button
                onClick={() => setShowCreate(true)}
                disabled={busy}
                className="mt-4 inline-flex items-center gap-1.5 rounded-md bg-accent px-3 py-1.5 text-[12px] font-medium text-white transition-colors hover:bg-accent-hover disabled:opacity-50"
              >
                <Plus className="h-3.5 w-3.5" />
                Create rooms
              </button>
            ) : null}
          </div>
        ) : showCreate ? (
          <CreateBreakoutsForm
            initial={draft}
            busy={busy}
            onCancel={() => setShowCreate(false)}
            onSubmit={async (names) => {
              setDraft(names);
              await onCreate(names.map((n) => ({ name: n })));
              setShowCreate(false);
            }}
          />
        ) : (
          <ul className="divide-y divide-white/5">
            <li className="px-4 py-3">
              <button
                onClick={onReturn}
                disabled={busy}
                className="inline-flex items-center gap-1.5 rounded-md border border-white/10 bg-white/5 px-2.5 py-1.5 text-[12px] text-white/80 transition-colors hover:bg-white/10 disabled:opacity-50"
              >
                <ArrowLeft className="h-3.5 w-3.5" />
                Return to main room
              </button>
            </li>
            {active.map((r) => (
              <li key={r.id} className="flex items-center gap-2 px-4 py-3">
                <div className="min-w-0 flex-1">
                  <div className="truncate text-[13px] font-medium text-white">
                    {r.name}
                  </div>
                  {r.time_limit ? (
                    <div className="mt-0.5 text-[11px] text-white/40">
                      {Math.round(r.time_limit / 60)}m limit
                    </div>
                  ) : null}
                </div>
                <button
                  onClick={() => onJoin(r.id)}
                  disabled={busy}
                  className="rounded-md bg-accent/15 px-2.5 py-1 text-[11px] font-medium text-accent transition-colors hover:bg-accent/25 disabled:opacity-50"
                >
                  Join
                </button>
                {isHost ? (
                  <button
                    onClick={() => onCloseRoom(r.id)}
                    disabled={busy}
                    className="rounded-md p-1 text-white/40 transition-colors hover:bg-white/10 hover:text-status-red disabled:opacity-50"
                    title="Close room"
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </button>
                ) : null}
              </li>
            ))}
            {closed.length > 0 ? (
              <li className="px-4 py-2 text-[10px] uppercase tracking-wide text-white/30">
                Closed
              </li>
            ) : null}
            {closed.map((r) => (
              <li
                key={r.id}
                className="flex items-center gap-2 px-4 py-2 text-[12px] text-white/40"
              >
                <span className="truncate flex-1">{r.name}</span>
                <span>—</span>
              </li>
            ))}
          </ul>
        )}
      </div>

      {/* Host controls — pinned to the footer when at least one room exists */}
      {isHost && callIsActive && active.length > 0 && !showCreate ? (
        <footer className="space-y-2 border-t border-white/5 p-3">
          <div className="flex gap-2">
            <input
              value={broadcast}
              onChange={(e) => setBroadcast(e.target.value)}
              placeholder="Broadcast a message…"
              className="flex-1 rounded-md border border-white/10 bg-app/40 px-2 py-1.5 text-[12px] text-white placeholder:text-white/40 focus:border-accent focus:outline-none"
              onKeyDown={(e) => {
                if (e.key === "Enter" && broadcast.trim()) {
                  void onBroadcast(broadcast);
                  setBroadcast("");
                }
              }}
            />
            <button
              onClick={async () => {
                if (!broadcast.trim()) return;
                await onBroadcast(broadcast);
                setBroadcast("");
              }}
              disabled={busy || !broadcast.trim()}
              className="rounded-md bg-white/10 p-1.5 text-white/80 transition-colors hover:bg-white/15 disabled:opacity-50"
              title="Broadcast"
            >
              <Megaphone className="h-3.5 w-3.5" />
            </button>
          </div>
          <div className="flex gap-2">
            <button
              onClick={() => setShowCreate(true)}
              disabled={busy}
              className="flex-1 rounded-md border border-white/10 bg-white/5 px-2 py-1.5 text-[11px] text-white/80 transition-colors hover:bg-white/10 disabled:opacity-50"
            >
              + Add rooms
            </button>
            <button
              onClick={onCloseAll}
              disabled={busy}
              className="rounded-md border border-status-red/40 bg-status-red/10 px-2 py-1.5 text-[11px] text-status-red transition-colors hover:bg-status-red/20 disabled:opacity-50"
            >
              Close all
            </button>
          </div>
        </footer>
      ) : null}
    </aside>
  );
}

function CreateBreakoutsForm({
  initial,
  busy,
  onCancel,
  onSubmit,
}: {
  initial: string[];
  busy: boolean;
  onCancel: () => void;
  onSubmit: (names: string[]) => void | Promise<void>;
}) {
  const [names, setNames] = useState<string[]>(initial);
  const valid = names.every((n) => n.trim().length > 0) && names.length > 0;
  return (
    <form
      className="space-y-3 p-4"
      onSubmit={(e) => {
        e.preventDefault();
        if (valid) void onSubmit(names.map((n) => n.trim()));
      }}
    >
      <div className="text-[11px] uppercase tracking-wide text-white/50">
        Room names
      </div>
      <ul className="space-y-2">
        {names.map((n, i) => (
          <li key={i} className="flex items-center gap-2">
            <input
              value={n}
              onChange={(e) => {
                const next = names.slice();
                next[i] = e.target.value;
                setNames(next);
              }}
              autoFocus={i === names.length - 1}
              className="flex-1 rounded-md border border-white/10 bg-app/40 px-2 py-1.5 text-[12px] text-white placeholder:text-white/40 focus:border-accent focus:outline-none"
              placeholder={`Room ${i + 1}`}
            />
            {names.length > 1 ? (
              <button
                type="button"
                onClick={() =>
                  setNames(names.filter((_, idx) => idx !== i))
                }
                className="rounded-md p-1 text-white/40 transition-colors hover:bg-white/10 hover:text-status-red"
                title="Remove"
              >
                <X className="h-3 w-3" />
              </button>
            ) : null}
          </li>
        ))}
      </ul>
      <button
        type="button"
        onClick={() => setNames([...names, `Room ${names.length + 1}`])}
        className="text-[11px] text-accent transition-colors hover:text-accent-hover"
      >
        + Add another room
      </button>
      <div className="flex gap-2 pt-1">
        <button
          type="submit"
          disabled={!valid || busy}
          className="rounded-md bg-accent px-3 py-1.5 text-[12px] font-medium text-white transition-colors hover:bg-accent-hover disabled:opacity-50"
        >
          {busy ? "Creating…" : "Create"}
        </button>
        <button
          type="button"
          onClick={onCancel}
          disabled={busy}
          className="rounded-md border border-white/10 bg-transparent px-3 py-1.5 text-[12px] text-white/80 transition-colors hover:bg-white/10 disabled:opacity-50"
        >
          Cancel
        </button>
      </div>
    </form>
  );
}

// ── Engine connection state pip ──────────────────────────────────
function EngineStateBadge({ state }: { state: EngineConnectionState }) {
  const map: Record<
    EngineConnectionState,
    { label: string; tint: string; icon: React.ReactNode }
  > = {
    idle: { label: "Idle", tint: "text-white/50", icon: <WifiOff className="h-3 w-3" /> },
    "acquiring-media": {
      label: "Acquiring devices",
      tint: "text-white/60",
      icon: <WifiOff className="h-3 w-3" />,
    },
    signaling: {
      label: "Connecting",
      tint: "text-status-yellow",
      icon: <WifiOff className="h-3 w-3" />,
    },
    connected: {
      label: "Connected",
      tint: "text-status-green",
      icon: <Wifi className="h-3 w-3" />,
    },
    reconnecting: {
      label: "Reconnecting",
      tint: "text-status-yellow",
      icon: <WifiOff className="h-3 w-3" />,
    },
    closed: { label: "Closed", tint: "text-white/50", icon: <WifiOff className="h-3 w-3" /> },
    failed: { label: "Failed", tint: "text-status-red", icon: <WifiOff className="h-3 w-3" /> },
  };
  const info = map[state];
  return (
    <span className={cn("inline-flex items-center gap-1", info.tint)}>
      {info.icon}
      {info.label}
    </span>
  );
}
