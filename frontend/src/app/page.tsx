"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";
import { useAuth } from "@/stores/auth";

export default function Home() {
  const router = useRouter();
  const user = useAuth((s) => s.user);
  const loading = useAuth((s) => s.loading);

  useEffect(() => {
    if (loading) return;
    router.replace(user ? "/w" : "/login");
  }, [loading, user, router]);

  return (
    <div className="flex h-full items-center justify-center gap-2 text-sm text-ink-3">
      <span className="h-3.5 w-3.5 animate-spin rounded-full border-2 border-line border-t-accent" />
      Loading…
    </div>
  );
}
