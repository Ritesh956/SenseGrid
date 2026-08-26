import { NextResponse } from "next/server";
import { ControlApiError, proxyJSON } from "@/lib/control-api";
import type { Device } from "@/lib/types";

// cmd/control has no GET /v1/devices/{id} — at this fleet's scale, filtering
// the (small) device list server-side here is the right amount of
// complexity, not a new Go endpoint (see the Phase 5 plan's scope notes).
export async function GET(_req: Request, { params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  try {
    const { status, body } = await proxyJSON<{ devices: Device[] }>("/v1/devices");
    if (status !== 200) {
      return NextResponse.json(body, { status });
    }
    const device = body.devices.find((d) => d.id === id);
    if (!device) {
      return NextResponse.json({ error: "device not found" }, { status: 404 });
    }
    return NextResponse.json(device);
  } catch (err) {
    if (err instanceof ControlApiError) {
      return NextResponse.json({ error: err.message }, { status: err.status });
    }
    return NextResponse.json({ error: "internal error" }, { status: 500 });
  }
}
