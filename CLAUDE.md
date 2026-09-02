# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

SenseGrid is a real-time IoT telemetry and control platform, built phase-by-phase against a design
document (the "Blueprint" — see "Where the full plan lives" below). Real sensor data from a phone PWA
and a laptop host agent flows through MQTT → NATS JetStream → TimescaleDB, with a control plane that
provisions devices and (from Phase 4 on) pushes config back down to them. Synthetic load only enters
via `cmd/fleet` (Phase 7), not the primary data path — the phone and laptop are real hardware.

**Current status: all 11 phases (0–10) tagged** (`v0.1-phase0`, `v0.2-phase1`, `v0.3-phase2`,
`v0.4-phase3`, `v0.5-phase4`, `v0.6-phase5`, `v0.7-phase6`, `v0.8-phase7`, `v0.9-phase8`,
`v0.10-phase9`, `v1.0-phase10`, `v1.0.1-phase10`, `v1.0.2-phase10`). Phase 9 (optional credibility layer — firmware) adds
`firmware/esp32`: an Arduino/PlatformIO ESP32 device speaking the exact same v1 wire contract as
`cmd/hostagent` — claim over HTTPS, MQTT/TLS, a hardware timer interrupt (not `delay()`) sampling a
DHT22 + potentiometer, and live `sample_rate_hz` config applied the same `applyPartial`/`toReported`
way `cmd/hostagent/config.go` does — verified against a live stack (a client speaking its exact
protocol claimed a device, published a reading that landed in TimescaleDB through the full pipeline,
and showed up via `GET /v1/devices` exactly like a phone or laptop, distinguished only by
`type:"esp32"` — the Phase 9 DoD itself) rather than just written and hoped. See "Optional
credibility layer — firmware (Phase 9)" below. Phase 10 (report & submission artifacts) assembled
the P7/P8 evidence into `docs/phase10-report.md` — architecture diagram with a live-trace-confirmed
per-hop latency budget, baseline-vs-load latency, the scaling/saturation curve, a failure-recovery
table, design-decisions write-up, honest scope statement — and, in the process of actually running
the three chaos drills (`kill_broker.sh`/`kill_processor.sh`/`pause_db.sh`) that Phase 7 had left
unexecuted, found and fixed a real measurement bug in `test/chaos/lib.sh`'s seq-gap check (it
compared a fresh run's counter against a device's *entire* readings history with no time bound,
producing a bogus 43,736-message "loss" reading off a stale reused device_id from five days earlier).
See "Report & submission artifacts (Phase 10)" below. See "Phase status" below before assuming
something isn't built yet — check git tags and `internal/` first.

## Commands

```bash
# Build / test / vet everything
go build ./...
go vet ./...
gofmt -l .              # must be empty; gofmt -w . to fix
go test ./...           # -race needs a 64-bit C toolchain — see the gotcha below

# Build one binary
go build -o /tmp/x ./cmd/ingest

# Bring up the full stack (see "Windows/Git Bash gotchas" below first)
cp .env.example .env    # only needed once, or after .env.example changes
bash scripts/gen-certs.sh
docker compose -f deploy/docker-compose.yml --env-file .env up -d --build
docker compose -f deploy/docker-compose.yml --env-file .env ps
docker compose -f deploy/docker-compose.yml --env-file .env logs <service> --no-log-prefix --tail 50
docker compose -f deploy/docker-compose.yml --env-file .env down       # keep volumes
docker compose -f deploy/docker-compose.yml --env-file .env down -v    # wipe volumes too

# Issue a device registration token (admin CLI, not an HTTP endpoint — see "Auth model")
export MSYS_NO_PATHCONV=1
docker compose -f deploy/docker-compose.yml --env-file .env exec control /app token create -name my-device -type laptop -ttl 1h

# Mint a JWT for the REST API (also CLI-only — see "REST API auth")
docker compose -f deploy/docker-compose.yml --env-file .env exec control /app jwt create -role admin -ttl 1h

# Provision the first console login (admin CLI, not an HTTP endpoint — see "REST API auth")
docker compose -f deploy/docker-compose.yml --env-file .env exec control /app user create -username admin -role admin -password <password>

# Prometheus scrape health: http://localhost:9190/targets
# Grafana (admin/admin), dashboards + alerting provisioned as code: http://localhost:3300
# — see "Observability (Phase 6)"

# Bulk-issue fleet registration tokens, then scale the fleet up — inert by default (FLEET_TARGET_DEVICES=0),
# see "Synthetic fleet & chaos testing (Phase 7)"
export MSYS_NO_PATHCONV=1
docker compose -f deploy/docker-compose.yml --env-file .env exec control /app token create -name fleet -type fleet -ttl 6h -count 1000 -out /chaos-data/fleet-tokens.txt
curl -X POST http://localhost:8083/fleet/scale -d '{"count":100}'

# Run the chaos suite (ramp/kill_broker/kill_processor/pause_db/partition) — see test/chaos/README.md
cd test/chaos && ./ramp.sh

# Run the Phase 8 hardening drills (backup/restore RTO, compression/retention, rate-limiter load test)
# against a live stack — see "Hardening & production readiness (Phase 8)" below
cd test/hardening && ./backup_restore.sh          # DESTRUCTIVE: destroys and restores the timescaledb volume
./compression_retention.sh
./rate_limit_load.sh

# Vulnerability scan: Go stdlib/deps, then every built image (needs Docker; pulls aquasec/trivy)
govulncheck ./...
export MSYS_NO_PATHCONV=1
for img in ingest processor control fleet mosquitto timescaledb console; do
  docker run --rm -v /var/run/docker.sock:/var/run/docker.sock -v trivy-cache:/root/.cache/ \
    aquasec/trivy:latest image --severity CRITICAL,HIGH --scanners vuln "deploy-$img"
done

# Run hostagent natively against the compose stack (it is NOT a compose service — see below)
HOSTAGENT_CLAIM_TOKEN=<token from above> \
TLS_CA_FILE=deploy/certs/ca.pem \
go run ./cmd/hostagent
# second run onward needs no token — credentials cache at ~/.sensegrid/hostagent-device.json

# Build the Phase 9 ESP32 firmware (see firmware/esp32/README.md for the one-time
# config.h/ca_cert.h setup this needs first)
pip install platformio
cd firmware/esp32 && pio run   # -> .pio/build/esp32dev/firmware.{bin,elf}, which wokwi.toml points at
```

There is a `Makefile`, but **no `make` binary is installed in this dev environment** — use the raw
commands above, not `make docker-up` etc. (CI and any environment that does have `make` can use it
directly; the targets mirror the commands above.)

`make` aside, this project targets **Windows + Git Bash** as the primary dev environment. See
"Windows/Git Bash gotchas" — several of these cost real debugging time once already; don't rediscover
them.

## Architecture

### Data path

```
PWA (browser)  ─┐                                    ┌─► ingest bridge ──► NATS JetStream ──► processor ──► TimescaleDB
hostagent (Go) ─┴─► Mosquitto (dynamic-security auth) ┘         │                                  │
                                                                 └── OTel span ──┐    ┌── OTel span ─┘
                                                                                 └─► Jaeger (trace joined by trace_id)
```

