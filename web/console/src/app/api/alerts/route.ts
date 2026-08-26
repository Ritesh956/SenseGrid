import type { NextRequest } from "next/server";
import { proxy } from "@/lib/control-api";

export async function GET(req: NextRequest) {
  return proxy(`/v1/alerts${req.nextUrl.search}`);
}
