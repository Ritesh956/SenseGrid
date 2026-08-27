// SenseGrid ESP32 firmware — Phase 9's "optional credibility layer":
// ingest from an actual (simulated) firmware-level device, not just
// phones and native Go processes. Speaks the exact same v1 wire contract
// as cmd/hostagent and cmd/fleet (see internal/telemetry, internal/shadow)
// so cmd/control and cmd/ingest need zero changes to accept it — the
// whole point is that this device is indistinguishable from any other
// once claimed.
//
// Flow: WiFi connect -> claim a device identity over HTTPS (cached in NVS
// after the first boot, same as cmd/hostagent's ~/.sensegrid/
// hostagent-device.json) -> MQTT/TLS connect using that identity -> a
// hardware timer interrupt (not delay()) flags loop() to sample DHT22
// (temperature, humidity) + a potentiometer and publish each as its own
// v1 Reading -> a config-topic subscription applies sample_rate_hz
// changes live, matching cmd/hostagent's applyPartial/appliedConfig
// pattern (see cmd/hostagent/config.go) -> a task watchdog resets the
// device if anything above ever hangs instead of erroring out.
//
// See firmware/esp32/README.md for build/run instructions, the Wokwi
// setup, and the TLS/networking caveats.

#include <Arduino.h>
#include <WiFi.h>
#include <WiFiClientSecure.h>
#include <HTTPClient.h>
#include <PubSubClient.h>
#include <ArduinoJson.h>
#include <DHTesp.h>
#include <Preferences.h>
#include <esp_task_wdt.h>
#include <esp_random.h>
#include <time.h>
#include <sys/time.h>

#include "config.h"
#include "ca_cert.h"

// ---------------------------------------------------------------------
// Wiring (see diagram.json)
// ---------------------------------------------------------------------
static const int DHT_PIN = 15;
static const int POT_PIN = 34;

// ---------------------------------------------------------------------
// Wire schema constants (internal/telemetry, internal/shadow — kept in
// sync by hand since this firmware has no access to the Go module).
// ---------------------------------------------------------------------
static const char *SCHEMA_VERSION = "1.0";

// ---------------------------------------------------------------------
// Globals
// ---------------------------------------------------------------------
Preferences prefs;
WiFiClientSecure mqttTlsClient;
PubSubClient mqtt(mqttTlsClient);
DHTesp dht;

String deviceID;
String mqttUsername;
String mqttPassword;
char configTopic[64];
char telemetryTopic[64];
char stateTopic[64];

uint64_t seqCounter = 0;

hw_timer_t *sampleTimer = nullptr;
volatile bool samplePending = false;
uint32_t sampleIntervalMS = 2000; // default 0.5Hz, matches the other clients' default

uint64_t lastAppliedRevision = 0;
double lastSampleRateHz = 1000.0 / (double)sampleIntervalMS;

// ---------------------------------------------------------------------
// UUID v4 / trace_id — no crypto library needed, just RFC4122-shaped
// random bytes from the ESP32's hardware RNG (esp_random), which is what
// google/uuid.Parse on the server side actually needs to accept it.
// ---------------------------------------------------------------------
String randomHex(int numBytes) {
  uint8_t buf[16];
  esp_fill_random(buf, numBytes);
  static const char *hexchars = "0123456789abcdef";
  String out;
  out.reserve(numBytes * 2);
  for (int i = 0; i < numBytes; i++) {
    out += hexchars[buf[i] >> 4];
    out += hexchars[buf[i] & 0x0f];
  }
  return out;
}

String newUUIDv4() {
  uint8_t b[16];
  esp_fill_random(b, sizeof(b));
  b[6] = (b[6] & 0x0f) | 0x40; // version 4
  b[8] = (b[8] & 0x3f) | 0x80; // variant 10xx
  static const char *hexchars = "0123456789abcdef";
  char out[37];
  int pos = 0;
  for (int i = 0; i < 16; i++) {
    out[pos++] = hexchars[b[i] >> 4];
    out[pos++] = hexchars[b[i] & 0x0f];
    if (i == 3 || i == 5 || i == 7 || i == 9) {
      out[pos++] = '-';
    }
  }
  out[pos] = '\0';
  return String(out);
}

String newTraceID() {
  return randomHex(16); // 32 hex chars, matches internal/telemetry.NewTraceID
}

