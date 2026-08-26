# test/chaos — Phase 7 load & chaos testing

Drives `cmd/fleet` (the synthetic device simulator) against a running
docker-compose stack to produce the load and failure-recovery evidence the
Blueprint's P7/P10 phases call for: a latency-vs-fleet-size curve, a
recovery-time table by failure mode, and a data-loss table by failure mode.

Every script writes a timestamped CSV to `results/`; `render_charts.py`
turns whatever's there into the report charts.

## One-time setup

```bash
cd SenseGrid
cp .env.example .env   # if you haven't already
bash scripts/gen-certs.sh
docker compose -f deploy/docker-compose.yml --env-file .env up -d --build
```

`cmd/fleet` starts inert (`FLEET_TARGET_DEVICES` defaults to 0 — it isn't
part of the primary data path, see `cmd/fleet/main.go`'s doc comment)
until scaled up. Scaling claims real devices, which needs registration
tokens; bulk-issue enough for the largest test you'll run (1000 for the
default ramp):

```bash
export MSYS_NO_PATHCONV=1
docker compose -f deploy/docker-compose.yml --env-file .env exec control \
  /app token create -name fleet -type fleet -ttl 6h -count 1000 \
  -out /chaos-data/fleet-tokens.txt
```

That's the same shared volume `cmd/fleet` reads from
(`FLEET_TOKENS_FILE=/chaos-data/fleet-tokens.txt`, wired in
`deploy/docker-compose.yml`) — nothing else to copy. Re-run with a larger
`-count` any time you need more headroom; already-claimed devices keep
their cached credentials (`fleet_data` volume) and don't consume new
tokens on a restart.

Requires `bash`, `curl`, `jq`, and `docker compose` on the host. Windows:
run from Git Bash — see `CLAUDE.md`'s "Windows/Git Bash gotchas" section;
`lib.sh` already handles the ones that bite these scripts
(`MSYS_NO_PATHCONV`, curl's Schannel revocation check).

## Scripts

All of them source `lib.sh` and are safe to re-run — they scale the fleet
to a fixed size at the start rather than assuming a particular starting
state.

| Script | What it does | Key env vars |
|---|---|---|
| `ramp.sh` | Scales 10→1000 (configurable), holding at each step to read steady-state latency/error-rate off Prometheus. **Run this first** — it's the P7 DoD's required saturation curve. | `RAMP_STEPS`, `RAMP_STABILIZE_S`, `RAMP_HOLD_S` |
| `kill_broker.sh` | Restarts Mosquitto mid-stream; every device loses its connection and has to reconnect on its own (`cmd/fleet/device.go`'s backoff reconnect). Measures recovery time, verifies zero loss. | `FLEET_SIZE`, `DRAIN_S` |
| `kill_processor.sh` | SIGKILLs `cmd/processor` while the fleet keeps publishing into JetStream unconsumed, then restarts it and watches the durable consumer drain the backlog. Verifies zero loss. | `FLEET_SIZE`, `OUTAGE_S`, `DRAIN_S` |
| `pause_db.sh` | Pauses (not restarts) TimescaleDB — `cmd/processor` should back off/nack rather than crash-loop or ack unpersisted writes. Verifies zero loss **and** no duplicate rows from JetStream redelivery. | `FLEET_SIZE`, `PAUSE_S`, `DRAIN_S` |
| `partition.sh` | Simulates a network partition for a device cohort via `cmd/fleet`'s own control API, pushes a shadow config change to exactly that cohort while it's unreachable, heals, and times convergence (`GET /v1/devices/drift` clearing). | `FLEET_SIZE`, `COHORT_SIZE`, `PARTITION_S` |

Example:

```bash
./ramp.sh
FLEET_SIZE=200 ./kill_processor.sh
FLEET_SIZE=200 COHORT_SIZE=40 PARTITION_S=45 ./partition.sh
```

Every script zeroes `cmd/fleet`'s misbehavior knobs
(`FLEET_MALFORMED_RATE` etc., via `POST /fleet/config`) before measuring,
so the numbers reflect the failure being tested, not synthetic noise —
malformed-payload injection is a separate, deliberate concern (see
`cmd/fleet/device.go`'s `malformedPayload`), not something to leave on
while proving zero data loss.

## Rendering charts

```bash
pip install pandas matplotlib
python render_charts.py
```

Writes `results/charts/latency_vs_fleet_size.png`,
`recovery_time_by_failure_mode.png`, and `data_loss_by_failure_mode.png`
from whatever's newest in `results/`. Any subset of the scripts having run
is fine — each chart just skips itself if its CSVs aren't there yet.

## Cleaning up

```bash
curl -X POST http://localhost:8083/fleet/scale -d '{"count":0}'
```

scales the fleet back to zero (each device disconnects cleanly) without
tearing down the rest of the stack.
