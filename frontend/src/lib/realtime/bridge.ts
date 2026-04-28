// Single place that attaches the WS client to the Zustand stores.
//
// Why not do this inside each component? Multiple chat views mounted in
// parallel (e.g. channel + thread panel) would each register a listener.
// Keeping it central ensures the stores see every event exactly once, and
// makes the wiring easy to audit.

"use client";

import { useEffect } from "react";
import { rt } from "@/lib/ws/client";
import { WS, type ServerEvent } from "@/lib/ws/events";
import type { Channel } from "@/lib/types";
import { useMessages } from "@/stores/messages";
import { useWorkspace } from "@/stores/workspace";
import { useAuth } from "@/stores/auth";

export function useRealtimeBridge() {
  const userId = useAuth((s) => s.user?.id);

  useEffect(() => {
    const client = rt();
    const unsub = client.on((evt) => handleEvent(evt, userId));
    // Tick typing expirations every 1s so the indicator fades out.
    const tid = window.setInterval(() => useMessages.getState().clearStaleTyping(), 1000);
    return () => {
      unsub();
      window.clearInterval(tid);
    };
  }, [userId]);
}

function handleEvent(evt: ServerEvent, selfId: string | undefined) {
  // Route to messages store for chat-related events.
  useMessages.getState().applyEvent(evt);

  // Typing events: suppress self-typing noise (server echoes to all subscribers).
  if (evt.type === WS.TypingStarted) {
    const p = evt.payload as { channel_id: string; user_id: string } | undefined;
    if (p && selfId && p.user_id === selfId) {
      // Remove our own entry that applyEvent just inserted.
      const typing = useMessages.getState().typing;
      const arr = (typing[p.channel_id] ?? []).filter((t) => t.userId !== selfId);
      useMessages.setState({ typing: { ...typing, [p.channel_id]: arr } });
    }
  }

  // Channel lifecycle: update workspace store so the sidebar reflects new channels.
  if (evt.type === WS.ChannelCreated || evt.type === WS.ChannelUpdated) {
    const payload = evt.payload as { channel?: Channel } | undefined;
    if (payload?.channel && typeof payload.channel === "object" && "id" in payload.channel) {
      useWorkspace.getState().upsertChannel(payload.channel);
    }
  }
}
