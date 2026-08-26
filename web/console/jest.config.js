/** @type {import('jest').Config} */
// Only src/lib's pure functions are unit-tested (downsample.ts,
// backoff.ts) — see the Phase 5 plan's testing-scope note on why this
// console doesn't carry a component/E2E suite yet.
module.exports = {
  preset: "ts-jest",
  testEnvironment: "node",
  testMatch: ["<rootDir>/src/**/*.test.ts"],
};
