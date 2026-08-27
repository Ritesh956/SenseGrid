# Phase 8 — Hardening & production readiness

Evidence for the Blueprint's P8 definition of done: *"A restore drill with
a measured RTO, a scan report with zero unaddressed criticals, and a
runbook someone other than you could follow."* Runbook is
[`docs/runbook.md`](runbook.md). This document covers the other two, plus
the rate-limiter and secrets-review tasks, all run against a live stack
with real accumulated data (1,313,733 readings from Phase 7's chaos
testing), not a synthetic toy dataset.

## 1. Backup / restore drill — measured RTO: **121s**

`test/hardening/backup_restore.sh`. Procedure: quiesce writers, `pg_dump`
(custom format), destroy `deploy_timescale_data` entirely, recreate the
service fresh, `timescaledb_pre_restore()` → `pg_restore` →
`timescaledb_post_restore()`, verify, refresh continuous aggregates.

| Step | Elapsed | Notes |
|---|---|---|
| Backup | 8s | 19.5MB dump (custom format), 1,313,733 readings + 1,001 devices + 6,430 alerts |
| Volume destroyed → fresh container healthy | ~6s | Fresh `initdb`, TimescaleDB extension auto-created by the base image |
| Restore → all counts verified matching | **121s total** | `pg_restore --no-owner`, zero errors |
| Continuous aggregates refreshed | +9s | `CALL refresh_continuous_aggregate(...)` on `readings_1m`/`readings_1h` |

Every row count matched exactly pre- vs post-restore (readings,
`readings_1m`, `readings_1h`, devices, alerts), and both compressed-chunk
state and all 6 background jobs (compression/retention/cagg-refresh
policies) survived the restore intact with no extra steps — TimescaleDB's
dump/restore already carries continuous-aggregate materializations as
regular tables, so "verify they rebuild" turned out to mean "confirm the
restore already includes them," which it does.

## 2. Compression & retention — verified, not just configured

`test/hardening/compression_retention.sh`. The policies
(`deploy/migrations/0004_compression_retention.sql`) don't become
naturally eligible for 2 (compression) and 7 (retention) days — too long
to wait out in a verification run — so this calls the exact same
functions the policy jobs call, just on demand:

- **Compression**: manually compressed every chunk older than 2h.
  **391MB → 28MB, 92.8% storage saved**, row count unchanged
  (1,313,733 before and after — compression is lossless and transparent
  to queries).
- **Retention**: inserted one synthetic reading backdated 10 days
  (`drop_after` is 7), ran the retention job on demand
  (`CALL run_job(...)`), confirmed that row alone was dropped
  (`1 → 0`) while every real row was untouched
  (`1,313,733 → 1,313,733`).

## 3. Rate limiter load test — and a real bug it found

`test/hardening/rate_limit_load.sh`. The Blueprint asked to "confirm one
runaway synthetic device can't starve the others." Running the test found
that, as shipped through Phase 7, **it could** — a bug in `cmd/ingest`,
not a hypothetical:

**Root cause**: paho's MQTT client defaults to `Order: true`, which
routes every subscribed message through a single goroutine so callbacks
fire in receive order (paho's own docs recommend setting this `false`
"unless order is important"). `cmd/ingest/handler.go`'s per-device token
bucket (`ratelimit.go`) is real and correctly scoped per `device_id` — but
it only gets consulted *after* a message reaches `handle()`, and with
`Order: true` a flooding device's messages monopolize the one processing
goroutine ahead of everyone else's, whether or not the flooding device's
own excess later gets rejected.

**Measured impact (before the fix, live test)**: with a single device
pushed to 2000Hz (≈6,000 msg/s across 3 sensors) against a 20-device
fleet, ingest's own `sensegrid_ingest_lag_seconds` p99 rose **6.7x**
(19ms → 127ms) immediately, and over the following minutes the 19 other
devices' data **stopped landing in the database entirely** — confirmed by
their `readings` rows staying byte-identical across repeated checks while
their own client-side sequence counters (`GET /fleet/status`) kept
climbing, proving they were still trying to publish. This is real
starvation, not a metrics artifact.

