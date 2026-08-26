"use client";

import { CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { downsample, type Point } from "@/lib/downsample";

// Charts window mean, not raw telemetry.Reading — internal/window's
// MetricEvent is what the codebase earmarks for this (see
// internal/window/metric_event.go's doc comment), and it's already the
// downsampled-by-windowing signal; the extra client-side downsample below
// only matters once a session's accumulated history itself grows large.
const MAX_CHART_POINTS = 300;

export default function MetricChart({ points }: { points: Point[] }) {
  const data = downsample(points, MAX_CHART_POINTS).map((p) => ({
    time: new Date(p.t).toLocaleTimeString(),
    value: p.v,
  }));

  if (data.length === 0) {
    return <p className="text-text-muted">No data yet — waiting for the device to report.</p>;
  }

  return (
    <ResponsiveContainer width="100%" height={280}>
      <LineChart data={data}>
        <CartesianGrid stroke="#293633" strokeDasharray="3 3" />
        <XAxis dataKey="time" stroke="#96A6A1" fontSize={11} minTickGap={40} />
        <YAxis stroke="#96A6A1" fontSize={11} />
        <Tooltip contentStyle={{ background: "#131C1B", border: "1px solid #293633", color: "#E6ECEA" }} />
        <Line type="monotone" dataKey="value" stroke="#37D9C4" dot={false} isAnimationActive={false} />
      </LineChart>
    </ResponsiveContainer>
  );
}
