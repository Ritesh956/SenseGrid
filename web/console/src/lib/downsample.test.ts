import { downsample, type Point } from "./downsample";

function series(n: number): Point[] {
  return Array.from({ length: n }, (_, i) => ({ t: i, v: i }));
}

describe("downsample", () => {
  it("returns points unchanged when already at or below the threshold", () => {
    const points = series(10);
    expect(downsample(points, 100)).toEqual(points);
    expect(downsample(points, 10)).toEqual(points);
  });

  it("reduces to at most maxPoints", () => {
    const points = series(1000);
    const out = downsample(points, 50);
    expect(out.length).toBeLessThanOrEqual(50);
  });

  it("always keeps the first and last point exactly", () => {
    const points = series(500);
    const out = downsample(points, 20);
    expect(out[0]).toEqual(points[0]);
    expect(out[out.length - 1]).toEqual(points[points.length - 1]);
  });

  it("is a no-op for a degenerate maxPoints", () => {
    const points = series(100);
    expect(downsample(points, 2)).toEqual(points);
    expect(downsample(points, 0)).toEqual(points);
  });
});
