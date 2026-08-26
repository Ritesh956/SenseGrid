import { proxy } from "@/lib/control-api";

export async function POST(_req: Request, { params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  return proxy(`/v1/rollouts/${id}/pause`, { method: "POST" });
}
