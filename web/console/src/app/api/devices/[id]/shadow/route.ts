import type { NextRequest } from "next/server";
import { proxy } from "@/lib/control-api";

export async function GET(_req: NextRequest, { params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  return proxy(`/v1/devices/${id}/shadow`);
}

// PUT here targets .../shadow/desired on the Go side — admin-only,
// enforced by cmd/control's requireRole, not by this proxy.
export async function PUT(req: NextRequest, { params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  const body = await req.text();
  return proxy(`/v1/devices/${id}/shadow/desired`, { method: "PUT", body });
}
