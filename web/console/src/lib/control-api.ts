import { NextResponse } from "next/server";
import { auth } from "@/auth";

// Server-side base URL for cmd/control — the Docker-internal hostname
// (https://control:8080), not the host-exposed port the browser uses for
// the WS connection (NEXT_PUBLIC_CONTROL_WS_URL — see
// use-control-socket.ts). Every src/app/api/** route handler proxies
// through here rather than the browser calling cmd/control directly, so
// cmd/control keeps its existing "no CORS to configure" property
// (CLAUDE.md) even though the console is a separate origin.
const CONTROL_API_URL = process.env.CONTROL_API_URL ?? "https://control:8080";

export class ControlApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function controlFetch(path: string, init?: RequestInit): Promise<Response> {
  const session = await auth();
  if (!session?.accessToken) {
    throw new ControlApiError(401, "not authenticated");
  }
  if (session.error) {
    throw new ControlApiError(401, "session expired");
  }
  return fetch(`${CONTROL_API_URL}${path}`, {
    ...init,
    headers: {
      ...init?.headers,
      Authorization: `Bearer ${session.accessToken}`,
      "Content-Type": "application/json",
    },
    cache: "no-store",
  });
}

// proxy forwards one request to cmd/control and passes its status/body
// straight back — every BFF route handler in src/app/api/** is a thin
// wrapper around this, since they all need exactly this shape (auth,
// forward, passthrough, no transformation).
export async function proxy(path: string, init?: RequestInit): Promise<NextResponse> {
  try {
    const res = await controlFetch(path, init);
    const body = await res.text();
    return new NextResponse(body, {
      status: res.status,
      headers: { "Content-Type": "application/json" },
    });
  } catch (err) {
    if (err instanceof ControlApiError) {
      return NextResponse.json({ error: err.message }, { status: err.status });
    }
    return NextResponse.json({ error: "internal error" }, { status: 500 });
  }
}

// proxyJSON is like proxy but decodes the response for callers that need
// to inspect or reshape it (e.g. api/devices/[id]/route.ts filtering the
// device list server-side, since cmd/control has no single-device GET).
export async function proxyJSON<T>(path: string, init?: RequestInit): Promise<{ status: number; body: T }> {
  const res = await controlFetch(path, init);
  const body = (await res.json()) as T;
  return { status: res.status, body };
}