- **Devices publish** to `sensegrid/v1/{device_id}/telemetry` over MQTT (TLS on 8883 for Go clients,
  WSS on 9001 for the browser — see "TLS / why HTTPS is required").
- **`cmd/ingest`** subscribes to `sensegrid/v1/+/telemetry` as its own service account (not a device —
  see "Auth model"), validates each payload against `internal/telemetry.Reading`, stamps a
  broker-receive time, and republishes to JetStream subject `telemetry.{device_id}`. Malformed
  payloads go to JetStream subject `dlq.telemetry`, never dropped silently.
- **`cmd/processor`** runs a durable JetStream pull consumer, batches into TimescaleDB (100 rows or
  500ms, whichever first — `cmd/processor/consumer.go`), and only acks once the batch commits.
- **`cmd/control`** is the provisioning/admin plane: issues one-time registration tokens (CLI only, see
  below), serves `POST /v1/devices/claim`, and serves the PWA's static files at the same origin (no
  separate web server, no CORS to configure).
- Every hop that touches a `Reading` (or the `MetricEvent` derived from it) emits an OTel span sharing
  the payload's `trace_id`, exported to Jaeger — pull a reading's whole journey by that ID at
  `http://localhost:16686`. As of Phase 6 that's `ingest.publish` → `processor.persist`/
  `processor.window` → `control.ws_relay` (see "Observability" below) — device to the edge of the
  console, not just to the database.

### Auth model (dynamic-security plugin — read this before touching anything MQTT-related)

Mosquitto has **no anonymous access anywhere**. Auth is entirely via the `dynamic-security` plugin,
controlled at runtime over MQTT itself (`$CONTROL/dynamic-security/v1`), not config files. Four
identities exist, each provisioned differently:

| Identity | Created by | Scope |
|---|---|---|
| **admin** | `deploy/mosquitto/entrypoint.sh` at first boot (offline, `mosquitto_ctrl dynsec init`) | Full control-channel access. Used only by `cmd/control` (`internal/dynsec`) to bootstrap the three roles below. |
| **device** (role) | `cmd/control` at startup, idempotent | Every claimed device gets this role. ACLs use `%c` substitution — a device can only touch `sensegrid/v1/{its-own-id}/*`. `username == clientid == device_id`, enforced by dynsec's `clientid` pinning on `createClient`. |
| **bridge** (role) | `cmd/control` at startup, idempotent | `cmd/ingest` connects as this (`MQTT_BRIDGE_USERNAME`/`PASSWORD` in `.env`, shared secret — **not** claimed at runtime like a device). ACL is a literal wildcard filter (`subscribeLiteral` on `sensegrid/v1/+/telemetry`), because a service needs to read *every* device's topic, which `%c` substitution can't express. |
| **control** (role) | `cmd/control` at startup, idempotent (Phase 4) | `cmd/control` itself connects as this (`MQTT_CONTROL_USERNAME`/`PASSWORD` in `.env`) to retained-publish desired shadow config to any device's `.../config` topic and wildcard-subscribe every device's `.../state` topic (`internal/shadow.Publisher`/`Reconciler`) — same wildcard-ACL reasoning as bridge, just publish-side instead of subscribe-side for config. |

**A new service that needs its own broker access** needs a new role + a new
`MQTT_<X>_USERNAME/PASSWORD` pair added to `connectDynsec` in `cmd/control/main.go` and
`.env.example`, following the bridge/control pattern — do not reuse an existing role for a different
purpose.

Device provisioning flow (`cmd/control/claim.go`): registration token (Redis, `internal/devicestore`,
single-use, TTL'd) → `POST /v1/devices/claim` → **Postgres `devices` row created first**, dynsec
`createClient` second. That order is deliberate: if dynsec fails after the Postgres write, you get an
inert unclaimed device_id (harmless). The reverse order would let a device authenticate and publish
with no `devices` row, and every one of its readings would silently fail the `readings.device_id`
foreign key downstream, in a completely different service.

### REST API auth (JWT — a separate system from the MQTT auth model above)

`cmd/control`'s HTTP endpoints (device/shadow/drift, alert list/ack/resolve, rollouts, and the Phase 5
`GET /v1/ws` feed) are gated by HMAC-signed (HS256) JWT bearer tokens, not MQTT dynsec — three roles,
treated as a hierarchy (`admin` > `operator` > `viewer`; higher satisfies a lower-role requirement),
verified by `cmd/control/auth.go`'s `requireRole` middleware (`verifyToken` underneath, shared by the
header-based and WS query-param-based paths — see "Console" below). Tokens are minted two ways:

- `control jwt create -role <role> [-ttl 1h]` CLI (`cmd/control/jwt_cli.go`), no subject, mirroring
  `token create`'s CLI-only pattern — shell access to the binary is the auth boundary. Short-lived by
  default (`JWT_ACCESS_TTL`, 15m) since it's meant for scripted/automation use.
- `POST /v1/auth/login` (`cmd/control/auth_login.go`, Phase 5) — bcrypt-checked against `internal/users`
  (a real Postgres table, provisioned the same CLI-only way: `control user create -username <u> -role
  <role> -password <p>`), minting the *same* role-only JWT shape `requireRole` already verifies, just
  with a subject (username) and a separately-configurable, longer TTL (`JWT_CONSOLE_TTL`, 12h default —
  kept distinct from `JWT_ACCESS_TTL` so CLI tokens stay short-lived without forcing an interactive
  console user to re-login every 15 minutes). This is the console's only identity system; there is no
  separate NextAuth user store on the frontend side (see "Console" below).

### Data contract (`internal/telemetry`)

`Reading` is the wire schema every publisher (PWA, hostagent, fleet, and eventually ESP32) uses:
`schema_version`, `device_id`, `sensor_type`, **either** `value` (scalar) **or** `values` (map, e.g.
accelerometer `{x,y,z}` or battery `{level,charging}`), `device_time_ms`, `seq` (monotonic per-device,
resets on client restart — expected), `trace_id` (32 hex chars, doubles as the OTel trace ID since MQTT
3.1.1 has no headers for a real W3C traceparent).

**The TimescaleDB `readings` table only has one scalar `value` column, not a jsonb one.** Vector
readings are flattened at persistence time (`cmd/processor/consumer.go: flatten`) into one row per
component, `sensor_type` suffixed (`accel.x`, `battery.level`, ...), sharing the same `seq`/`time` for
idempotency purposes. This keeps every `sensor_type` aggregating identically in the continuous
aggregates (`deploy/migrations/0003...sql`) instead of special-casing vectors. Don't "fix" this by
adding a jsonb column without revisiting that migration.

### Migrations (`internal/migrations`) — not golang-migrate, on purpose

A ~130-line forward-only runner: every `*.sql` file in `deploy/migrations/` applies once, in filename
order, tracked in a `schema_migrations` table. No down-migrations exist or are planned. Two things to
know before touching it:

- A file whose **first line is exactly `-- no-transaction`** runs statement-by-statement outside an
  explicit `BEGIN`/`COMMIT` (needed for TimescaleDB continuous-aggregate and
  compression/retention-policy DDL — not proven unsafe in a transaction, but not worth the risk either).
