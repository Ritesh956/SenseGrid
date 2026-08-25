import { MQTTClient } from "./mqtt-client.js";
import { requestMotionPermission, SensorSampler, readBattery, readNetworkType } from "./sensors.js";
import { defaultConfig, applyPartial, toReported } from "./shadow-config.js";

const STORAGE_KEY = "sensegrid.device.v1";
const SCHEMA_VERSION = "1.0";
const BATTERY_POLL_MS = 15000;

const el = (id) => document.getElementById(id);
const claimView = el("claim-view");
const dashView = el("dash-view");

function loadDevice() {
  try {
    return JSON.parse(localStorage.getItem(STORAGE_KEY));
  } catch {
    return null;
  }
}

function saveDevice(d) {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(d));
}

function forgetDevice() {
  localStorage.removeItem(STORAGE_KEY);
  location.reload();
}

function randomTraceID() {
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  return [...bytes].map((b) => b.toString(16).padStart(2, "0")).join("");
}

// ---- claim flow -----------------------------------------------------

async function claim(token) {
  const deviceID = crypto.randomUUID();
  const resp = await fetch("/v1/devices/claim", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ token, device_id: deviceID }),
  });
  if (!resp.ok) {
    const body = await resp.json().catch(() => ({}));
    throw new Error(body.error || `claim failed (${resp.status})`);
  }
  return resp.json();
}

el("claim-form").addEventListener("submit", async (evt) => {
  evt.preventDefault();
  const token = el("token-input").value.trim();
  const errorEl = el("claim-error");
  const button = el("claim-submit");
  errorEl.textContent = "";
  button.disabled = true;
  button.textContent = "Registering…";
  try {
    const device = await claim(token);
    saveDevice(device);
    location.reload();
  } catch (err) {
    errorEl.textContent = err.message;
    button.disabled = false;
    button.textContent = "Register device";
  }
});

// ---- dashboard --------------------------------------------------------