uint64_t nowEpochMS() {
  struct timeval tv;
  gettimeofday(&tv, nullptr);
  return (uint64_t)tv.tv_sec * 1000ULL + (uint64_t)tv.tv_usec / 1000ULL;
}

// ---------------------------------------------------------------------
// WiFi
// ---------------------------------------------------------------------
void connectWiFi() {
  Serial.printf("wifi: connecting to %s\n", WIFI_SSID);
  WiFi.mode(WIFI_STA);
  WiFi.begin(WIFI_SSID, WIFI_PASSWORD);

  uint32_t backoffMS = 1000;
  while (WiFi.status() != WL_CONNECTED) {
    esp_task_wdt_reset();
    delay(backoffMS);
    Serial.print(".");
    backoffMS = min(backoffMS * 2, (uint32_t)15000);
  }
  Serial.printf("\nwifi: connected, ip=%s\n", WiFi.localIP().toString().c_str());
}

// NTP sync is needed for device_time_ms to mean anything (a Reading with
// an epoch time from 1970 would fail nothing server-side — device_time_ms
// only needs to be positive — but ingest_lag_seconds would be nonsense,
// same class of problem the Blueprint's data contract cares about for
// every other client). Bounded wait with a watchdog feed, not a bare
// while(true) — an ESP32 with no internet-reachable NTP server (e.g. a
// misconfigured Wokwi Private Gateway) should still boot and try to
// operate rather than hang forever here.
void syncTime() {
  Serial.print("ntp: syncing");
  configTime(0, 0, "pool.ntp.org", "time.nist.gov");
  time_t now = time(nullptr);
  int attempts = 0;
  while (now < 8 * 3600 * 2 && attempts < 40) { // ~2 days epoch = "not synced yet"
    esp_task_wdt_reset();
    delay(500);
    Serial.print(".");
    now = time(nullptr);
    attempts++;
  }
  Serial.println(now >= 8 * 3600 * 2 ? " synced" : " timed out, continuing with unsynced clock");
}

// ---------------------------------------------------------------------
// Device claim (internal/provisioning's Go LoadOrClaim, ported) — claims
// once, caches in NVS (Preferences), matching every other native client's
// "claim once, reuse thereafter" contract.
// ---------------------------------------------------------------------
bool loadCachedCredentials() {
  deviceID = prefs.getString("device_id", "");
  mqttUsername = prefs.getString("mqtt_user", "");
  mqttPassword = prefs.getString("mqtt_pass", "");
  return deviceID.length() > 0 && mqttUsername.length() > 0 && mqttPassword.length() > 0;
}

bool claimDevice() {
  if (strlen(CLAIM_TOKEN) == 0) {
    Serial.println("claim: no cached credentials and CLAIM_TOKEN is empty — set one in config.h from `control token create -type esp32`");
    return false;
  }

  String newID = newUUIDv4();

  WiFiClientSecure client;
  client.setCACert(SENSEGRID_CA_CERT);

  HTTPClient http;
  char url[128];
  snprintf(url, sizeof(url), "https://%s:%d/v1/devices/claim", CONTROL_HOST, CONTROL_PORT);
  Serial.printf("claim: POST %s\n", url);
  if (!http.begin(client, url)) {
    Serial.println("claim: http.begin failed");
    return false;
  }
  http.addHeader("Content-Type", "application/json");
  http.setTimeout(15000);

  JsonDocument req;
  req["token"] = CLAIM_TOKEN;
  req["device_id"] = newID;
  String body;
  serializeJson(req, body);

  int code = http.POST(body);
  if (code != 200) {
    Serial.printf("claim: rejected, http %d: %s\n", code, http.getString().c_str());
    http.end();
    return false;
  }

  JsonDocument resp;
  DeserializationError err = deserializeJson(resp, http.getStream());
  http.end();
  if (err) {
    Serial.printf("claim: decoding response failed: %s\n", err.c_str());
    return false;
  }

  deviceID = resp["device_id"].as<String>();
  mqttUsername = resp["mqtt_username"].as<String>();
  mqttPassword = resp["mqtt_password"].as<String>();
  if (deviceID.length() == 0 || mqttUsername.length() == 0 || mqttPassword.length() == 0) {
    Serial.println("claim: response missing device_id/mqtt_username/mqtt_password");
    return false;
  }

  prefs.putString("device_id", deviceID);
  prefs.putString("mqtt_user", mqttUsername);
  prefs.putString("mqtt_pass", mqttPassword);
  Serial.printf("claim: claimed device_id=%s\n", deviceID.c_str());
  return true;
}

