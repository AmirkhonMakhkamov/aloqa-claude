"use client";

import { useEffect } from "react";
import { useParams, useRouter } from "next/navigation";
import { AppShell } from "@/components/shell/AppShell";
import { useRealtimeBridge } from "@/lib/realtime/bridge";
import { rt } from "@/lib/ws/client";
import { rooms } from "@/lib/ws/events";
import { useAuth } from "@/stores/auth";
import { useMembers } from "@/stores/members";
import { useWorkspace } from "@/stores/workspace";

/*
 * Workspace shell: authenticates the user, keeps stores in sync with the
 * URL-selected workspace, wires WS subscriptions, and renders the AppShell
 * (rail + contextual sidebar + main).
 */
export default function WorkspaceLayout({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const params = useParams<{ wsId: string }>();
  const wsId = params.wsId;

  const user = useAuth((s) => s.user);
  const loading = useAuth((s) => s.loading);
  const activeId = useWorkspace((s) => s.activeId);
  const setActive = useWorkspace((s) => s.setActive);
  const loadWorkspaces = useWorkspace((s) => s.loadWorkspaces);
  const ensureMembers = useMembers((s) => s.ensureLoaded);

  // Attach WS events → stores (messages, typing, channel upserts).
  useRealtimeBridge();

  // Redirect if not authenticated.
  useEffect(() => {
    if (!loading && !user) router.replace("/login");
  }, [loading, user, router]);

  // Ensure the store matches the URL workspace.
  useEffect(() => {
    if (!wsId) return;
    if (activeId !== wsId) {
      void loadWorkspaces().then(() => setActive(wsId));
    }
  }, [wsId, activeId, loadWorkspaces, setActive]);

  // Best-effort roster fetch for name/avatar lookups.
  useEffect(() => {
    if (wsId && user) void ensureMembers(wsId);
  }, [wsId, user, ensureMembers]);

  // Open the WebSocket once the user is known, subscribe to this workspace.
  useEffect(() => {
    if (!user || !wsId) return;
    const client = rt();
    client.start();
    const room = rooms.workspace(wsId);
    client.subscribe(room);
    return () => {
      client.unsubscribe(room);
    };
  }, [user, wsId]);

  if (loading || !user) {
    return (
      <div className="flex h-full items-center justify-center text-ink-3">
        Loading…
      </div>
    );
  }

  return <AppShell>{children}</AppShell>;
}
