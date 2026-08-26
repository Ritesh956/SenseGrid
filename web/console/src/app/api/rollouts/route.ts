import type { NextRequest } from "next/server";
import { proxy } from "@/lib/control-api";

export async function GET() {
  return proxy("/v1/rollouts");
}

export async function POST(req: NextRequest) {
  const body = await req.text();
  return proxy("/v1/rollouts", { method: "POST", body });
}
