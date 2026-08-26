import path from "node:path";
import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  // Standalone output keeps the runtime image small (deploy/docker/console.Dockerfile
  // copies only .next/standalone + .next/static, not the full node_modules tree),
  // matching every other SenseGrid service's distroless-sized runtime image.
  output: "standalone",
  // Without this, Next.js infers the workspace root from the nearest
  // lockfile above this directory (a developer's home-directory
  // package-lock.json, in one observed case) instead of web/console
  // itself, which is harmless but throws a build-time warning every time.
  outputFileTracingRoot: path.join(__dirname),
};

export default nextConfig;
