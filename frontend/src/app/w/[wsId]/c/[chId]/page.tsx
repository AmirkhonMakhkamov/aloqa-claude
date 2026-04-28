"use client";

import { useEffect, useState } from "react";
import { useParams, useRouter } from "next/navigation";
import { Hash, Info, Lock, Phone, Pin, User, Users } from "lucide-react";
import { Composer } from "@/components/chat/Composer";
import { MessageList } from "@/components/chat/MessageList";
import { ThreadPanel } from "@/components/chat/ThreadPanel";
import { TypingIndicator } from "@/components/chat/TypingIndicator";
import { Button } from "@/components/ui/Button";
import { channelsApi, callsApi } from "@/lib/api/endpoints";
import { rt } from "@/lib/ws/client";
import { rooms } from "@/lib/ws/events";
import type { Channel, Message } from "@/lib/types";
import { useWorkspace } from "@/stores/workspace";

export default function ChannelPage() {
  const router = useRouter();
  const params = useParams<{ wsId: string; chId: string }>();
  const wsId = params.wsId;
  const chId = params.chId;

  const channelsInStore = useWorkspace((s) => s.channels);
  const refreshUnread = useWorkspace((s) => s.refreshUnread);
  const [channel, setChannel] = useState<Channel | null>(
    channelsInStore.find((c) => c.id === chId) ?? null,
  );
  const [thread, setThread] = useState<Message | null>(null);
  const [showDetails, setShowDetails] = useState(false);
  const [starting, setStarting] = useState(false);

  // Keep local channel metadata fresh (topic/name may change).
  useEffect(() => {
    const fromStore = channelsInStore.find((c) => c.id === chId);
    if (fromStore) {
      setChannel(fromStore);
      return;
    }
    // Fallback fetch if we arrived via a deep link before the list loaded.
    channelsApi
      .get(wsId, chId)
      .then(setChannel)
      .catch(() => undefined);
  }, [wsId, chId, channelsInStore]);

  // Subscribe to the channel room for the lifetime of this page.
  useEffect(() => {
    const client = rt();
    client.start();
    const chatRoom = rooms.channel(chId);
    const typingRoom = rooms.typing(chId);
    client.subscribe(chatRoom);
    client.subscribe(typingRoom);
    return () => {
      client.unsubscribe(chatRoom);
      client.unsubscribe(typingRoom);
    };
  }, [chId]);

  // Mark channel read when it opens (and again if it becomes focused).
  useEffect(() => {
    let cancelled = false;
    const mark = () => {
      if (cancelled) return;
      channelsApi
        .markRead(wsId, chId)
        .then(() => refreshUnread())
        .catch(() => undefined);
    };
    mark();
    const onVis = () => {
      if (document.visibilityState === "visible") mark();
    };
    document.addEventListener("visibilitychange", onVis);
    return () => {
      cancelled = true;
      document.removeEventListener("visibilitychange", onVis);
    };
  }, [wsId, chId, refreshUnread]);

  // Close the thread panel when the channel changes.
  useEffect(() => {
    setThread(null);
  }, [chId]);

  async function startCall() {
    if (starting || !channel) return;
    setStarting(true);
    try {
      const call = await callsApi.start(wsId, {
        type: "group",
        title: `#${channel.name} meeting`,
        channel_id: channel.id,
      });
      router.push(`/w/${wsId}/calls/${call.id}`);
    } catch {
      // toast would go here once we add a toast system
    } finally {
      setStarting(false);
    }
  }

  if (!channel) {
    return (
      <div className="flex h-full items-center justify-center gap-2 text-sm text-ink-3">
        <span className="h-3.5 w-3.5 animate-spin rounded-full border-2 border-line border-t-accent" />
        Loading channel…
      </div>
    );
  }

  const Icon = channel.type === "private" ? Lock : channel.type === "dm" ? User : Hash;

  return (
    <div className="flex h-full">
      <div className="flex min-w-0 flex-1 flex-col">
        <ChannelHeader
          channel={channel}
          Icon={Icon}
          onToggleDetails={() => setShowDetails((v) => !v)}
          onStartCall={startCall}
          startingCall={starting}
        />
        <div className="min-h-0 flex-1">
          <MessageList wsId={wsId} chId={chId} onOpenThread={setThread} />
        </div>
        <TypingIndicator wsId={wsId} chId={chId} />
        <Composer
          wsId={wsId}
          chId={chId}
          placeholder={`Message #${channel.name}`}
        />
      </div>

      {thread ? (
        <ThreadPanel
          wsId={wsId}
          chId={chId}
          parent={thread}
          onClose={() => setThread(null)}
        />
      ) : null}

      {showDetails ? (
        <ChannelDetails channel={channel} onClose={() => setShowDetails(false)} />
      ) : null}
    </div>
  );
}