- **Both `cmd/control` and `cmd/processor` run migrations independently at their own startup** — no
  ordering dependency between them. This creates a narrow, real race on a cold start (they can both
  try to apply the first migration simultaneously); `migrations.go` handles it by treating specific
  Postgres "already exists" error codes (`42P07`, `42710`, `23505`) as success. This was found live,
  not theoretical — `CREATE TABLE IF NOT EXISTS` genuinely isn't race-proof
  (`pg_type_typname_nsp_index`).

### Service map

| Binary | Port | Dockerfile | Notes |
|---|---|---|---|
| `cmd/control` | 8090→8080 (HTTPS), 9091 (metrics) | `deploy/docker/control.Dockerfile` | Own Dockerfile: also serves `web/sensor-client` and needs `deploy/migrations`. Since Phase 4 also connects to NATS/JetStream (shadow KV, rollout state/events) and dials the broker itself as the `control` MQTT identity, in addition to the dynsec admin channel. Since Phase 6, `/metrics` is a second, plain-HTTP server on its own port (`METRICS_ADDR`) rather than sharing the HTTPS one — see "Observability" below. |
| `cmd/ingest` | 8081 | `deploy/docker/go.Dockerfile` (generic) | |
| `cmd/processor` | 8082 | `deploy/docker/processor.Dockerfile` | Own Dockerfile: needs `deploy/migrations`. |
| `cmd/fleet` | 8083 | `deploy/docker/go.Dockerfile` (generic) | Phase 7: real device simulator, inert by default (`FLEET_TARGET_DEVICES=0`) until scaled via its own HTTP control API (`/fleet/status`, `/fleet/scale`, `/fleet/partition`, `/fleet/config`) — see "Synthetic fleet & chaos testing" below. |
| `cmd/hostagent` | 8084 | *(not containerized)* | Runs natively — needs real host CPU/battery/WiFi, which a container can't see. |
| mosquitto | 8883 (TLS), 9001 (WSS) | `deploy/docker/mosquitto.Dockerfile` | |
| NATS | 4222, 8222 (monitor) | image | JetStream on. |
| TimescaleDB | 5432 | `deploy/docker/timescaledb.Dockerfile` | |
| Redis | 6379 | image | Tokens only (see auth model). |
| Jaeger | 16686 (UI), 4317 (OTLP) | image | Accepts OTLP directly — no separate OTel Collector. |
| `web/console` | 3100→3000 | `deploy/docker/console.Dockerfile` | Next.js, not a Go binary — see "Console (Phase 5)" above for why it's structured differently from everything else in this table. |
| Prometheus | 9190→9090 | image | Scrapes `ingest:8081`, `processor:8082`, `control:9091`, `fleet:8083` (Phase 7) — see `deploy/prometheus/prometheus.yml` and "Observability"/"Synthetic fleet & chaos testing" below. |
| Grafana | 3300→3000 | image | Dashboards + alert rules provisioned as code from `deploy/grafana/`, admin/admin. |

Shared `internal/` packages, briefly: `config` (env-driven `Config` struct, one `Load()` per service),
`logging` (slog JSON), `httpserver` (health server + graceful shutdown, optional TLS), `tlsutil` (dev
CA loading, shared by anything dialing the broker/API directly), `dynsec` (the dynamic-security control
protocol client — see auth model), `provisioning` (claim-flow client for native Go edge clients:
`hostagent` and `fleet`), `devices` (Postgres device registry), `devicestore` (Redis
registration tokens only), `telemetry` (wire schema + JetStream subject naming), `tracing` (OTel
wiring + the trace_id-to-SpanContext bridge), `migrations` (see above).

### TLS / why HTTPS is required, not just nice-to-have

`DeviceMotionEvent`/`DeviceOrientationEvent` are gated behind a secure context in every modern mobile
browser — `web/sensor-client` simply won't receive motion events over plain HTTP from a phone on the
LAN. That's why `cmd/control` terminates TLS (dev CA, `deploy/certs/control.pem`) and Mosquitto's
WebSocket listener (9001) is TLS too (a page loaded over HTTPS can't open a plain `ws://` connection —
mixed content is blocked outright, no click-through). Testing from an actual phone needs **two**
one-time "accept this certificate" clicks in the phone's browser: once at `https://<lan-ip>:8090/`,
once at `https://<lan-ip>:9001/` (the second one shows a blank/error page after accepting — that's
expected, the goal is just registering the TLS exception for that origin). `LAN_IP=<ip> bash
scripts/gen-certs.sh` (after removing `deploy/certs/control.*`) adds the LAN IP to the cert's SAN so
only the untrusted-CA warning shows, not also a hostname mismatch.

### Console (Phase 5, `web/console`)

A Next.js + TypeScript + Tailwind app, separate from `web/sensor-client` (the Phase 1 vanilla-JS PWA)
and built as its own Docker service (`deploy/docker/console.Dockerfile`, host port `3100`→container
`3000` — 3000 itself is commonly already taken by something else on a dev machine, so it's mapped away
by default rather than assumed free). Two things make it structurally different from every other
service in this repo:

- **It talks to `cmd/control` through its own server-side BFF** (`src/app/api/**` route handlers), not
  directly from the browser — every REST call is proxied server-side with the session's bearer token
  attached, which is what lets `cmd/control` keep its existing "no CORS to configure" property even
  though the console is a separate origin. The one exception is the **live WebSocket**
  (`GET /v1/ws`, `cmd/control/ws_handler.go`), which connects browser-to-control directly (WS isn't
  subject to CORS) — this needs `wss://` and the same one-time dev-CA-trust click already required for
  the phone (see "TLS" above), and needs the *host-exposed* URL (`NEXT_PUBLIC_CONTROL_WS_URL`), not the
  Docker-internal one the BFF uses (`CONTROL_API_URL`) — the browser can't resolve the `control`
  hostname. The WS handler itself just subscribes core NATS (`metrics.>`/`alerts.>`/`rollout.>`) per
  connection and relays — no new publish-side plumbing, and no durability/replay need since the console
  only wants this feed live.
