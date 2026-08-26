export interface Point {
  t: number;
  v: number;
}

// downsample reduces points to at most maxPoints by averaging within
// fixed-size buckets, preserving the first and last point exactly so a
// chart's visible time range never shifts. Below maxPoints, points is
// returned unchanged — this only kicks in once a device-detail chart's
// accumulated MetricEvent history actually grows past what's worth
// rendering as individual SVG points (see the device-detail page).
export function downsample(points: Point[], maxPoints: number): Point[] {
  if (maxPoints <= 2 || points.length <= maxPoints) {
    return points;
  }

  const bucketSize = (points.length - 2) / (maxPoints - 2);
  const out: Point[] = [points[0]];

  for (let i = 0; i < maxPoints - 2; i++) {
    const start = Math.floor(1 + i * bucketSize);
    const end = Math.min(Math.floor(1 + (i + 1) * bucketSize), points.length - 1);
    const bucket = points.slice(start, end);
    if (bucket.length === 0) {
      continue;
    }
    const avgT = bucket.reduce((sum, p) => sum + p.t, 0) / bucket.length;
    const avgV = bucket.reduce((sum, p) => sum + p.v, 0) / bucket.length;
    out.push({ t: avgT, v: avgV });
  }

  out.push(points[points.length - 1]);
  return out;
}
