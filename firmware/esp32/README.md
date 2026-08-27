# SenseGrid ESP32 firmware (Phase 9)

An ESP32 running this firmware is a normal SenseGrid device — same claim
flow, same v1 telemetry/shadow wire schema, same MQTT topics as
`cmd/hostagent` or the PWA sensor client. `cmd/control`/`cmd/ingest`/the
console need zero changes to accept it. See `CLAUDE.md`'s "Optional
credibility layer — firmware (Phase 9)" section for the full picture; this
file is just build/run instructions.

## What it does

- Connects to WiFi, syncs time over NTP (needed for `device_time_ms` to
  mean anything).
- Claims a device identity over HTTPS (`POST /v1/devices/claim`), caching
  the result in NVS so only the *first* boot needs a registration token —
  same contract as `internal/provisioning.LoadOrClaim` (the Go version of
  this), just reimplemented in C++ since the ESP32 can't import a Go
  module.
- Connects to mosquitto over MQTT/TLS using the claimed credentials.
- A hardware timer interrupt (not `delay()` — see `main.cpp`'s comments)
  flags `loop()` to sample a DHT22 (temperature + humidity) and a
  potentiometer, publishing each as its own `internal/telemetry.Reading`.
- Subscribes to its config topic and applies `sample_rate_hz` changes to
  the timer live, reporting applied/rejected state back — the same
  `applyPartial`/`toReported` pattern as `cmd/hostagent/config.go`.
- A task watchdog resets the device if anything hangs.

## One-time setup

```bash
cd firmware/esp32
cp src/config.h.example src/config.h
cp src/ca_cert.h.example src/ca_cert.h
```

Edit `src/ca_cert.h`: paste in the contents of `deploy/certs/ca.pem`
(generate it first with `bash scripts/gen-certs.sh` from the repo root, if
you haven't already — same dev CA every other SenseGrid client trusts).

Edit `src/config.h`: set `CONTROL_HOST`/`MQTT_HOST` to wherever the ESP32
can actually reach your stack (see "Reaching your stack" below), and mint
a one-time `CLAIM_TOKEN`:

```bash
export MSYS_NO_PATHCONV=1
docker compose -f ../../deploy/docker-compose.yml --env-file ../../.env exec control \
  /app token create -name esp32-1 -type esp32 -ttl 1h
```

## Building

```bash
pip install platformio   # one-time
cd firmware/esp32
pio run                  # compiles; .pio/build/esp32dev/firmware.bin + .elf
```

Verified: this compiles clean against the officially-supported PlatformIO
`espressif32` platform (RAM 14.3%, flash 71.2% on the default 4MB
partition scheme). See `platformio.ini`'s comment for why the firmware
deliberately targets arduino-esp32 core 2.x's timer/watchdog API rather
than core 3.x's — the official PlatformIO platform doesn't ship core 3.x
(only a community fork does), so 3.x's API would fail to compile on the
toolchain most people, and Wokwi's own cloud build, actually have.

## Running in Wokwi

Open this folder in [Wokwi](https://wokwi.com) (import from GitHub, or
`Wokwi: Start Simulator` from the [VS Code
extension](https://docs.wokwi.com/vscode/getting-started) with `pio run`
already having produced the `.bin`/`.elf` `wokwi.toml` points at) — no
local toolchain needed beyond that one build step, and Wokwi's own cloud
build can do even that for a GitHub-hosted project. `diagram.json` wires
an ESP32 DevKit V1 to a `wokwi-dht22` (pin D15) and a `wokwi-potentiometer`
(pin D34); drag the potentiometer's knob or edit the DHT22's
temperature/humidity attributes in Wokwi's UI to see live values flow
through.

### Reaching your stack

Wokwi's simulated WiFi (`Wokwi-GUEST`) has real internet access and can
reach public MQTT brokers directly — confirmed working for the MQTT/TLS
handshake itself. Reaching *your* local docker-compose stack specifically
needs one of:

- **[Wokwi's Private Gateway](https://docs.wokwi.com/guides/private-gateway)**
  — a small local app that routes the simulator's virtual network to your
  LAN. Run it, then set `CONTROL_HOST`/`MQTT_HOST` in `config.h` to your
  laptop's LAN IP. **Regenerate certs with the LAN IP in their SAN first**
  — `LAN_IP=<your-ip> bash scripts/gen-certs.sh` (after
  `rm deploy/certs/mosquitto.* deploy/certs/control.*` if they already
  exist) — this firmware's `WiFiClientSecure` verifies the certificate
  chain properly and doesn't have a "click through the warning" escape
  hatch the way a browser does, so a cert missing the LAN IP in its SAN
  fails the TLS handshake outright rather than just warning. This gap
  (mosquitto's cert not covering `LAN_IP` at all) was found and fixed as
  part of this phase — see `scripts/gen-certs.sh`'s comments.
- A public tunnel (ngrok, Cloudflare Tunnel, etc.) exposing mosquitto's
  8883 and control's 8090 — only worth it for a demo you want reachable
  without the Private Gateway app; not something this repo sets up for
  you, since it means exposing your dev stack to the public internet.

### What's actually been verified vs. what needs your own Wokwi run

**Verified against a live stack in this session** — not just eyeballed:

1. The firmware compiles cleanly against a real PlatformIO/arduino-esp32
   toolchain (RAM 14.3%, flash 71.2%).
2. `POST /v1/devices/claim` with a real `-type esp32` token returned
   exactly the `{device_id, name, type, mqtt_username, mqtt_password,
   mqtt_ws_url}` shape `main.cpp`'s `claimDevice()` parses.
3. Connecting to mosquitto over MQTT/TLS with those exact credentials
   (clientID = username = device_id, CA-verified, no client cert) —
   the same auth this firmware's `connectMQTT()` performs — succeeded,
   and publishing a `Reading` to `sensegrid/v1/{id}/telemetry` plus
   subscribing to `sensegrid/v1/{id}/config` both worked.
4. That published reading landed correctly in `readings`
   (`device_id, sensor_type, value, seq` all matched what was sent),
   proving the full pipeline — mosquitto → ingest → JetStream →
   processor → TimescaleDB — accepts this device exactly like any other.
5. **The Phase 9 DoD itself**: `GET /v1/devices` (what the console's
   fleet view calls) returned this device as
   `{"id":"...", "name":"esp32-test", "type":"esp32", "status":"claimed",
   "registered_at":"..."}` — the same shape as a phone or laptop device,
   distinguished only by `type`.

This was done with a Node MQTT client speaking the identical wire
protocol (topics, auth, JSON schema) rather than a literal ESP32/Wokwi
run, since this session has no Wokwi account/API token to automate one
and no way to simulate *your* specific LAN/Private Gateway setup. What's
unverified is purely "does the actual C++ on real (simulated) ESP32
hardware execute this same protocol correctly" — the server-side
acceptance and the wire contract are both confirmed for real. Opening
this in Wokwi yourself (see above) is the remaining step, and per the
above it should Just Work against a reachable stack. If the claim step
fails, check `cmd/control`'s logs first; if TLS fails outright (not just
a slow connect), it's almost always the cert SAN not covering the address
in `config.h`.

## Known gaps

- No mutual TLS (client cert) — same as every other SenseGrid device,
  server-auth-only TLS matching `internal/tlsutil`'s posture.
- `enabled_sensors`/`batch_size`/`reporting_mode` in the shared
  `shadow.Desired` schema are accepted (so a config push targeting this
  device alongside others doesn't get rejected) but not acted on — this
  firmware always publishes continuously with both sensors enabled.
  Same "accept what you don't understand, act on what you do" spirit as
  `cmd/hostagent`, just a smaller feature set.
- No OTA update mechanism — out of scope for a credibility-layer device.