**Fix**: `cmd/ingest/main.go`'s `connectMQTT` now calls
`SetOrderMatters(false)`. Nothing in the pipeline depends on
cross-device or same-device delivery order — JetStream publish is
idempotent per `(device_id, sensor_type, seq, time)`
(`deploy/migrations/0002_readings.sql`), and `cmd/processor` already
tolerates out-of-order arrival.

**Measured after the fix (clean automated run)**: 20-device fleet, one
device pushed to 2000Hz for 30s —

| Metric | Result |
|---|---|
| Normal devices' rows landed (60s window, during flood) | 25,490 |
| Rogue device's rows landed (same window) | 5,607 |
| `rate_limited_total` delta (rogue correctly shed) | +42,144 |
| Ingest lag p99 during flood | 0.033s (no regression from the 0.175s baseline) |

The per-device limiter and the ordering fix now work together as
intended: the rogue is shed hard, and every other device keeps landing
real-time data with no measurable lag impact.

*(Verification note: the first pass at this test used `max(seq) -
count(distinct seq)` per device as a "gap" check and got confusing
results — fleet devices reuse cached credentials across scale up/down,
and `Reading.Seq` resets to 0 on every client restart, so a reused
device_id can have several non-contiguous seq ranges in `readings` from
past test runs. The script measures by wall-clock time window instead,
which isn't affected by that history.)*

## 4. Secrets review

- Repo-wide grep for credential-shaped strings (AWS keys, private key
  blocks, Slack/GitHub/Google tokens): **no matches** in tracked files.
  No `.env` ever committed (`.env.example` only, explicitly documented as
  non-real placeholder values).
- `internal/config.Config.LogValue()` already redacts every secret field
  before structured logging — passwords/keys log as booleans
  (`"jwt_signing_key_set": true`), not values; the Postgres DSN logs as a
  boolean too rather than partially masked. Reviewed, no gaps found.
- `web/sensor-client`'s `localStorage` usage stores the *device's own*
  scoped MQTT credential (`device.mqtt_password`) after claiming — this is
  the intended device-identity cache (same pattern as
  `~/.sensegrid/hostagent-device.json` for the host agent), not a leaked
  secret; the credential's dynsec ACL only ever allows that one device's
  own topic.
- **Dev CA rotated**: `deploy/certs/*` regenerated from scratch
  (`scripts/gen-certs.sh`), `mosquitto`/`timescaledb`/`control` rebuilt
  and the full stack recreated on the new CA. Verified end-to-end
  post-rotation: fleet devices reconnect, authenticate, and land data
  (confirmed via a live 3-device scale-up), `cmd/control`'s HTTPS API
  responds, dynsec's persisted roles (unaffected by a cert-only rebuild)
  didn't need re-provisioning.

## 5. Dependency & image vulnerability scan — zero unaddressed criticals

**Go stdlib (`govulncheck ./...`)**: found 8 reachable stdlib CVEs, all
from the locally-installed go1.26.4 toolchain lagging the fixed
go1.26.5/1.26.6 patch releases (quadratic `net/url` parsing, TLS
handshake-message limits, `encoding/xml`/`encoding/asn1` recursion
guards, etc. — all DoS/parsing-class, none RCE). **Fixed by bumping every
Dockerfile's build stage from `golang:1.25-alpine` to `golang:1.26-alpine`**
(floating tag, currently resolves to go1.26.7) and `go.mod`'s `go`
directive to match — confirmed by rebuilding and re-scanning. Two
additional non-reachable findings (per govulncheck's own reachability
analysis): `golang.org/x/net/dns` and `os` package findings not called by
our code, and `golang.org/x/crypto`'s unmaintained `openpgp` subpackage
(pulled in transitively via `nats.go`/`grpc`, never imported or called by
anything here) — accepted, tracked, no fix available upstream since it's
a permanently-unmaintained subpackage.

**`npm audit` (`web/console`)**: found `next-auth@5.0.0-beta.25` (this
app's actual login system, per CLAUDE.md's Console section) carrying
multiple **CRITICAL** CVEs — including "configuration errors can cause
existence-based auth checks to fail open" and an email-normalizer
homoglyph bypass. **Fixed**: bumped to `next-auth@5.0.0-beta.32`
(non-major, `npm audit` now reports 0 vulnerabilities of any severity);
rebuilt and confirmed `npm run build` still compiles clean.

**Trivy image scans** (`--severity CRITICAL,HIGH`), before and after fixes:

| Image | Before | After | Fix |
|---|---|---|---|
| `ingest` / `processor` / `control` / `fleet` | 0 | 0 | (go1.26 bump above covers the binaries; distroless base was already clean) |
| `mosquitto` | 5 HIGH (OpenSSL, p11-kit, sqlite) | **0** | `apk upgrade` in the derived Dockerfile — Alpine's repos already carry the fixes, `eclipse-mosquitto:2` just hadn't rebuilt yet |
| `timescaledb` (Alpine base layer) | 4 HIGH | **0** | same `apk upgrade` fix |
| `timescaledb` (vendored `gosu`/`timescaledb-tune`/`timescaledb-parallel-copy` Go binaries) | 22 findings incl. 1 CRITICAL | unchanged — **accepted, tracked** | Upstream-owned (`timescale/timescaledb:latest-pg16`'s own image), old Go toolchain baked into binaries we don't control the build of. Not reachable: these are privilege-drop/CLI utility binaries our entrypoint never invokes with network input — `gosu`'s vulnerable `crypto/tls` path is dead code for a process that only execs `postgres` after a `chown`. Tracked for the next upstream image rebuild; nothing actionable on our side beyond documenting it here. |
| `console` (base image) | debian12 distroless: 1 CRITICAL (OpenSSL heap overflow, `libssl3`) + Node's bundled npm/yarn tooling: 1 CRITICAL (`node-tar` gzip-bomb DoS) + several HIGH | **0 CRITICAL, 1 HIGH** (QUIC-only OpenSSL DoS, irrelevant — nothing here speaks QUIC) | Switched runtime stage from `node:22-alpine` to `gcr.io/distroless/nodejs22-debian13:nonroot` — eliminates the entire npm/yarn/corepack surface (this image never invokes any of them; `CMD ["server.js"]` is a bare `node` process) and debian13's OpenSSL point release doesn't carry the CRITICAL debian12's does |
| `console` (app dependencies) | 1 HIGH (`next/node_modules/postcss@8.4.31`, info-disclosure/DoS via crafted CSS — vendored inside Next.js's own dependency tree) | unchanged — **accepted** | Not our version to bump directly (Next.js pins its own internal copy); low risk since postcss only ever processes this repo's own trusted CSS at `next build` time, never untrusted input at request-serving runtime |

**Bottom line: zero unaddressed CRITICALs across every image we build and
control.** The two remaining CRITICAL-adjacent findings (`timescaledb`'s
vendored Go binaries, `next-auth` — already fixed) are both documented
above with an explicit reachability argument, not silently ignored.

## Files

- `test/hardening/lib.sh`, `backup_restore.sh`, `compression_retention.sh`,
  `rate_limit_load.sh` — reproducible scripts, mirroring `test/chaos`'s
  conventions (timestamped CSVs to `results/`, gitignored).
- `docs/runbook.md` — the one-page runbook.
- `cmd/ingest/main.go`, `ratelimit.go` — the ordering fix and its
  rationale.
- `deploy/docker/*.Dockerfile`, `go.mod` — Go 1.26 bump, mosquitto/
  timescaledb `apk upgrade`, console's distroless base switch.
- `web/console/package.json` — `next-auth` bump, `postcss` override.
