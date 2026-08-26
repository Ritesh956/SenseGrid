"use client";

import { useParams } from "next/navigation";
import { useCallback, useMemo, useState } from "react";
import useSWR from "swr";
import { fetcher } from "@/lib/fetcher";
import { useSocketFrames, useConsoleRole } from "@/components/SocketProvider";
import type { WSFrame } from "@/lib/use-control-socket";
import MetricChart from "@/components/MetricChart";
import ShadowPanel from "@/components/ShadowPanel";
import type { Alert, Device, MetricEvent } from "@/lib/types";
import type { Point } from "@/lib/downsample";

const MAX_POINTS_PER_SENSOR = 2000;

export default function DeviceDetailPage() {
  const params = useParams<{ id: string }>();
  const deviceId = params.id;
  const role = useConsoleRole();

  const { data: device } = useSWR<Device>(`/api/devices/${deviceId}`, fetcher);
  const { data: alertsData, mutate: refreshAlerts } = useSWR<{ alerts: Alert[] }>(
    `/api/alerts?device_id=${deviceId}&limit=20`,
    fetcher,
  );

  const [series, setSeries] = useState<Record<string, Point[]>>({});
  const [sensor, setSensor] = useState<string | null>(null);

  const onFrame = useCallback(
    (frame: WSFrame) => {
      if (frame.type === "alert") {
        const alert = frame.payload as { device_id?: string };
        if (alert.device_id === deviceId) {
          refreshAlerts();
        }
        return;
      }
      if (frame.type !== "metric") {
        return;
      }
      const evt = frame.payload as MetricEvent;
      if (evt.device_id !== deviceId) {
        return;
      }
      setSeries((prev) => {
        const existing = prev[evt.sensor_type] ?? [];
        const next = [...existing, { t: evt.window_end_ms, v: evt.mean }].slice(-MAX_POINTS_PER_SENSOR);
        return { ...prev, [evt.sensor_type]: next };
      });
      setSensor((s) => s ?? evt.sensor_type);
    },
    [deviceId, refreshAlerts],
  );
  useSocketFrames(onFrame);

  const sensorTypes = useMemo(() => Object.keys(series).sort(), [series]);
  const activePoints = sensor ? (series[sensor] ?? []) : [];

  return (
    <div>
      <div className="mb-6">
        <h1 className="text-xl font-semibold">{device?.name ?? deviceId}</h1>
        <p className="text-sm text-text-muted">
          {device?.type} · {device?.status} · {deviceId}
        </p>
      </div>

      <section className="mb-8">
        <div className="mb-2 flex items-center justify-between">
          <h2 className="text-sm font-medium text-text-muted">Live metrics</h2>
          {sensorTypes.length > 1 && (
            <select
              value={sensor ?? ""}
              onChange={(e) => setSensor(e.target.value)}
              className="rounded border border-border bg-surface-2 px-2 py-1 text-sm text-text"
            >
              {sensorTypes.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
          )}
        </div>
        <div className="rounded-lg border border-border p-4">
          <MetricChart points={activePoints} />
        </div>
      </section>

      <section className="mb-8">
        <h2 className="mb-2 text-sm font-medium text-text-muted">Desired vs. reported</h2>
        <ShadowPanel deviceId={deviceId} role={role} />
      </section>

      <section>
        <h2 className="mb-2 text-sm font-medium text-text-muted">Recent alerts</h2>
        {!alertsData || alertsData.alerts.length === 0 ? (
          <p className="text-text-muted">No recent alerts.</p>
        ) : (
          <ul className="divide-y divide-border rounded-lg border border-border">
            {alertsData.alerts.map((a) => (
              <li key={a.ID} className="flex items-center justify-between px-4 py-2 text-sm">
                <span>
                  <span className="font-medium">{a.RuleName}</span>{" "}
                  <span className="text-text-muted">({a.SensorType})</span>
                </span>
                <span className="text-text-muted">{a.State}</span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}
