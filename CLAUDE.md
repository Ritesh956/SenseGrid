# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

SenseGrid is a real-time IoT telemetry and control platform, built phase-by-phase against a design
document (the "Blueprint" — see "Where the full plan lives" below). Real sensor data from a phone PWA
and a laptop host agent flows through MQTT → NATS JetStream → TimescaleDB, with a control plane that
provisions devices and (from Phase 4 on) pushes config back down to them. Synthetic load only enters
via `cmd/fleet` (Phase 7), not the primary data path — the phone and laptop are real hardware.

**Current status: Phases 0–6 tagged** (`v0.1-phase0`, `v0.2-phase1`, `v0.3-phase2`, `v0.4-phase3`,
`v0.5-phase4`, `v0.6-phase5`, `v0.7-phase6`). Phase 6 (full observability) scraped the Prometheus metrics
Phase 2 already exposed but never scraped, extended tracing and added a `/metrics` port to `cmd/control`
(the one service that had neither), and provisioned Grafana entirely as code (`deploy/grafana`) —
3 dashboards and 3 SLO alert rules — see "Observability (Phase 6)" below. Phase 7 (synthetic fleet &
chaos testing) is next. See "Phase status" below before assuming something isn't built yet — check git
tags and `internal/` first.

## Commands

```bash
# Build / test / vet everything
go build ./...
go vet ./...
gofmt -l .              # must be empty; gofmt -w . to fix
go test ./... -race

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

# Run hostagent natively against the compose stack (it is NOT a compose service — see below)
HOSTAGENT_CLAIM_TOKEN=<token from above> \
TLS_CA_FILE=deploy/certs/ca.pem \
go run ./cmd/hostagent
# second run onward needs no token — credentials cache at ~/.sensegrid/hostagent-device.json
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

`Reading` is the wire schema every publisher (PWA, hostagent, and eventually fleet/ESP32) uses:
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
| `cmd/fleet` | 8083 | `deploy/docker/go.Dockerfile` (generic) | Stub until Phase 7. |
| `cmd/hostagent` | 8084 | *(not containerized)* | Runs natively — needs real host CPU/battery/WiFi, which a container can't see. |
| mosquitto | 8883 (TLS), 9001 (WSS) | `deploy/docker/mosquitto.Dockerfile` | |
| NATS | 4222, 8222 (monitor) | image | JetStream on. |
| TimescaleDB | 5432 | `deploy/docker/timescaledb.Dockerfile` | |
| Redis | 6379 | image | Tokens only (see auth model). |
| Jaeger | 16686 (UI), 4317 (OTLP) | image | Accepts OTLP directly — no separate OTel Collector. |
| `web/console` | 3100→3000 | `deploy/docker/console.Dockerfile` | Next.js, not a Go binary — see "Console (Phase 5)" above for why it's structured differently from everything else in this table. |
| Prometheus | 9190→9090 | image | Scrapes `ingest:8081`, `processor:8082`, `control:9091` — see `deploy/prometheus/prometheus.yml` and "Observability" below. |
| Grafana | 3300→3000 | image | Dashboards + alert rules provisioned as code from `deploy/grafana/`, admin/admin. |

Shared `internal/` packages, briefly: `config` (env-driven `Config` struct, one `Load()` per service),
`logging` (slog JSON), `httpserver` (health server + graceful shutdown, optional TLS), `tlsutil` (dev
CA loading, shared by anything dialing the broker/API directly), `dynsec` (the dynamic-security control
protocol client — see auth model), `provisioning` (claim-flow client for native Go edge clients:
`hostagent` now, `fleet` later), `devices` (Postgres device registry), `devicestore` (Redis
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

## Windows/Git Bash gotchas

These cost real time to find once; don't rediscover them.

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
| 7+ | *(not started)* | Synthetic fleet + chaos testing, hardening, ESP32 firmware. |

## Where the full plan lives

The complete 11-phase implementation plan (non-functional targets, full data contracts, testing
strategy, risk register, timeline) is a published artifact: **SenseGrid Blueprint** —
`https://claude.ai/code/artifact/d2c73600-17fd-4f55-b2ce-45701954ca35`. This repo follows it, with
deviations noted inline in code comments where a real implementation detail changed the plan (dynsec
over classic ACL files, the custom migration runner over golang-migrate, Jaeger direct over a separate
OTel Collector, vector-reading flattening) — those comments are the source of truth over the blueprint
where they disagree, since they reflect what actually got built and verified.