function initDashboard(device) {
  el("device-name").textContent = device.name;
  el("device-type").textContent = device.type;
  el("device-id").textContent = device.device_id;

  let seq = 0;
  let sending = false;
  let sampler = null;
  let mqttClient = null;
  let batteryTimer = null;
  let cfg = defaultConfig(20); // reseeded from rateInput.value at start()
  let pending = [];
  let lastFlush = Date.now();

  const rateInput = el("rate-input");
  const permBtn = el("perm-btn");
  const stopBtn = el("stop-btn");
  const statusPill = el("status-pill");
  const errorEl = el("dash-error");
  const sentCountEl = el("sent-count");
  const queuedCountEl = el("queued-count");

  function setStatus(status, detail) {
    statusPill.textContent = status;
    statusPill.dataset.status = status;
    if (detail) errorEl.textContent = detail;
    if (status === "connected") errorEl.textContent = "";
  }

  // sensorAllowed gates a sensor_type against cfg.enabledSensors — null
  // means "all enabled" (the pre-Phase-4 default), matching
  // SensorSampler.setEnabledSensors' own convention for the same field.
  function sensorAllowed(sensorType) {
    return !cfg.enabledSensors || cfg.enabledSensors.includes(sensorType);
  }

  // enqueue either publishes immediately (reporting_mode "continuous",
  // today's pre-Phase-4 behavior) or buffers and flushes as a burst once
  // batch_size/flush_interval_ms is hit (see shadow-config.js) — the wire
  // shape of each individual telemetry message is unchanged either way,
  // only the timing of when it's sent.
  function enqueue(payload) {
    if (cfg.reportingMode !== "batched") {
      mqttClient.publish(`sensegrid/v1/${device.device_id}/telemetry`, payload);
      sentCountEl.textContent = mqttClient.stats.sent;
      queuedCountEl.textContent = mqttClient.stats.queued;
      return;
    }
    pending.push(payload);
    const sizeHit = cfg.batchSize > 0 && pending.length >= cfg.batchSize;
    const timeHit = cfg.flushIntervalMs > 0 && Date.now() - lastFlush >= cfg.flushIntervalMs;
    if (sizeHit || timeHit) flushPending();
  }

  function flushPending() {
    for (const payload of pending) {
      mqttClient.publish(`sensegrid/v1/${device.device_id}/telemetry`, payload);
    }
    pending = [];
    lastFlush = Date.now();
    sentCountEl.textContent = mqttClient.stats.sent;
    queuedCountEl.textContent = mqttClient.stats.queued;
  }

  function publishReading(sensorType, values) {
    if (!mqttClient || !sensorAllowed(sensorType)) return;
    seq += 1;
    enqueue({
      schema_version: SCHEMA_VERSION,
      device_id: device.device_id,
      sensor_type: sensorType,
      values,
      device_time_ms: Date.now(),
      seq,
      trace_id: randomTraceID(),
    });
  }

  function publishScalar(sensorType, value) {
    if (!mqttClient || value === null || value === undefined || !sensorAllowed(sensorType)) return;
    seq += 1;
    enqueue({
      schema_version: SCHEMA_VERSION,
      device_id: device.device_id,
      sensor_type: sensorType,
      value,
      device_time_ms: Date.now(),
      seq,
      trace_id: randomTraceID(),
    });
  }

  function publishReported(rejected, rejectedRevision, reason) {
    if (!mqttClient) return;
    const rep = toReported(cfg, rejected, rejectedRevision, reason);
    mqttClient.publish(`sensegrid/v1/${device.device_id}/state`, rep);
  }

  // handleConfigMessage applies (or rejects) an incoming desired-config
  // message — mirrors cmd/hostagent's configHandler in main.go. Runtime
  // effects (sample rate, enabled sensors) take effect immediately; the
  // wire schema and validation rules are shared with hostagent via
  // internal/shadow's Go types / shadow-config.js's JS mirror.
  function handleConfigMessage(_topic, payloadBytes) {
    let desired;
    try {
      desired = JSON.parse(new TextDecoder().decode(payloadBytes));
    } catch (err) {
      console.error("config: unparseable desired config", err);
      return;
    }

    const { next, error } = applyPartial(cfg, desired);
    if (error) {
      console.warn("config: rejecting desired config", error);
      publishReported(true, desired.revision, error);
      return;
    }

    cfg = next;
    if (sampler) {
      sampler.setRate(cfg.sampleRateHz);
      sampler.setEnabledSensors(cfg.enabledSensors);
    }
    publishReported(false);
  }

  async function start() {
    const { granted, error } = await requestMotionPermission();
    if (!granted) {
      setStatus("error", error || "Motion permission denied — check your browser's site settings.");
      return;
    }

    permBtn.hidden = true;
    stopBtn.hidden = false;
    rateInput.disabled = true;
    cfg = defaultConfig(Number(rateInput.value) || 20);
    pending = [];
    lastFlush = Date.now();

    mqttClient = new MQTTClient(`wss://${location.hostname}:9001`, {
      clientId: device.device_id,
      username: device.mqtt_username,
      password: device.mqtt_password,
    });
    mqttClient.onStatusChange = (status, detail) => {
      setStatus(status, detail);
      // Subscribing here (not once, right after start()) matches
      // mqtt-client.js's own resubscribe-on-every-connect behavior: the
      // very first CONNACK is itself a "connected" status change, so this
      // covers both the initial connect and every reconnect with the same
      // code path — no separate "first connect" special case needed.
      if (status === "connected") {
        mqttClient.subscribe(`sensegrid/v1/${device.device_id}/config`, 1);
        publishReported(false);
      }
    };
    mqttClient.onMessage = handleConfigMessage;
    mqttClient.start();

    sampler = new SensorSampler(cfg.sampleRateHz, publishReading);
    sampler.start();

    readNetworkTypeAndPublish();
    batteryTimer = setInterval(async () => {
      const level = await readBattery();
      publishScalar("battery", level);
      publishScalar("network_rtt_hint", readNetworkType() ? 1 : 0);
    }, BATTERY_POLL_MS);

    sending = true;
  }

  function readNetworkTypeAndPublish() {
    const net = readNetworkType();
    if (net) el("network-type").textContent = net;
  }

  function stop() {
    sending = false;
    if (sampler) sampler.stop();
    if (pending.length && mqttClient) flushPending(); // don't lose a partial batch on stop
    if (mqttClient) mqttClient.stop();
    clearInterval(batteryTimer);
    permBtn.hidden = false;
    stopBtn.hidden = true;
    rateInput.disabled = false;
    setStatus("disconnected");
  }

  rateInput.addEventListener("change", () => {
    if (sampler) sampler.setRate(Number(rateInput.value) || 20);
  });

  permBtn.addEventListener("click", start);
  stopBtn.addEventListener("click", stop);
  el("forget-btn").addEventListener("click", forgetDevice);

  setInterval(() => {
    if (mqttClient && sending) {
      sentCountEl.textContent = mqttClient.stats.sent;
      queuedCountEl.textContent = mqttClient.stats.queued;
    }
  }, 1000);
}

// ---- boot ---------------------------------------------------------------

const existing = loadDevice();
if (existing && existing.device_id) {
  claimView.hidden = true;
  dashView.hidden = false;
  initDashboard(existing);
} else {
  claimView.hidden = false;
  dashView.hidden = true;
}
