"use client";

import Link from "next/link";
import useSWR from "swr";
import { fetcher } from "@/lib/fetcher";
import type { Rollout } from "@/lib/types";

const STATE_COLOR: Record<string, string> = {
  running: "text-accent",
  paused: "text-warn",
  completed: "text-text-muted",
  aborted: "text-crit",
};

export default function RolloutsPage() {
  const { data, isLoading } = useSWR<{ rollouts: Rollout[] }>("/api/rollouts", fetcher, {
    refreshInterval: 5000,
  });

  return (
    <div>
      <h1 className="mb-6 text-xl font-semibold">Rollouts</h1>
      {isLoading && <p className="text-text-muted">Loading…</p>}
      {data && data.rollouts.length === 0 && <p className="text-text-muted">No rollouts yet.</p>}
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
        {data?.rollouts.map((r) => {
          const stage = r.stages[r.current_stage_index];
          return (
            <Link
              key={r.id}
              href={`/rollouts/${r.id}`}
              className="rounded-lg border border-border p-4 hover:border-accent"
            >
              <div className="mb-2 flex items-center justify-between">
                <h3 className="font-medium">{r.name}</h3>
                <span className={`text-xs uppercase ${STATE_COLOR[r.state] ?? ""}`}>{r.state}</span>
              </div>
              <p className="mb-2 text-sm text-text-muted">
                Stage {r.current_stage_index + 1}/{r.stages.length}
                {stage ? ` — ${stage.percent}% cohort` : ""}
              </p>
              <div className="h-1.5 w-full overflow-hidden rounded-full bg-surface-2">
                <div className="h-full bg-accent" style={{ width: `${stage ? stage.percent : 100}%` }} />
              </div>
            </Link>
          );
        })}
      </div>
    </div>
  );
}