- **NextAuth (Auth.js v5) owns login**, calling `POST /v1/auth/login` server-side and holding the
  resulting control-plane JWT in the session (JWT strategy — see "REST API auth" above; no separate
  NextAuth identity store). Two Next.js-specific things bit during setup, worth knowing before touching
  auth config again: `NEXT_PUBLIC_*` env vars are inlined into the client bundle at **build** time, not
  read at container start, so `NEXT_PUBLIC_CONTROL_WS_URL` has to be a Docker build arg
  (`console.Dockerfile` + `docker-compose.yml`'s `build.args`), not a runtime `environment:` entry, or it
  silently does nothing; and Auth.js only trusts the incoming request's `Host` header automatically on
  Vercel — every other deployment (this one included) needs `trustHost: true` in `src/auth.ts`, or every
  request 307-redirect-loops between `/` and `/login` with an `UntrustedHost` error visible only in the
  container's own logs, never the browser. Both found live, not theoretical.

The console charts `internal/window`'s `MetricEvent` (windowed mean/EWMA), not raw `telemetry.Reading`
— see that package's doc comment, which earmarks it for exactly this. Device-shadow desired/reported
state is **not** on the WS feed (it's MQTT retained-publish only, never republished to NATS — see "Auth
model" above); the device-detail page polls `GET /v1/devices/{id}/shadow` instead, on a short interval.

`web/console/go.mod` is a deliberate no-op module boundary, not a stray file — some npm packages
(observed: `flatted`, pulled in transitively) ship a stray `.go` file inside `node_modules`, which the
root module's `go build ./...`/`go test ./...` would otherwise walk into and pick up. A `go.mod` here
(nothing ever imports it) marks the whole subtree as a separate module, which the root module's `...`
pattern skips automatically. Found live after `npm install` in this directory, not theoretical.

**Sidebar nav bug found and fixed post-Phase-10** (`web/console/src/components/ConsoleShell.tsx`):
clicking Alerts or Rollouts in the sidebar updated the URL correctly but rendered a *different*
sibling route's content — e.g. `/alerts` showing the Rollouts page. All three sidebar links
(Fleet/Alerts/Rollouts) sit in the viewport at once, so Next's default `<Link>` prefetching fetches
every route ahead of any click; the client router cache then served the wrong prefetched payload on
navigation. A hard reload at the same URL always rendered correctly, isolating this to the client-side
prefetch/router cache rather than the route or data logic. Fixed with `prefetch={false}` on the three
nav links — an acceptable trade for a low-traffic internal ops nav with only three links, forcing every
click to fetch fresh RSC content instead of relying on the prefetch cache. Verified by rebuilding and
clicking through all three views repeatedly.

**A separate, non-bug finding from the same investigation**: the Fleet page's per-device sample-rate
column (`FleetPage`'s `SampleRate` component, `src/app/(console)/page.tsx`) polls each visible device's
`GET /api/devices/{id}/shadow` individually every 5s via `useSWR` — already documented in its own code
comment as "fine at this fleet's scale (a handful of real devices), not worth a new bulk-shadow Go
endpoint just for one column." That assumption held until this dev stack accumulated ~1,100 leftover
`type:"fleet"` devices across Phase 7/8/10's chaos and load testing — at that scale the per-row polling
saturates the browser's per-origin connection pool, and everything else (including nav clicks and the
WS reconnect) gets visibly slower behind the queue. Not a code bug — the fix is operational (clean up
stale test devices, e.g. `DELETE FROM devices WHERE type='fleet'` cascaded through
`readings`/`alerts`/`shadow_history`/`rollout_targets` first), not a code change — but worth knowing if
the console feels sluggish in a dev environment that's seen a lot of `cmd/fleet` load testing.

### Observability (Phase 6)

Almost entirely a matter of *exposing and provisioning* what Phases 2–5 had already built, not
introducing metrics/tracing for the first time — `cmd/ingest` and `cmd/processor` have had `/metrics`
since Phase 2, just never scraped, and `internal/tracing` has been wired into `cmd/ingest`/`cmd/processor`
since Phase 2 too.

- **`cmd/control` got its first Prometheus/tracing wiring this phase** (`cmd/control/metrics.go`):
  `sensegrid_control_ws_clients_connected` (inc/dec around each `GET /v1/ws` connection —
  `ws_handler.go`) and `sensegrid_control_active_devices` (a periodic gauge reusing
  `cfg.DriftStaleAfter`, the same "still meaningfully connected" threshold `internal/shadow.Drift`
  already uses, rather than inventing a second one). Exposed on a **separate plain-HTTP port**
  (`METRICS_ADDR`, `:9091` default) instead of sharing `cmd/control`'s HTTPS API port — Prometheus
  scrapes it over the internal Docker network, so there's no reason to make it trust the dev CA cert
  too.
- **The WS relay (`ws_handler.go`) extends a reading's trace one hop further**: `MetricEvent` carries
  the `trace_id` of the reading it was computed from (`AlertEvent`/`RolloutEvent` don't — no bridging
  attempted for those two frame kinds), so relaying a `metrics.>` message to a console viewer opens a
  short `control.ws_relay` span joined to that same trace via `tracing.ContextWithReadingTrace` — the
  same bridge `cmd/processor` already uses. This is what makes a trace "browsable end-to-end, device →
  console" (the DoD's wording) — the actual browser hop isn't instrumented (no OTel SDK in
  `web/console`; that would be scope creep for this phase), but the last server-side hop before it is.
- **`cmd/processor`'s two durable consumers (persistence, windowing) each gained a `consumer_lag` gauge**
  (`sensegrid_processor_consumer_lag` / `_windowed_consumer_lag`), polled every 5s via the JetStream
  `Consumer.Info` call each `run()` loop already has the `jetstream.Consumer` handle for — the one
  genuinely new signal this phase adds; everything else is exposure of what already existed.
- **`cmd/fleet` got nothing this phase** — still a placeholder stub (Phase 7's job), so instrumenting it
  would just be measuring code that doesn't do anything yet.
- **Grafana is entirely file-provisioned** (`deploy/grafana/provisioning/**`, `deploy/grafana/dashboards/*.json`)
  — one Prometheus datasource with a **fixed `uid: prometheus`** (not Grafana's auto-generated default,
  since `provisioning/alerting/slo-rules.yml` references it explicitly by that UID and a random one would
  break on every fresh provision), 3 dashboards (fleet-health, pipeline-latency, alerts), and 3
  Grafana-managed SLO alert rules — no separate Alertmanager container, this project has no
  on-call/paging need to justify one. No metric anywhere in this codebase carries labels (every
  `prometheus.Counter`/`Histogram`/`Gauge` is label-less) — Phase 6 kept that convention for its new
  metrics too rather than introducing the first `*Vec`.
- Host ports **9090** (Prometheus' own default) and **3000–3002** were already taken by other local
  projects when this was built — Prometheus → **9190**, Grafana → **3300**, same reasoning as the
  console's 3100 mapping (Phase 5). If you hit a port conflict on a different machine, these are the
  three numbers to change (`deploy/docker-compose.yml`'s `prometheus`/`grafana`/`console` port mappings).

### Synthetic fleet & chaos testing (Phase 7)

`cmd/fleet` is a real device simulator, not a raw publish loop: each virtual device (`device.go`)
claims its own identity via `internal/provisioning` (bulk-issued tokens — `control token create
-count N`, this phase's addition to `token.go`), connects over MQTT/TLS, subscribes to its own config
topic, and reports shadow state exactly like `cmd/hostagent` does — the same `applyPartial`/
`appliedConfig` pattern, duplicated rather than shared since the two binaries' sensor sets differ.
Signals (`signal.go`) are sinusoidal + drift + gaussian noise + step changes + rate-gated anomaly
spikes, across three sensors (`temperature`, `humidity`, and a vector `accel` — exercising both the
scalar and vector-flattening paths the "Data contract" section above describes). Misbehavior
(malformed payloads, latency jitter, clock skew, voluntary disconnects) is a live-tunable
`runtimeConfig` (`POST /fleet/config`), off by default.

- **Unlike every other compose service, `cmd/fleet` starts inert** (`FLEET_TARGET_DEVICES` defaults to
  0) — it's not part of the primary data path (see "What this is" above), so a fresh `docker compose up`
  shouldn't generate load. `manager.go`'s `FleetManager.Scale` brings it up/down on request
  (`POST /fleet/scale`), staggering new connections over `FLEET_RAMP_WINDOW` so a big jump doesn't open
  hundreds of TCP handshakes at once, and reuses a scaled-down device's cached credentials
  (`FLEET_STATE_DIR`, a bind mount — see below) rather than burning a fresh registration token on every
  restart.
- **Simulated network partition, not real network manipulation**: since a fleet of any size runs as
  goroutines inside one process rather than one container per device, `POST /fleet/partition`
  (`device.go`'s `sampleLoop`) just disconnects that device's own MQTT client for the requested
  duration and reconnects it on a timer — the realistic failure mode for a synthetic fleet is "the
  client's own connection drops." This also naturally exercises the retained-publish config channel: a
  config pushed while a device is partitioned is picked up immediately on reconnect via the existing
  resubscribe, no special-casing needed.
- **`chaos_data`/`fleet_data` are host bind mounts, not named Docker volumes** (see
  `deploy/docker-compose.yml`): `control` and `fleet` both run as distroless nonroot (uid 65532), and a
  fresh named volume is created root-owned with no `chown` mechanism available (no shell in the image)
  — found live as a `permission denied` writing the token file. Same reason `./certs` is already a bind
  mount rather than a volume.
- **`test/chaos`** has five scripts (`ramp.sh`, `kill_broker.sh`, `kill_processor.sh`, `pause_db.sh`,
  `partition.sh`) driving `cmd/fleet`'s control API and Docker directly (broker restart, processor
  SIGKILL, DB pause), each writing a timestamped CSV to `results/` (gitignored — reproducible run
  output, not committed) plus `render_charts.py` turning those into the P10 report charts. See
  `test/chaos/README.md`.
- **A real 1000-device ramp found the saturation point the P7 DoD asks for**: latency stays flat
  (~0.3–0.6s p99) through 200 devices, then saturates sharply at 400→600 — `sensegrid_ingest_lag_seconds`
  (the ingest bridge's own processing lag, not something downstream) jumps in lockstep with end-to-end
  p99, implicating the single-instance ingest bridge (no horizontal scaling) as the bottleneck resource
  rather than the broker or TimescaleDB, consistent with ~900 msg/s (600 devices × 3 sensors / 2s
  sample interval) approaching one Go process's ceiling.
- Also found running the chaos scripts against a real stack: `GET /v1/devices/drift`
  (`cmd/control/devices_handlers.go`) marshaled a nil slice to JSON `null` instead of `[]` when nothing
  had drifted, breaking any consumer doing `.devices[]` — fixed to `drifted := []deviceView{}`.

### Hardening & production readiness (Phase 8)

The phase the original brief skipped entirely — turning "it worked in the chaos test" into "here's
what we'd trust in production." Full evidence and numbers: `docs/phase8-hardening-report.md`. One-page
on-call runbook: `docs/runbook.md`. Reproducible drills: `test/hardening/` (same conventions as
`test/chaos/` — timestamped CSVs to `results/`, gitignored).

- **Backup/restore drill (`test/hardening/backup_restore.sh`) measured a 121s RTO** against a real
  1.3M-row dataset (Phase 7's chaos-test data, not a synthetic toy set): `pg_dump`, destroy
  `deploy_timescale_data` entirely, recreate, `timescaledb_pre_restore()` → `pg_restore` →
  `timescaledb_post_restore()`, verify. Every row count matched exactly pre/post (readings,
  `readings_1m`, `readings_1h`, devices, alerts), and compressed-chunk state plus all 6 background jobs
  survived the restore intact with no extra steps — TimescaleDB's dump/restore already carries
  continuous-aggregate materializations as regular tables, so "verify aggregates rebuild" turned out to
  mean "confirm the restore already includes them."
- **Compression/retention verification (`compression_retention.sh`) doesn't wait out `compress_after`/
  `drop_after`** (2/7 days — too long for a script): it calls the exact functions the policy jobs call,
  on demand. Result: **391MB → 28MB (92.8% storage saved), lossless** (row count unchanged), and a
  synthetic reading backdated 10 days was the only thing retention dropped — real data untouched.
- **The rate-limiter load test (`rate_limit_load.sh`) found a real bug**, not a hypothetical: paho's
  MQTT client defaults to `Order: true`, which routes every subscribed message through **one goroutine**
  so callbacks fire in receive order — `cmd/ingest`'s per-device token bucket (`ratelimit.go`) is
  correctly scoped per `device_id`, but it's only consulted *after* a message reaches `handle()`, so a
  flooding device's messages monopolized the one processing goroutine ahead of everyone else's
  regardless. Measured live: pushing one device to 2000Hz against a 20-device fleet made ingest's p99
  lag jump 6.7x (19ms→127ms) and the other 19 devices' data **stopped landing in the database entirely
  for minutes** (their fleet-side sequence counters kept climbing — they were still publishing — while
  their DB rows stayed byte-identical). Fixed with `SetOrderMatters(false)` in `cmd/ingest/main.go` —
  nothing in the pipeline depends on delivery order (JetStream publish is idempotent per
  `(device_id, sensor_type, seq, time)`, and `cmd/processor` already tolerates out-of-order arrival).
  Confirmed after the fix: normal devices landed 25,490 rows/60s while the rogue was shed at +42,144
  rate-limited, with no lag regression.
- **Verifying this by seq gaps (`max(seq) - count(distinct seq)`) is a trap**: `cmd/fleet` reuses cached
  device credentials across scale up/down, and `Reading.Seq` resets to 0 on every client restart (see
  the data contract above), so a reused `device_id` can have several non-contiguous seq ranges in
  `readings` from past test runs — `rate_limit_load.sh` measures by wall-clock time window instead,
  which isn't affected by that history.
- **Secrets review**: no committed secrets (grepped for credential-shaped strings across tracked
  files); `internal/config.Config.LogValue()` already redacts every secret field before structured
  logging (booleans, not values); `web/sensor-client`'s `localStorage` use is the device's own scoped
  MQTT credential (intended, same pattern as `~/.sensegrid/hostagent-device.json`), not a leak. The dev
  CA was rotated (`scripts/gen-certs.sh`, regenerate from scratch) and the whole stack verified
  end-to-end on the new certs.
- **Vulnerability scan (govulncheck + npm audit + Trivy on every built image): zero unaddressed
  CRITICALs.** `web/console`'s `next-auth@5.0.0-beta.25` — the console's actual login system, see
  "Console (Phase 5)" above — carried multiple CRITICAL auth-bypass CVEs (existence checks failing
  open, an email-normalizer homoglyph bypass); bumped to `5.0.0-beta.32`. Go stdlib had 8 reachable CVEs
  from a lagging toolchain, fixed by bumping every Dockerfile's builder to `golang:1.26-alpine` and
  `go.mod` to match. `mosquitto`/`timescaledb`'s Alpine base layers had 5/4 HIGH CVEs already fixed in
  Alpine's own repos but not yet in the floating upstream image tags — `apk upgrade` in both
  Dockerfiles. `console`'s runtime stage switched from `node:22-alpine` to
  `gcr.io/distroless/nodejs22-debian13:nonroot`, clearing a CRITICAL node-tar CVE (npm's own bundled
  deps — this image never invokes npm/yarn at runtime) and a CRITICAL OpenSSL CVE that debian12's
  distroless variant still carried. Two findings remain and are documented (not silently ignored) as
  accepted/upstream-owned: `timescaledb`'s vendored `gosu`/`timescaledb-tune`/`timescaledb-parallel-copy`
  binaries (old Go toolchain baked into the official `timescale/timescaledb` image, never executed with
  network input by our entrypoint) and Next.js's internally-pinned `postcss` (only ever processes this
  repo's own trusted CSS at build time, never untrusted input at request-serving runtime).

### Optional credibility layer — firmware (Phase 9)

`firmware/esp32` is an Arduino/PlatformIO ESP32 project, not a Go binary — it's a firmware-level device
speaking the exact same v1 wire contract as `cmd/hostagent`, so `cmd/control`/`cmd/ingest`/the console
need zero changes to accept it. Full build/run instructions: `firmware/esp32/README.md`.

- **Ported field-by-field from the Go reference implementations, not reimplemented from scratch**: the
  claim request/response shape from `cmd/control/claim.go`, `Reading` from `internal/telemetry`, and
  `Desired`/`Reported` from `internal/shadow` are all hand-mirrored in C++ since the ESP32 can't import
  a Go module — `main.cpp`'s `claimDevice()`/`publishReading()`/`mqttCallback()` are the C++ analogues
  of `internal/provisioning.claim`, `cmd/fleet/device.go`'s per-reading publish, and
  `cmd/hostagent/config.go`'s `applyPartial`/`toReported`, respectively.
- **Sampling is timer-interrupt-driven, not `delay()`-paced**: a hardware timer ISR just sets a
  `volatile` flag; the actual DHT22/ADC reads and MQTT publish happen in `loop()`, not inside the ISR
  — DHT22's bit-banged single-wire protocol needs interrupts briefly disabled during the read, which is
  unsafe (and non-reentrant) to do from inside another interrupt context.
- **Deliberately targets arduino-esp32 core 2.x's timer/watchdog API, not core 3.x's newer one** — found
  live: the officially-supported PlatformIO `espressif32` platform doesn't ship core 3.x at all (only a
  community fork does), so code written against 3.x's API (`timerBegin(freq_hz)`,
  `esp_task_wdt_config_t`) fails to compile on the toolchain most people, and Wokwi's own cloud build,
  actually have. `platformio.ini`'s comment has the details; this repo's firmware compiles clean against
  the standard toolchain as a result (verified: RAM 14.3%, flash 71.2%).
- **Verified against a live stack, not just written and hoped**: a client speaking this firmware's exact
  protocol (same auth, same topics, same JSON schema) claimed a device via `POST /v1/devices/claim`,
  connected over MQTT/TLS with the returned credentials, and published a reading that landed correctly
  in `readings` through the full ingest→JetStream→processor pipeline. `GET /v1/devices` — what the
  console's fleet view calls — then returned that device in the exact same shape as a phone or laptop,
  distinguished only by `type:"esp32"`, which is the Phase 9 DoD itself. What's *not* verified is an
  actual Wokwi run (no Wokwi account/API token available to automate one, and reaching a specific
  person's LAN/Private Gateway setup isn't something that can be verified in the abstract) — see the
  README's "What's actually been verified" section for the exact boundary.
- **Found and fixed a real gap in `scripts/gen-certs.sh` along the way**: `LAN_IP` was only ever added
  to `control`'s cert SAN, never `mosquitto`'s. Invisible to a browser (an extra click-through on a
  hostname mismatch, same as the untrusted-CA warning it already shows), but a hard TLS failure for any
  client that verifies properly with no click-through escape hatch — Go's `crypto/tls`
  (`cmd/hostagent`, `cmd/fleet`) and this firmware's `WiFiClientSecure` both fall into that category.
  Fixed by adding `LAN_IP` to both certs' SANs when set.

### Report & submission artifacts (Phase 10)

`docs/phase10-report.md` is the deliverable — the Blueprint's P10 checklist (architecture diagram
with a per-hop latency budget, latency baseline vs. under load, the scaling curve with its
saturation point explained, a failure-recovery table, the backup/restore RTO, a design-decisions
write-up, and an honest scope statement), assembled from evidence gathered live, not cited from
memory or fabricated.

- **Three of the four chaos drills had never actually been run before this phase** — only
  `ramp.sh` had (Phase 7). `kill_broker.sh`, `kill_processor.sh`, `pause_db.sh`, and `partition.sh`
  were all executed fresh against the real stack for this report: 100-device fleet, broker restart
  (11s to 100/100 reconnected), processor SIGKILL (6s catchup), TimescaleDB pause (15s catchup, 0
  duplicate rows — the `ON CONFLICT DO NOTHING` idempotency guard held), and a network partition +
  shadow config push (3s to converge). All effectively zero data loss.
- **Found and fixed a real bug in the chaos-test harness itself**: `test/chaos/lib.sh`'s
  `verify_no_seq_gaps` compared each drill's freshly-reset `fleet_last_seq` against
  `count(DISTINCT seq)`/`max(seq)` over a device's *entire* history in `readings`, with no time
  bound. On a stack that already had Phase 7/8 data in it, one device_id reused from the original
  1000-device ramp test (five days earlier) had accumulated seq values into the hundreds of
  thousands across past runs, producing a first measured "total_gap" of 43,736 — driven entirely by
  stale history, not the failure under test. Several other devices in the same reused fleet also
  carried **retained shadow config** from an old test (a `sample_rate_hz` pushed and never reset),
  publishing 10–60x faster than the rest of the fleet. Fixed two ways: `verify_no_seq_gaps` now
  takes an optional `since` timestamp scoping its DB query to `time >= since` (same fix pattern
  Phase 8's report already applied to `rate_limit_load.sh` for an identical class of problem), and
  the fleet's real credential cache (`test/chaos/fleet-data/fleet-devices/`, a host bind mount —
  not the same thing as the unused `deploy_fleet_data` Docker volume, which cost one wasted lookup
  to discover) was wiped so every device in the report's numbers is a fresh claim.
- **The per-hop latency budget is backed by a live Jaeger trace, not just the 500ms batch-flush
  design constant**: `processor.persist` fires ~417–421ms after `ingest.publish` (matching the
  "100 rows or 500ms, whichever first" flush trigger in `cmd/processor/consumer.go` almost exactly)
  while its own span duration is under 0.02ms — direct confirmation that the batch *wait*, not the
  DB write, is the practical cost of the persistence hop. The report also reconciles why this figure
  is higher than the ramp baseline's measured p50 (0.156s): the trace was captured against a
  deliberately light 5-device fleet, where the 500ms timer dominates almost completely, versus the
  ramp baseline's 10 devices producing just enough throughput for a mix of size- and time-triggered
  flushes.
- **`test/chaos/render_charts.py` gained a fourth chart function**
  (`chart_latency_baseline_vs_load`), reusing the same `ramp_*.csv` data as the scaling-curve chart
  rather than a one-off script, so regenerating the whole report's evidence is one command.

## Windows/Git Bash gotchas

These cost real time to find once; don't rediscover them.

- **`go test ./... -race` does not run in this dev environment**, and the failure looks like broken
  code rather than a broken toolchain: every package fails with `# runtime/cgo` / `cc1.exe: sorry,
  unimplemented: 64-bit mode not compiled in`. The race detector requires cgo, cgo requires a C
  compiler matching `GOARCH`, and the `gcc` on PATH here is MinGW.org's GCC 6.3.0 — a 32-bit-only
  build (`gcc -dumpmachine` → `mingw32`) while Go targets `amd64`. Nothing is wrong with the code:
  plain `go test ./...` passes every package. Race coverage is real but happens in CI, which runs
  `go test ./... -race` on `ubuntu-latest` (`.github/workflows/ci.yml`). To get it locally you'd
  need a 64-bit toolchain (mingw-w64 / MSYS2's `mingw-w64-x86_64-gcc`) ahead of MinGW.org's gcc on
  PATH — worth doing before trusting a local run, not worth blocking on otherwise.
- **MSYS path mangling**: Git Bash rewrites any bare argument starting with `/` into a Windows path
  before handing it to a non-MSYS executable (`docker exec ... /app ...` becomes `docker exec ...
  C:/Program Files/Git/app ...`). Symptoms: `stat ... no such file or directory` for a path that
  obviously exists in the container. Fix: `export MSYS_NO_PATHCONV=1` before the command. Also bit
  `scripts/gen-certs.sh`'s openssl subject strings (`/CN=...`) — already handled there via
  `MSYS_NO_PATHCONV=1`/`MSYS2_ARG_CONV_EXCL="*"` at the top of the script.
- **curl + the dev CA on Windows**: Git Bash's `curl.exe` uses the Windows Schannel backend, which does
  OCSP/revocation checking that fails against our throwaway CA with no revocation infrastructure. Add
  `--ssl-no-revoke` (Schannel-specific flag) when curling `https://localhost:8090/...` manually.
- **openssl `-extfile <(...)`**: process substitution doesn't work reliably through this openssl/MSYS
  combo (`Can't open "/dev/fd/63"`). `gen-certs.sh` writes a real temp extfile instead — don't
  "simplify" it back to process substitution.
- **Docker ENTRYPOINT resets CMD**: declaring a new `ENTRYPOINT` in a derived Dockerfile (e.g.
  `mosquitto.Dockerfile`'s bootstrap wrapper) drops the base image's inherited `CMD` — confirmed via
  `docker inspect` showing `Cmd: null`. Any Dockerfile that sets a custom `ENTRYPOINT` must restate
  `CMD` explicitly right after, or the container runs the entrypoint script and then exits 0 with
  nothing actually started.
- **paho.mqtt.golang: `SetAutoReconnect` doesn't cover the first connection.** It only handles
  reconnecting after a *previously successful* connection drops. A fresh `Connect()` call that fails
  outright (e.g. `cmd/ingest` starting before `cmd/control` has provisioned its bridge account) needs
  `SetConnectRetry(true)` separately — found live when `ingest` failed to start deterministically on a
  cold `docker compose up`.
- **Distroless containers have no shell.** `gcr.io/distroless/static-debian12:nonroot` (every Go
  service's runtime image) can't be `docker exec`'d into for debugging and can't run a classic
  `CMD-SHELL` healthcheck. Mosquitto's healthcheck uses a plain `nc -z` TCP check for exactly this
  class of reason (also because dynsec has no anonymous access to probe with). To run a CLI subcommand
  inside a running container image (e.g. `control token create`), exec the binary directly —
  `docker compose exec control /app token create ...` — not a shell.
- **Docker Desktop isn't always running.** `docker info` failing with a `dockerDesktopLinuxEngine` pipe
  error means the daemon isn't up, not that Docker is broken — start Docker Desktop and wait
  (`until docker info >/dev/null 2>&1; do sleep 5; done`).
- **`MSYS_NO_PATHCONV` is all-or-nothing per command line, not per-argument** (found writing
  `test/chaos`'s scripts). Exporting it globally to stop `docker compose exec ... /app ...`'s
  container-internal path from being rewritten (see the first gotcha above) also stops `docker
  compose`'s own `-f`/`--env-file` *host* paths from being converted, corrupting them into
  `C:\c\Users\...` instead of erroring cleanly. Fix: pre-convert those two paths once via `cygpath -w`
  (`test/chaos/lib.sh`), so the global export becomes safe for both at once.
- **`docker compose exec` forwards the calling shell's stdin by default, even with `-T`.** Called from
  inside a `while read ... done <<< "$multiline_var"` loop, it silently drains the *rest* of the
  here-string on its first invocation — no error, the loop just quietly runs once instead of N times.
  `test/chaos/lib.sh`'s `compose()` wrapper redirects `< /dev/null` for exactly this reason; none of
  its callers need real stdin anyway.
- **Multi-line `jq -r` output can carry an embedded `\r` per line** somewhere in the curl/docker/jq
  chain on this platform, which `read -r`/`mapfile` both preserve (they don't strip CRs, only disable
  backslash escaping). Invisible in a terminal; turns a syntactically valid UUID into one Postgres
  rejects outright. Strip with `| tr -d '\r'` right after any multi-line `jq -r` call feeding a loop or
  an exact-match comparison — single-value extractions via plain `$(...)` are unaffected, since command
  substitution's trailing-newline trim happens to eat a trailing `\r\n` too; it's only *internal* CRs
  between lines that survive.
- **`curl.exe`'s `-o /dev/null` intermittently exits 23 ("write error") on this platform**, and a
  `curl` call can fail with exit 56 ("recv error") against a service that's fully healthy a moment
  later, especially once `cmd/fleet` is pushing real load (seen at 200+ simulated devices). Avoid
  `-o /dev/null` (fold the response body and a `-w` status code into one captured stream instead), and
  wrap any network call a long-running script depends on in a retry with backoff
  (`test/chaos/lib.sh`'s `curl_retry`) — under `set -e`, one transient failure otherwise takes down an
  entire multi-minute chaos run.

## Phase status

| Phase | Tag | Shipped |
|---|---|---|
| 0 — Foundations | `v0.1-phase0` | Repo scaffold, 5 service skeletons (`/healthz`, graceful shutdown, structured logging), TLS-enabled compose stack (dev CA, TLS on Mosquitto + TimescaleDB). |
| 1 — Edge clients & provisioning | `v0.2-phase1` | PWA sensor client (hand-rolled MQTT-over-WS, no MQTT.js dependency), device claim flow, Mosquitto dynamic-security auth, `cmd/hostagent` real CPU/mem/battery/WiFi metrics. |
| 2 — Ingest, persistence, tracing | `v0.3-phase2` | `cmd/ingest` bridge, `cmd/processor` batched persistence, TimescaleDB schema + continuous aggregates + compression/retention, OTel tracing to Jaeger, Prometheus histograms (exposed, not yet scraped — that's Phase 6). |
| 3 — Stream processing & anomaly detection | `v0.4-phase3` | `internal/window` (Welford's incremental mean/variance + EWMA, count/age-bound sliding window per device/sensor), `internal/rules` (hot-reloadable YAML, `deploy/rules.yaml`), `internal/anomaly` (z-score/rate-of-change/silence detectors with M-consecutive hysteresis), `internal/alerts` (firing/acknowledged/resolved state machine, Postgres + `alerts.*` JetStream publish) — wired into `cmd/processor` as a second durable consumer of TELEMETRY (`windowed.go`) alongside the Phase 2 persistence consumer. `POST /v1/alerts/{id}/ack`/`/resolve` HTTP endpoints are deliberately deferred to Phase 4's `cmd/control` REST API. |
| 4 — Control plane & device shadow | `v0.5-phase4` | `internal/shadow` (JetStream KV desired/reported state, Postgres audit mirror, retained MQTT publish, state-report reconciler), JWT auth + role hierarchy in `cmd/control` (no login endpoint — `control jwt create` CLI, matching the existing `token create` pattern), device/shadow/drift/alert-ack REST endpoints, runtime config (sample rate/enabled sensors/batching) applied live in `cmd/hostagent` and the PWA (whose hand-rolled MQTT client gained SUBSCRIBE/incoming-PUBLISH support), and `internal/rollout` (staged rollout engine — cohort selection, stage/bake advancement, health-based auto-rollback via `internal/alerts`/`devices.last_seen`/rejection signals, Postgres-backed restart resumption). |
| 5 — Console | `v0.6-phase5` | `web/console` (Next.js + TypeScript + Tailwind), NextAuth login backed by a real `POST /v1/auth/login` + `internal/users` (`control user create` CLI), role-gated fleet/device-detail/alerts/rollouts views behind a server-side BFF proxy, `GET /v1/ws` (live `metrics.>`/`alerts.>`/`rollout.>` fan-out off NATS with reconnect-with-backoff and a visible degraded-connection state), client-side chart downsampling, and `GET /v1/alerts` (the first alert-listing endpoint, alongside the existing ack/resolve). See "Console (Phase 5)" above. |
| 6 — Full observability | `v0.7-phase6` | Prometheus scraping `ingest`/`processor`/`control` (the latter's first `/metrics`, on its own plain-HTTP port); `cmd/control`'s first OTel tracing plus a new `control.ws_relay` span extending a reading's trace through the WS relay; JetStream consumer-lag gauges on both `cmd/processor` consumers; Grafana provisioned entirely as code (`deploy/grafana`) — 3 dashboards (fleet-health, pipeline-latency, alerts) and 3 SLO alert rules (ingest p99 lag, end-to-end p99 latency, consumer lag). See "Observability (Phase 6)" above. |
| 7 — Synthetic fleet & chaos testing | `v0.8-phase7` | `cmd/fleet` turned into a real device simulator (claimed identity, MQTT/TLS, config subscription, shadow reporting, sinusoidal+drift+noise+anomaly signals across scalar/vector sensors, live-tunable misbehavior), inert by default and driven by its own HTTP control API (`/fleet/scale`, `/fleet/partition`, `/fleet/config`) instead of per-device containers; bulk token issuance (`control token create -count`); `test/chaos`'s five scripts (ramp/kill_broker/kill_processor/pause_db/partition) plus a chart renderer. A real 1000-device ramp found the saturation point (flat through 200 devices, sharp onset at 400–600, implicating the single-instance ingest bridge); broker-restart/processor-kill/DB-pause/partition runs all verified zero data loss. See "Synthetic fleet & chaos testing (Phase 7)" above. |
| 8 — Hardening & production readiness | `v0.9-phase8` | A measured 121s backup/restore RTO against a real 1.3M-row dataset; compression/retention verified live (92.8% storage saved, lossless) rather than just configured; a real bug found and fixed in the rate limiter (paho's `Order:true` default let a runaway device starve everyone else despite the per-device token bucket — fixed with `SetOrderMatters(false)`, confirmed live before/after); dev CA rotated; zero unaddressed CRITICALs after a govulncheck/npm-audit/Trivy pass across every built image (including a CRITICAL `next-auth` auth-bypass fix in the console). See "Hardening & production readiness (Phase 8)" above. |
| 9 — Optional credibility layer (firmware) | `v0.10-phase9` | `firmware/esp32`, an Arduino/PlatformIO ESP32 device speaking the exact v1 wire contract `cmd/hostagent` does — HTTPS claim cached in NVS, MQTT/TLS, timer-interrupt-driven DHT22+potentiometer sampling (not `delay()`), live `sample_rate_hz` config via the same `applyPartial`/`toReported` pattern as `cmd/hostagent/config.go`. Verified live: a client speaking its exact protocol claimed a device, published a reading that landed in TimescaleDB through the full pipeline, and showed up via `GET /v1/devices` exactly like a phone or laptop (`type:"esp32"`) — the Phase 9 DoD itself. Also fixed a real gap in `scripts/gen-certs.sh` (mosquitto's cert never got `LAN_IP` in its SAN, only control's did). See "Optional credibility layer — firmware (Phase 9)" above. |
| 10 — Report & submission artifacts | `v1.0-phase10`, `v1.0.1-phase10`, `v1.0.2-phase10` | `docs/phase10-report.md`: architecture diagram with a live-Jaeger-trace-confirmed per-hop latency budget, latency baseline vs. under load, the scaling/saturation curve, a failure-recovery table (broker restart, processor kill, DB pause, network partition — all effectively zero data loss), the Phase 8 backup/restore RTO recapped, a design-decisions write-up, and an honest scope statement. Ran three chaos drills live for the first time (`kill_broker.sh`/`kill_processor.sh`/`pause_db.sh`, only `ramp.sh` had run before) and found/fixed a real measurement bug in `test/chaos/lib.sh`'s seq-gap check along the way (a stale reused device_id from the Phase 7 ramp test produced a bogus 43,736-message "loss" reading before a time-scoped fix). See "Report & submission artifacts (Phase 10)" above. `v1.0.1-phase10` is a follow-up patch tag (not a new phase): bumped `.github/workflows/ci.yml`'s actions to their `node24`-native majors (fixing a Node 20 deprecation warning), which changed which golangci-lint major `version: latest` + `install-mode: goinstall` resolves to (v1.64.8 → v2.13.2) and surfaced 5 real unchecked-`Close()`-error findings, fixed with the same `defer func() { _ = x.Close() }()` pattern already used for `shutdownTracing` in `cmd/control/main.go`. `v1.0.2-phase10` is a second follow-up patch: added a top-level `README.md` (previously missing entirely) with the architecture, quickstart, and a full theory/mechanics/application write-up, committed the P10 report's chart PNGs (they were gitignored as "reproducible run output," making the report's embedded images broken links on GitHub), and fixed a real console bug — the sidebar nav rendering a sibling route's stale prefetched content on click, found by manually clicking through the console in a browser after the report was already done. See "Console (Phase 5)" above for the nav bug and the related stale-fleet-devices performance finding. |

## Where the full plan lives

The complete 11-phase implementation plan (non-functional targets, full data contracts, testing
strategy, risk register, timeline) is a published artifact: **SenseGrid Blueprint** —
`https://claude.ai/code/artifact/d2c73600-17fd-4f55-b2ce-45701954ca35`. This repo follows it, with
deviations noted inline in code comments where a real implementation detail changed the plan (dynsec
over classic ACL files, the custom migration runner over golang-migrate, Jaeger direct over a separate
OTel Collector, vector-reading flattening) — those comments are the source of truth over the blueprint
where they disagree, since they reflect what actually got built and verified.
