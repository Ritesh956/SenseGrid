"use client";

import { useState } from "react";
import useSWR from "swr";
import { fetcher } from "@/lib/fetcher";
import { roleAtLeast } from "@/lib/roles";
import type { ShadowView } from "@/lib/types";

// Desired-vs-reported is REST-polled, not WS-fed — internal/shadow's
// desired/reported state is MQTT retained-publish only, never republished
// to NATS (see the Phase 5 plan's Context section on why that's a
// deliberate scope decision, not a gap).
export default function ShadowPanel({ deviceId, role }: { deviceId: string; role: string }) {
  const { data, mutate } = useSWR<ShadowView>(`/api/devices/${deviceId}/shadow`, fetcher, {
    refreshInterval: 3000,
  });
  const [sampleRate, setSampleRate] = useState("");
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);

  const canEdit = roleAtLeast(role, "admin");

  async function onSave() {
    setSaving(true);
    setSaveError(null);
    try {
      const res = await fetch(`/api/devices/${deviceId}/shadow`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          schema_version: "1.0",
          sample_rate_hz: sampleRate ? Number(sampleRate) : undefined,
        }),
      });
      if (!res.ok) {
        const body = await res.json().catch(() => ({}));
        throw new Error((body as { error?: string }).error ?? `save failed: ${res.status}`);
      }
      await mutate();
      setSampleRate("");
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : "save failed");
    } finally {
      setSaving(false);
    }
  }

  if (!data) {
    return <p className="text-text-muted">Loading shadow state…</p>;
  }

  return (
    <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
      <div className="rounded-lg border border-border p-4">
        <div className="mb-2 flex items-center justify-between">
          <h3 className="text-sm font-medium text-text-muted">Desired (rev {data.desired_revision})</h3>
          {data.drift && (
            <span className="rounded-full bg-warn/20 px-2 py-0.5 text-xs font-medium text-warn">drift</span>
          )}
        </div>
        <ShadowFields values={data.desired} />
      </div>

      <div className="rounded-lg border border-border p-4">
        <h3 className="mb-2 text-sm font-medium text-text-muted">Reported</h3>
        {data.reported ? (
          <>
            <ShadowFields values={data.reported} />
            {data.reported.rejected && (
              <p className="mt-2 text-xs text-crit">
                rejected rev {data.reported.rejected_revision}: {data.reported.reject_reason}
              </p>
            )}
          </>
        ) : (
          <p className="text-text-muted">No report yet.</p>
        )}
      </div>

      {canEdit && (
        <div className="rounded-lg border border-border p-4 md:col-span-2">
          <h3 className="mb-2 text-sm font-medium text-text-muted">Update desired sample rate</h3>
          <div className="flex items-center gap-2">
            <input
              type="number"
              step="0.1"
              placeholder="Hz"
              value={sampleRate}
              onChange={(e) => setSampleRate(e.target.value)}
              className="w-32 rounded border border-border bg-surface-2 px-3 py-1.5 text-text outline-none focus:border-accent"
            />
            <button
              onClick={onSave}
              disabled={saving || !sampleRate}
              className="rounded bg-accent px-3 py-1.5 text-sm font-medium text-bg disabled:opacity-50"
            >
              {saving ? "Saving…" : "Push config"}
            </button>
          </div>
          {saveError && <p className="mt-2 text-sm text-crit">{saveError}</p>}
        </div>
      )}
    </div>
  );
}

function ShadowFields({ values }: { values: object }) {
  const entries = Object.entries(values).filter(([k]) => k !== "schema_version");
  return (
    <dl className="space-y-1 text-sm">
      {entries.map(([k, v]) => (
        <div key={k} className="flex justify-between gap-4">
          <dt className="text-text-muted">{k}</dt>
          <dd className="text-text">{Array.isArray(v) ? v.join(", ") : String(v ?? "—")}</dd>
        </div>
      ))}
    </dl>
  );
}
