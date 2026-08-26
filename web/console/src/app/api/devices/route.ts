import { proxy } from "@/lib/control-api";

export async function GET() {
  return proxy("/v1/devices");
}
