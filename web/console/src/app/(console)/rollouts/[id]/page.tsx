"use client";

import { useParams } from "next/navigation";
import { useCallback, useState } from "react";
import useSWR from "swr";
import { fetcher } from "@/lib/fetcher";
import { useSocketFrames, useConsoleRole } from "@/components/SocketProvider";
import type { WSFrame } from "@/lib/use-control-socket";
import { roleAtLeast } from "@/lib/roles";
import type { Rollout } from "@/lib/types";

const STATE_COLOR: Record<string, string> = {
  running: "text-accent",
  paused: "text-warn",
  completed: "text-text-muted",
  aborted: "text-crit",
};

export default function RolloutDetailPage() {
  const params = useParams<{ id: string }>();
  const role = useConsoleRole();
  const canAbort = roleAtLeast(role, "admin");
  const { data: rollout, mutate } = useSWR<Rollout>(`/api/rollouts/${params.id}`, fetcher, {
    refreshInterval: 5000,
  });
  const [acting, setActing] = useState(false);

  const onFrame = useCallback(
    (frame: WSFrame) => {
      if (frame.type !== "rollout") {
        return;
      }
      const evt = frame.payload as { rollout_id?: string };
      if (evt.rollout_id === params.id) {
        mutate();
      }
    },
    [params.id, mutate],
  );
  useSocketFrames(onFrame);

  async function onAbort() {
    if (!confirm("Abort this rollout? This can't be undone.")) {
      return;
    }
    setActing(true);
    try {
      const res = await fetch(`/api/rollouts/${params.id}/abort`, { method: "POST" });
      if (res.ok) {
        await mutate();
      }
    } finally {
      setActing(false);
    }
  }

  if (!rollout) {
    return <p className="text-text-muted">Loading…</p>;
  }

  return (
    <div>
      <div className="mb-6 flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold">{rollout.name}</h1>
          <p className={`text-sm uppercase ${STATE_COLOR[rollout.state] ?? ""}`}>{rollout.state}</p>
        </div>
        {canAbort && rollout.state === "running" && (
          <button
            onClick={onAbort}
            disabled={acting}
            className="rounded border border-crit px-3 py-1.5 text-sm text-crit hover:bg-crit/10 disabled:opacity-50"
          >
            Abort rollout
          </button>
        )}
      </div>

      <section className="mb-6 rounded-lg border border-border p-4">
        <h2 className="mb-3 text-sm font-medium text-text-muted">Stages</h2>
        <div className="space-y-2">
          {rollout.stages.map((s, i) => {
            const active = i === rollout.current_stage_index;
            const done = i < rollout.current_stage_index;
            return (
              <div key={i} className="flex items-center gap-3 text-sm">
                <span className={`w-20 shrink-0 ${active ? "font-medium text-accent" : "text-text-muted"}`}>
                  Stage {i + 1}
                </span>
                <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-surface-2">
                  <div
                    className={`h-full ${done ? "bg-text-muted" : active ? "bg-accent" : ""}`}
                    style={{ width: done || active ? `${s.percent}%` : "0%" }}
                  />
                </div>
                <span className="w-32 shrink-0 text-right text-text-muted">
                  {s.percent}% · {s.bake_seconds}s bake
                </span>
              </div>
            );
          })}
        </div>
      </section>

      <section className="mb-6 rounded-lg border border-border p-4">
        <h2 className="mb-3 text-sm font-medium text-text-muted">Health criteria</h2>
        <dl className="grid grid-cols-3 gap-4 text-sm">
          <div>
            <dt className="text-text-muted">Max error rate</dt>
            <dd>{(rollout.health_criteria.max_error_rate * 100).toFixed(1)}%</dd>
          </div>
          <div>
            <dt className="text-text-muted">Max disconnect rate</dt>
            <dd>{(rollout.health_criteria.max_disconnect_rate * 100).toFixed(1)}%</dd>
          </div>
          <div>
            <dt className="text-text-muted">Max rejection rate</dt>
            <dd>{(rollout.health_criteria.max_rejection_rate * 100).toFixed(1)}%</dd>
          </div>
        </dl>
      </section>

      <section className="rounded-lg border border-border p-4">
        <h2 className="mb-3 text-sm font-medium text-text-muted">Cohort</h2>
        <p className="text-sm text-text-muted">
          {rollout.cohort.device_type
            ? `device type: ${rollout.cohort.device_type}`
            : `${rollout.cohort.device_ids?.length ?? 0} explicit device(s)`}
        </p>
      </section>
    </div>
  );
}
