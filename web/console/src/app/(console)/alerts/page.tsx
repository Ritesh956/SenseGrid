"use client";

import { useCallback, useState } from "react";
import useSWR from "swr";
import { fetcher } from "@/lib/fetcher";
import { useSocketFrames, useConsoleRole } from "@/components/SocketProvider";
import type { WSFrame } from "@/lib/use-control-socket";
import { roleAtLeast } from "@/lib/roles";
import type { Alert, AlertState } from "@/lib/types";

const FILTERS: { label: string; value: AlertState | "" }[] = [
  { label: "Firing", value: "firing" },
  { label: "Acknowledged", value: "acknowledged" },
  { label: "Resolved", value: "resolved" },
  { label: "All", value: "" },
];

const SEVERITY_COLOR: Record<string, string> = {
  critical: "text-crit",
  warning: "text-warn",
};

export default function AlertsPage() {
  const [filter, setFilter] = useState<AlertState | "">("firing");
  const role = useConsoleRole();
  const canOperate = roleAtLeast(role, "operator");

  const { data, mutate, isLoading } = useSWR<{ alerts: Alert[] }>(
    `/api/alerts${filter ? `?state=${filter}` : ""}`,
    fetcher,
  );

  const onFrame = useCallback(
    (frame: WSFrame) => {
      if (frame.type === "alert") {
        mutate();
      }
    },
    [mutate],
  );
  useSocketFrames(onFrame);

  async function act(id: string, action: "ack" | "resolve") {
    const res = await fetch(`/api/alerts/${id}/${action}`, { method: "POST" });
    if (res.ok) {
      mutate();
    }
  }

  return (
    <div>
      <div className="mb-6 flex items-center justify-between">
        <h1 className="text-xl font-semibold">Alerts</h1>
        <div className="flex gap-1 rounded-lg border border-border p-1">
          {FILTERS.map((f) => (
            <button
              key={f.label}
              onClick={() => setFilter(f.value)}
              className={`rounded px-3 py-1 text-sm ${
                filter === f.value ? "bg-surface-2 text-text" : "text-text-muted"
              }`}
            >
              {f.label}
            </button>
          ))}
        </div>
      </div>

      {isLoading && <p className="text-text-muted">Loading…</p>}
      {data && data.alerts.length === 0 && <p className="text-text-muted">No alerts.</p>}

      {data && data.alerts.length > 0 && (
        <div className="overflow-x-auto rounded-lg border border-border">
          <table className="w-full text-left text-sm">
            <thead className="bg-surface-2 text-text-muted">
              <tr>
                <th className="px-4 py-2 font-medium">Device</th>
                <th className="px-4 py-2 font-medium">Rule</th>
                <th className="px-4 py-2 font-medium">Severity</th>
                <th className="px-4 py-2 font-medium">State</th>
                <th className="px-4 py-2 font-medium">Fired</th>
                {canOperate && <th className="px-4 py-2 font-medium">Actions</th>}
              </tr>
            </thead>
            <tbody>
              {data.alerts.map((a) => (
                <tr key={a.ID} className="border-t border-border hover:bg-surface-2">
                  <td className="px-4 py-2 text-text-muted">{a.DeviceID}</td>
                  <td className="px-4 py-2">
                    {a.RuleName} <span className="text-text-muted">({a.SensorType})</span>
                  </td>
                  <td className={`px-4 py-2 ${SEVERITY_COLOR[a.Severity] ?? ""}`}>{a.Severity}</td>
                  <td className="px-4 py-2">{a.State}</td>
                  <td className="px-4 py-2 text-text-muted">{new Date(a.FiredAt).toLocaleString()}</td>
                  {canOperate && (
                    <td className="flex gap-2 px-4 py-2">
                      {a.State === "firing" && (
                        <button
                          onClick={() => act(a.ID, "ack")}
                          className="rounded border border-border px-2 py-1 text-xs hover:bg-surface-2"
                        >
                          Ack
                        </button>
                      )}
                      {a.State !== "resolved" && (
                        <button
                          onClick={() => act(a.ID, "resolve")}
                          className="rounded border border-border px-2 py-1 text-xs hover:bg-surface-2"
                        >
                          Resolve
                        </button>
                      )}
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
