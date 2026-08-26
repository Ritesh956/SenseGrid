"use client";

import { useEffect, useRef, useState } from "react";
import { backoffDelayMs } from "./backoff";

export type ConnectionState = "connecting" | "open" | "degraded" | "closed";

export interface WSFrame {
  type: "metric" | "alert" | "rollout";
  subject: string;
  payload: unknown;
  ts: number;
}

// useControlSocket owns the console's one live connection to cmd/control's
// GET /v1/ws (ws_handler.go) — connects directly browser-to-control, not
// through the Next.js BFF: WebSocket handshakes aren't subject to CORS,
// and proxying every live frame through a second hop would just add
// latency for nothing. Reconnects with exponential backoff (backoff.ts)
// and flips to "degraded" the instant the socket closes, not after
// backoff finishes reconnecting — that's what makes the P5 DoD ("kill the
// WS mid-session — the console visibly says so within a couple seconds")
// true on the client side. token is appended as ?token= since a browser
// WebSocket handshake can't carry a custom Authorization header (see
// ws_handler.go's doc comment for the same constraint server-side).
export function useControlSocket(
  baseUrl: string | null,
  token: string | undefined,
  onFrame: (frame: WSFrame) => void,
): ConnectionState {
  const [state, setState] = useState<ConnectionState>("connecting");
  const onFrameRef = useRef(onFrame);
  onFrameRef.current = onFrame;

  useEffect(() => {
    if (!baseUrl || !token) {
      setState("closed");
      return;
    }

    const url = `${baseUrl}?token=${encodeURIComponent(token)}`;
    let attempt = 0;
    let torndown = false;
    let socket: WebSocket | null = null;
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null;

    const connect = () => {
      setState((s) => (s === "open" ? s : "connecting"));
      socket = new WebSocket(url);

      socket.onopen = () => {
        attempt = 0;
        setState("open");
      };
      socket.onmessage = (event) => {
        try {
          const frame = JSON.parse(event.data as string) as WSFrame;
          onFrameRef.current(frame);
        } catch {
          // Malformed frame — drop it, not fatal to the connection.
        }
      };
      socket.onclose = () => {
        if (torndown) {
          return;
        }
        setState("degraded");
        const delay = backoffDelayMs(attempt);
        attempt += 1;
        reconnectTimer = setTimeout(connect, delay);
      };
      socket.onerror = () => {
        socket?.close();
      };
    };

    connect();

    return () => {
      torndown = true;
      if (reconnectTimer) {
        clearTimeout(reconnectTimer);
      }
      socket?.close();
      setState("closed");
    };
  }, [baseUrl, token]);

  return state;
}
