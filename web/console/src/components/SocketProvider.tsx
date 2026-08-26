"use client";

import { createContext, useCallback, useContext, useEffect, useRef } from "react";
import { useControlSocket, type ConnectionState, type WSFrame } from "@/lib/use-control-socket";

type Listener = (frame: WSFrame) => void;

interface SocketContextValue {
  state: ConnectionState;
  subscribe: (listener: Listener) => () => void;
  role: string;
}

const SocketContext = createContext<SocketContextValue | null>(null);

const WS_URL = process.env.NEXT_PUBLIC_CONTROL_WS_URL || null;

// One WS connection for the whole console session, held at the shell
// level (see ConsoleShell) — pages subscribe to it via useSocketFrames
// instead of each opening their own socket, since cmd/control's WS
// handler is deliberately one-subscription-set-per-connection (see
// ws_handler.go) and there's no reason for e.g. the fleet page and a
// device-detail page to duplicate that. Also carries the session's role
// down to pages for UI-level gating (useConsoleRole) — the real
// enforcement is always cmd/control's requireRole on the actual write.
export function SocketProvider({
  accessToken,
  role,
  children,
}: {
  accessToken?: string;
  role: string;
  children: React.ReactNode;
}) {
  const listenersRef = useRef<Set<Listener>>(new Set());

  const onFrame = useCallback((frame: WSFrame) => {
    for (const listener of listenersRef.current) {
      listener(frame);
    }
  }, []);

  const state = useControlSocket(WS_URL, accessToken, onFrame);

  const subscribe = useCallback((listener: Listener) => {
    listenersRef.current.add(listener);
    return () => {
      listenersRef.current.delete(listener);
    };
  }, []);

  return <SocketContext.Provider value={{ state, subscribe, role }}>{children}</SocketContext.Provider>;
}

export function useSocketState(): ConnectionState {
  const ctx = useContext(SocketContext);
  return ctx?.state ?? "closed";
}

export function useConsoleRole(): string {
  const ctx = useContext(SocketContext);
  return ctx?.role ?? "viewer";
}

export function useSocketFrames(listener: Listener) {
  const ctx = useContext(SocketContext);
  const listenerRef = useRef(listener);
  listenerRef.current = listener;

  useEffect(() => {
    if (!ctx) {
      return;
    }
    return ctx.subscribe((frame) => listenerRef.current(frame));
  }, [ctx]);
}
