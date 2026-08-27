# SenseGrid on-call runbook

One page, three symptoms. Written so someone who has never touched this
repo before can act on it — every command assumes the Windows/Git Bash
gotchas in [`CLAUDE.md`](../CLAUDE.md) (`MSYS_NO_PATHCONV`, `--ssl-no-revoke`,
etc.) and can be run from the repo root with `.env` already in place.

## 1. Ingest lag is spiking (`sensegrid_ingest_lag_seconds` p99 alert)

`ingest_lag_seconds` is `broker_receive_time - device_time` for every
accepted reading (`cmd/ingest/handler.go`) — it goes up when the single
`cmd/ingest` process can't keep up with the MQTT bridge's telemetry
filter (`sensegrid/v1/+/telemetry`), not because of anything downstream.

1. **Check the Grafana pipeline-latency dashboard first**
   (`http://localhost:3300`) — is this every device, or one? A
   fleet-wide flat rise across the saturation curve (see CLAUDE.md's
   Phase 7 section — flat through ~200 devices, sharp onset 400→600) means
   you're just past `cmd/ingest`'s single-process ceiling; the only real
   fix today is horizontal scaling of the ingest bridge, which doesn't
   exist yet (tracked as a Phase 8+ gap, not something to firefight live).
2. **If it's a spike with no fleet-size change, suspect one runaway
   device.** Check `sensegrid_ingest_rate_limited_total` — if it's
   climbing fast, a single device is being correctly rate-shed
   (`cmd/ingest/ratelimit.go`, 100 msg/s + burst 200 per device_id) and
   shouldn't be affecting anyone else's lag (Phase 8 confirmed this
   isolation holds — see `test/hardening/rate_limit_load.sh` and its
   results). If lag is *also* climbing for other devices at the same
   time, something has regressed that isolation — check `cmd/ingest`'s
   MQTT client options in `main.go` still have `SetOrderMatters(false)`;
   that single line is what makes the isolation real (see its comment for
   why).
3. **Check `cmd/processor`'s consumer lag gauges**
   (`sensegrid_processor_consumer_lag` / `_windowed_consumer_lag`) — if
   *these* are climbing instead, the bottleneck is downstream of ingest,
   not ingest itself; see the JetStream stream state directly at
   `http://localhost:8222/jsz?consumers=true&streams=true` for
   `num_pending`/`num_ack_pending` per consumer.
4. **Check `cmd/processor` and TimescaleDB are actually up** —
   `docker compose -f deploy/docker-compose.yml --env-file .env ps`.
   A paused/restarting DB (see §2) will eventually show up here as lag
   too, just one hop further downstream.

## 2. A rollout won't advance (stuck in a stage, never bakes out)

`internal/rollout.Engine` re-evaluates every active rollout on a timer
(`ROLLOUT_TICK_INTERVAL`, default 10s) — advancement needs bake time to
elapse *and* health checks to pass.

1. **`GET /v1/rollouts/{id}`** (mint a token first: `docker compose ...
   exec control /app jwt create -role viewer`) — check `status` and
   `current_stage`. If `status` is `paused`, something already
   auto-rolled-back; check `GET /v1/alerts?device_id=...` for the
   targeted cohort around that time — a rollout pauses itself on the same
   health signals `internal/shadow.Drift` and `internal/alerts` already
   track (disconnect rate, alert rate for the cohort), not a separate
   health system.
2. **If `status` is `active` but `current_stage` hasn't moved past its
   bake window**, check the targeted cohort's actual state reports —
   `GET /v1/devices/drift`. A device that never applies the pushed
   config (never reports back `applied_revision` matching) counts against
   the rollout's disconnect rate the same as a truly offline device — the
   engine can't tell "device is slow" from "device is gone" other than by
   that same staleness threshold (`ROLLOUT_DISCONNECT_STALE_AFTER`).
3. **If a device is legitimately stuck rejecting the config** (`reject_reason`
   set in its `GET /v1/devices/{id}/shadow` reported state), that's a
   client-side validation failure, not a rollout-engine bug — the fix is
   in whatever config was pushed, not in the engine.

## 3. The console shows "degraded" (the live feed banner)

The console's `GET /v1/ws` connection (`cmd/control/ws_handler.go`)
reconnects with backoff on any disconnect; "degraded" means it's between
attempts, not that data is being silently dropped (there's no
replay/durability promise on this feed by design — see CLAUDE.md's
Console section).

1. **Check `cmd/control` is actually up and its NATS connection is
   healthy** — `docker compose ... logs control --tail 50` for connection
   errors to `nats://nats:4222`. The WS handler relays `metrics.>` /
   `alerts.>` / `rollout.>` off core NATS; if `cmd/control` itself lost
   its NATS connection, every WS client degrades simultaneously.
2. **Check it's not just this one browser tab** — `NEXT_PUBLIC_CONTROL_WS_URL`
   points at the *host-exposed* `wss://localhost:8090/v1/ws`, which needs
   the one-time dev-CA-trust click in that browser (see CLAUDE.md's TLS
   section). A single tab stuck on "degraded" while others are fine is
   almost always this, not a real outage.
3. **If it's every client and `cmd/control`/NATS both look healthy**,
   check `sensegrid_control_ws_clients_connected` in Grafana — a value of
   0 despite active browser tabs means the WS handler itself isn't
   accepting connections; check the JWT the browser session is presenting
   hasn't simply expired (`JWT_CONSOLE_TTL`, 12h default) — an expired
   token fails the WS upgrade's `requireRole` check the same way it would
   fail any REST call, just less obviously in the UI.

## Where the evidence for all of this lives

- `test/chaos/` — load/failure-recovery drills (Phase 7): saturation
  curve, broker/processor/DB failure recovery times, zero-data-loss
  verification.
- `test/hardening/` — backup/restore RTO, compression/retention
  verification, rate-limiter isolation (Phase 8).
- Both write timestamped CSVs to their own `results/` (gitignored,
  reproducible by re-running the scripts against a live stack — not
  meant to be read as a historical log).