function ChannelHeader({
  channel,
  Icon,
  onToggleDetails,
  onStartCall,
  startingCall,
}: {
  channel: Channel;
  Icon: React.ComponentType<{ className?: string }>;
  onToggleDetails: () => void;
  onStartCall: () => void;
  startingCall: boolean;
}) {
  return (
    <header className="flex h-[52px] shrink-0 items-center gap-3 border-b border-line bg-app px-6">
      <Icon className="h-4 w-4 text-ink-3" />
      <div className="min-w-0">
        <div className="truncate text-[15px] font-semibold text-ink">
          {channel.name}
        </div>
        {channel.topic ? (
          <div className="truncate text-[12px] text-ink-3">{channel.topic}</div>
        ) : null}
      </div>
      <div className="ml-auto flex items-center gap-2">
        <Button size="sm" variant="outline" onClick={onStartCall} loading={startingCall}>
          <Phone className="h-3.5 w-3.5" /> Start meeting
        </Button>
        <button
          type="button"
          onClick={onToggleDetails}
          className="grid h-8 w-8 place-items-center rounded-md text-ink-2 transition hover:bg-app-2 hover:text-ink"
          aria-label="Channel details"
        >
          <Info className="h-4 w-4" />
        </button>
      </div>
    </header>
  );
}

function ChannelDetails({
  channel,
  onClose,
}: {
  channel: Channel;
  onClose: () => void;
}) {
  return (
    <aside className="flex h-full w-[320px] shrink-0 flex-col border-l border-line bg-app p-5 text-[13px] text-ink-2">
      <div className="mb-5 flex items-center justify-between">
        <h3 className="text-[15px] font-semibold text-ink">About #{channel.name}</h3>
        <button
          type="button"
          onClick={onClose}
          className="rounded-md px-2 py-1 text-[12px] text-ink-3 transition hover:bg-app-2 hover:text-ink"
        >
          Close
        </button>
      </div>
      <dl className="space-y-4">
        <div className="space-y-1">
          <dt className="text-[11px] font-semibold uppercase tracking-wider text-ink-2">Topic</dt>
          <dd className="text-ink">
            {channel.topic || <em className="text-ink-3">No topic set.</em>}
          </dd>
        </div>
        <div className="space-y-1">
          <dt className="text-[11px] font-semibold uppercase tracking-wider text-ink-2">Type</dt>
          <dd className="capitalize text-ink">{channel.type}</dd>
        </div>
        <div className="space-y-1">
          <dt className="text-[11px] font-semibold uppercase tracking-wider text-ink-2">Created</dt>
          <dd className="text-ink">{new Date(channel.created_at).toLocaleString()}</dd>
        </div>
      </dl>

      <div className="mt-6 space-y-2">
        <div className="inline-flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-ink-2">
          <Users className="h-3 w-3" /> Members
        </div>
        <p className="text-[12px] text-ink-3">
          Member listing lives on the Admin page for now.
        </p>
      </div>

      <div className="mt-6 space-y-2">
        <div className="inline-flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-ink-2">
          <Pin className="h-3 w-3" /> Pinned
        </div>
        <p className="text-[12px] text-ink-3">
          Pin messages from the hover toolbar; they&apos;ll show up here in a future pass.
        </p>
      </div>
    </aside>
  );
}