bool loadOrClaim() {
  if (loadCachedCredentials()) {
    Serial.printf("claim: using cached device_id=%s\n", deviceID.c_str());
    return true;
  }
  return claimDevice();
}

// ---------------------------------------------------------------------
// Shadow config (internal/shadow.Desired/Reported, ported from
// cmd/hostagent/config.go's applyPartial/toReported — same two message
// shapes, same "partial update, omit what you're not changing" contract).
// ---------------------------------------------------------------------
void publishReported(bool rejected, uint64_t rejectedRevision, const char *rejectReason) {
  JsonDocument doc;
  doc["schema_version"] = SCHEMA_VERSION;
  doc["applied_revision"] = lastAppliedRevision;
  doc["sample_rate_hz"] = lastSampleRateHz;
  if (rejected) {
    doc["rejected"] = true;
    doc["rejected_revision"] = rejectedRevision;
    doc["reject_reason"] = rejectReason;
  }
  doc["reported_at_ms"] = nowEpochMS();

  String body;
  serializeJson(doc, body);
  mqtt.publish(stateTopic, body.c_str(), false); // QoS 0, not retained — matches publishReportedValue
}

// mqttCallback handles the config topic — the only thing this device
// subscribes to. Applies a partial shadow.Desired update (only
// sample_rate_hz is meaningful for this firmware; enabled_sensors/
// batch_size/reporting_mode from the shared schema don't apply to a
// 2-sensor device with no batching, so they're accepted but ignored
// rather than rejected — same spirit as cmd/hostagent only acting on the
// fields it understands).
void mqttCallback(char *topic, byte *payload, unsigned int length) {
  JsonDocument doc;
  DeserializationError err = deserializeJson(doc, payload, length);
  if (err) {
    Serial.printf("config: unparseable desired config, ignoring: %s\n", err.c_str());
    return;
  }

  uint64_t revision = doc["revision"] | 0ULL;

  if (doc["sample_rate_hz"].is<double>()) {
    double rate = doc["sample_rate_hz"].as<double>();
    if (rate <= 0) {
      Serial.printf("config: rejecting revision %llu: sample_rate_hz must be positive\n", (unsigned long long)revision);
      publishReported(true, revision, "sample_rate_hz must be positive");
      return;
    }
    uint32_t newIntervalMS = (uint32_t)(1000.0 / rate);
    if (newIntervalMS < 1) {
      newIntervalMS = 1;
    }
    sampleIntervalMS = newIntervalMS;
    lastSampleRateHz = 1000.0 / (double)sampleIntervalMS;
    // timerAlarmWrite reconfigures the running timer's period in place —
    // no need to stop/detach/reattach the interrupt (core 2.x API, see
    // platformio.ini's comment for why not core 3.x's timerAlarm).
    timerAlarmWrite(sampleTimer, (uint64_t)sampleIntervalMS * 1000ULL, true);
    Serial.printf("config: applied revision %llu, sample_interval_ms=%u\n", (unsigned long long)revision, sampleIntervalMS);
  }

  lastAppliedRevision = revision;
  publishReported(false, 0, nullptr);
}

// ---------------------------------------------------------------------
// MQTT connect (mirrors cmd/hostagent's SetOnConnectHandler: subscribe to
// the config topic, then immediately publish current reported state —
// "on change/reconnect", not just on change).
// ---------------------------------------------------------------------
void connectMQTT() {
  uint32_t backoffMS = 1000;
  while (!mqtt.connected()) {
    esp_task_wdt_reset();
    Serial.printf("mqtt: connecting as %s\n", deviceID.c_str());
    // clientID == username == device_id: mosquitto's dynamic-security
    // plugin pins clientid to the claimed device_id (see
    // internal/dynsec/client.go's createClient) — using anything else
    // here gets the connection refused at the broker.
    if (mqtt.connect(deviceID.c_str(), mqttUsername.c_str(), mqttPassword.c_str())) {
      Serial.println("mqtt: connected");
      mqtt.subscribe(configTopic, 1);
      publishReported(false, 0, nullptr);
      return;
    }
    Serial.printf("mqtt: connect failed, rc=%d, retrying in %ums\n", mqtt.state(), backoffMS);
    delay(backoffMS);
    backoffMS = min(backoffMS * 2, (uint32_t)30000);
  }
}

