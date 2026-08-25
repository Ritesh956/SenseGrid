# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

SenseGrid is a real-time IoT telemetry and control platform, built phase-by-phase against a design
document (the "Blueprint" — see "Where the full plan lives" below). Real sensor data from a phone PWA
and a laptop host agent flows through MQTT → NATS JetStream → TimescaleDB, with a control plane that
provisions devices and (from Phase 4 on) pushes config back down to them. Synthetic load only enters
via `cmd/fleet` (Phase 7), not the primary data path — the phone and laptop are real hardware.

**Current status: Phases 0–2 tagged** (`v0.1-phase0`, `v0.2-phase1`, `v0.3-phase2`); **Phase 3
(windowed stream processing + anomaly detection) is implemented, not yet tagged** — see
`internal/window`, `internal/rules`, `internal/anomaly`, `internal/alerts`, and `cmd/processor`'s
second durable consumer (`windowed.go`). Phase 4 (control plane & device shadow) is next. See "Phase
status" below before assuming something isn't built yet — check git tags and `internal/` first.

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
- Every hop that touches a `Reading` emits an OTel span sharing the payload's `trace_id`, exported to
  Jaeger — pull a reading's whole journey by that ID at `http://localhost:16686`.

### Auth model (dynamic-security plugin — read this before touching anything MQTT-related)

Mosquitto has **no anonymous access anywhere**. Auth is entirely via the `dynamic-security` plugin,
controlled at runtime over MQTT itself (`$CONTROL/dynamic-security/v1`), not config files. Three
identities exist, each provisioned differently:

| Identity | Created by | Scope |
|---|---|---|
| **admin** | `deploy/mosquitto/entrypoint.sh` at first boot (offline, `mosquitto_ctrl dynsec init`) | Full control-channel access. Used only by `cmd/control` (`internal/dynsec`) to bootstrap the two roles below. |
| **device** (role) | `cmd/control` at startup, idempotent | Every claimed device gets this role. ACLs use `%c` substitution — a device can only touch `sensegrid/v1/{its-own-id}/*`. `username == clientid == device_id`, enforced by dynsec's `clientid` pinning on `createClient`. |
| **bridge** (role) | `cmd/control` at startup, idempotent | `cmd/ingest` connects as this (`MQTT_BRIDGE_USERNAME`/`PASSWORD` in `.env`, shared secret — **not** claimed at runtime like a device). ACL is a literal wildcard filter (`subscribeLiteral` on `sensegrid/v1/+/telemetry`), because a service needs to read *every* device's topic, which `%c` substitution can't express. |

**A new service that needs its own broker access** (e.g. Phase 4's control-plane MQTT client) needs a
new role + a new `MQTT_<X>_USERNAME/PASSWORD` pair added to `connectDynsec` in `cmd/control/main.go`
and `.env.example`, following the bridge pattern — do not reuse the bridge or device roles for a
different purpose.

Device provisioning flow (`cmd/control/claim.go`): registration token (Redis, `internal/devicestore`,
single-use, TTL'd) → `POST /v1/devices/claim` → **Postgres `devices` row created first**, dynsec
`createClient` second. That order is deliberate: if dynsec fails after the Postgres write, you get an
inert unclaimed device_id (harmless). The reverse order would let a device authenticate and publish
with no `devices` row, and every one of its readings would silently fail the `readings.device_id`
foreign key downstream, in a completely different service.

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
| `cmd/control` | 8090→8080 (HTTPS) | `deploy/docker/control.Dockerfile` | Own Dockerfile: also serves `web/sensor-client` and needs `deploy/migrations`. |
| `cmd/ingest` | 8081 | `deploy/docker/go.Dockerfile` (generic) | |
| `cmd/processor` | 8082 | `deploy/docker/processor.Dockerfile` | Own Dockerfile: needs `deploy/migrations`. |
| `cmd/fleet` | 8083 | `deploy/docker/go.Dockerfile` (generic) | Stub until Phase 7. |
| `cmd/hostagent` | 8084 | *(not containerized)* | Runs natively — needs real host CPU/battery/WiFi, which a container can't see. |
| mosquitto | 8883 (TLS), 9001 (WSS) | `deploy/docker/mosquitto.Dockerfile` | |
| NATS | 4222, 8222 (monitor) | image | JetStream on. |
| TimescaleDB | 5432 | `deploy/docker/timescaledb.Dockerfile` | |
| Redis | 6379 | image | Tokens only (see auth model). |
| Jaeger | 16686 (UI), 4317 (OTLP) | image | Accepts OTLP directly — no separate OTel Collector. |

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
| 3 — Stream processing & anomaly detection | *(implemented, untagged)* | `internal/window` (Welford's incremental mean/variance + EWMA, count/age-bound sliding window per device/sensor), `internal/rules` (hot-reloadable YAML, `deploy/rules.yaml`), `internal/anomaly` (z-score/rate-of-change/silence detectors with M-consecutive hysteresis), `internal/alerts` (firing/acknowledged/resolved state machine, Postgres + `alerts.*` JetStream publish) — wired into `cmd/processor` as a second durable consumer of TELEMETRY (`windowed.go`) alongside the Phase 2 persistence consumer. `POST /v1/alerts/{id}/ack`/`/resolve` HTTP endpoints are deliberately deferred to Phase 4's `cmd/control` REST API. |
| 4+ | *(not started)* | Control plane device shadow, staged rollouts, console, full observability, synthetic fleet + chaos testing, hardening, ESP32 firmware. |

## Where the full plan lives

The complete 11-phase implementation plan (non-functional targets, full data contracts, testing
strategy, risk register, timeline) is a published artifact: **SenseGrid Blueprint** —
`https://claude.ai/code/artifact/d2c73600-17fd-4f55-b2ce-45701954ca35`. This repo follows it, with
deviations noted inline in code comments where a real implementation detail changed the plan (dynsec
over classic ACL files, the custom migration runner over golang-migrate, Jaeger direct over a separate
OTel Collector, vector-reading flattening) — those comments are the source of truth over the blueprint
where they disagree, since they reflect what actually got built and verified.
