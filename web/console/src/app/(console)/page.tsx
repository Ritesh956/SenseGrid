"use client";

import Link from "next/link";
import useSWR from "swr";
import { fetcher } from "@/lib/fetcher";
import type { Alert, Device, ShadowView } from "@/lib/types";

function AlertBadge({ count }: { count: number }) {
  if (count === 0) {
    return null;
  }
  return (
    <span className="rounded-full bg-crit/20 px-2 py-0.5 text-xs font-medium text-crit">{count} firing</span>
  );
}

// Best-effort per-device shadow fetch — fine at this fleet's scale (a
// handful of real devices), not worth a new bulk-shadow Go endpoint just
// for one column (see the Phase 5 plan's scope notes).
function SampleRate({ deviceId }: { deviceId: string }) {
  const { data } = useSWR<ShadowView>(`/api/devices/${deviceId}/shadow`, fetcher, { refreshInterval: 5000 });
  const rate = data?.desired?.sample_rate_hz;
  return <span className="text-text-muted">{rate ? `${rate} Hz` : "—"}</span>;
}

export default function FleetPage() {
  const { data: devicesData, error: devicesError } = useSWR<{ devices: Device[] }>("/api/devices", fetcher, {
    refreshInterval: 5000,
  });
  const { data: alertsData } = useSWR<{ alerts: Alert[] }>("/api/alerts?state=firing", fetcher, {
    refreshInterval: 5000,
  });

  const firingByDevice = new Map<string, number>();
  for (const a of alertsData?.alerts ?? []) {
    firingByDevice.set(a.DeviceID, (firingByDevice.get(a.DeviceID) ?? 0) + 1);
  }

  return (
    <div>
      <h1 className="mb-6 text-xl font-semibold">Fleet</h1>

      {devicesError && <p className="text-crit">Failed to load devices: {devicesError.message}</p>}

      {!devicesData && !devicesError && <p className="text-text-muted">Loading…</p>}

      {devicesData && devicesData.devices.length === 0 && (
        <p className="text-text-muted">No devices claimed yet.</p>
      )}

      {devicesData && devicesData.devices.length > 0 && (
        <div className="overflow-x-auto rounded-lg border border-border">
          <table className="w-full text-left text-sm">
            <thead className="bg-surface-2 text-text-muted">
              <tr>
                <th className="px-4 py-2 font-medium">Device</th>
                <th className="px-4 py-2 font-medium">Type</th>
                <th className="px-4 py-2 font-medium">Status</th>
                <th className="px-4 py-2 font-medium">Last seen</th>
                <th className="px-4 py-2 font-medium">Sample rate</th>
                <th className="px-4 py-2 font-medium">Alerts</th>
              </tr>
            </thead>
            <tbody>
              {devicesData.devices.map((d) => (
                <tr key={d.id} className="border-t border-border hover:bg-surface-2">
                  <td className="px-4 py-2">
                    <Link href={`/devices/${d.id}`} className="text-accent hover:underline">
                      {d.name}
                    </Link>
                    <div className="text-xs text-text-muted">{d.id}</div>
                  </td>
                  <td className="px-4 py-2">{d.type}</td>
                  <td className="px-4 py-2">{d.status}</td>
                  <td className="px-4 py-2 text-text-muted">
                    {d.last_seen ? new Date(d.last_seen).toLocaleString() : "never"}
                  </td>
                  <td className="px-4 py-2">
                    <SampleRate deviceId={d.id} />
                  </td>
                  <td className="px-4 py-2">
                    <AlertBadge count={firingByDevice.get(d.id) ?? 0} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