// ---------------------------------------------------------------------
// Sensor sampling — triggered by the hardware timer ISR below, but the
// actual DHT22/ADC reads and MQTT publish happen here in loop(), not
// inside the ISR. DHT22's single-wire protocol is bit-banged with
// microsecond timing DHTesp handles by disabling interrupts briefly
// during the read; doing that *inside* an ISR context (nested,
// non-reentrant) is exactly the kind of thing that corrupts the read or
// wedges the interrupt controller, so the ISR only ever sets a flag.
// ---------------------------------------------------------------------
void publishReading(const char *sensorType, double value) {
  seqCounter++;

  JsonDocument doc;
  doc["schema_version"] = SCHEMA_VERSION;
  doc["device_id"] = deviceID;
  doc["sensor_type"] = sensorType;
  doc["value"] = value;
  doc["device_time_ms"] = nowEpochMS();
  doc["seq"] = seqCounter;
  doc["trace_id"] = newTraceID();

  String body;
  serializeJson(doc, body);
  mqtt.publish(telemetryTopic, body.c_str(), false); // QoS 0 publish, matching the PWA/hostagent's continuous mode
}

void sampleAndPublish() {
  TempAndHumidity th = dht.getTempAndHumidity();
  if (dht.getStatus() == DHTesp::ERROR_NONE) {
    publishReading("temperature", th.temperature);
    publishReading("humidity", th.humidity);
  } else {
    Serial.printf("dht22: read failed (%s), skipping this sample\n", dht.getStatusString());
  }

  int raw = analogRead(POT_PIN);
  double pct = (raw / 4095.0) * 100.0;
  publishReading("potentiometer", pct);
}

void IRAM_ATTR onSampleTimer() {
  samplePending = true;
}

// ---------------------------------------------------------------------
// setup / loop
// ---------------------------------------------------------------------
void setup() {
  Serial.begin(115200);
  delay(200);
  Serial.println("\nSenseGrid ESP32 firmware starting");

  // Watchdog first, generous timeout: covers WiFi/NTP/claim/MQTT connect
  // (which can legitimately take tens of seconds on a bad network) as
  // well as the steady-state loop(), where a stall really would mean
  // something's actually wedged. Core 2.x's simpler (timeout_seconds,
  // panic) signature — see platformio.ini's comment on why not core 3.x's
  // esp_task_wdt_config_t.
  esp_task_wdt_init(30, true);
  esp_task_wdt_add(NULL);

  prefs.begin("sensegrid", false);
  dht.setup(DHT_PIN, DHTesp::DHT22);

  connectWiFi();
  syncTime();

  if (!loadOrClaim()) {
    Serial.println("fatal: could not obtain device credentials, rebooting in 10s");
    delay(10000);
    ESP.restart();
  }

  snprintf(configTopic, sizeof(configTopic), "sensegrid/v1/%s/config", deviceID.c_str());
  snprintf(telemetryTopic, sizeof(telemetryTopic), "sensegrid/v1/%s/telemetry", deviceID.c_str());
  snprintf(stateTopic, sizeof(stateTopic), "sensegrid/v1/%s/state", deviceID.c_str());

  mqttTlsClient.setCACert(SENSEGRID_CA_CERT);
  mqtt.setServer(MQTT_HOST, MQTT_PORT);
  mqtt.setCallback(mqttCallback);
  mqtt.setBufferSize(1024); // default 256 is too small for our JSON payloads + topic
  connectMQTT();

  // timer 0, divider 80 against an 80MHz APB clock = 1MHz tick rate (1
  // tick = 1us), counting up — so the alarm value below is directly in
  // microseconds. Reconfigured live by mqttCallback when sample_rate_hz
  // changes, no detach/reattach needed.
  sampleTimer = timerBegin(0, 80, true);
  timerAttachInterrupt(sampleTimer, &onSampleTimer, true);
  timerAlarmWrite(sampleTimer, (uint64_t)sampleIntervalMS * 1000ULL, true);
  timerAlarmEnable(sampleTimer);

  Serial.println("setup complete");
}

void loop() {
  esp_task_wdt_reset();

  if (WiFi.status() != WL_CONNECTED) {
    Serial.println("wifi: connection lost, reconnecting");
    connectWiFi();
  }
  if (!mqtt.connected()) {
    connectMQTT();
  }
  mqtt.loop();

  if (samplePending) {
    samplePending = false;
    sampleAndPublish();
  }
}
