/** @type {import('jest').Config} */
// Only src/lib's pure functions are unit-tested (downsample.ts,
// backoff.ts) — see the Phase 5 plan's testing-scope note on why this
// console doesn't carry a component/E2E suite yet.
module.exports = {
  preset: "ts-jest",
  testEnvironment: "node",
  testMatch: ["<rootDir>/src/**/*.test.ts"],
  // testMatch already scopes which tests *run*, but jest-haste-map still
  // crawls the whole rootDir building its module map — so once a `next
  // build` has produced .next/standalone/package.json, that file and the
  // real package.json both claim the name "sensegrid-console" and every
  // run prints a "Haste module naming collision" warning. Ignoring build
  // output at the module-map level is the actual fix; .next is generated,
  // never a test source.
  modulePathIgnorePatterns: ["<rootDir>/.next/"],
};
