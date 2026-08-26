"use client";

import { useSocketState } from "./SocketProvider";
import type { ConnectionState } from "@/lib/use-control-socket";

const LABEL: Record<ConnectionState, string> = {
  connecting: "Connecting…",
  open: "Live",
  degraded: "Reconnecting…",
  closed: "Disconnected",
};

const DOT: Record<ConnectionState, string> = {
  connecting: "bg-warn",
  open: "bg-accent",
  degraded: "bg-crit animate-pulse",
  closed: "bg-text-muted",
};

// The P5 DoD in one component: kill cmd/control's WS mid-session and this
// flips to "Reconnecting…" within a couple seconds (useControlSocket sets
// "degraded" the instant the socket closes, before backoff even starts).
export default function ConnectionBadge() {
  const state = useSocketState();
  return (
    <div className="flex items-center gap-2 rounded-full border border-border px-3 py-1 text-xs text-text-muted">
      <span className={`h-2 w-2 rounded-full ${DOT[state]}`} />
      {LABEL[state]}
    </div>
  );
}
