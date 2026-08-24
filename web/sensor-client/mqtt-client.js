// Minimal MQTT 3.1.1-over-WebSocket client: CONNECT, PUBLISH (QoS 0 only),
// PINGREQ/PINGRESP keepalive, auto-reconnect with backoff, and a bounded
// in-memory publish queue that drains on reconnect.
//
// Deliberately hand-rolled instead of vendoring a third-party MQTT.js
// bundle: this device only ever publishes (no subscribe in Phase 1), so
// the wire format needed is small enough to implement directly and verify
// against the MQTT 3.1.1 spec, with no external dependency to trust or to
// fetch at build/runtime.
//
// Packet framing verified against docs.oasis-open.org/mqtt/mqtt/v3.1.1/os/.

const MAX_QUEUE = 500;
const PING_INTERVAL_MS = 20000;
const PONG_GRACE_MS = 10000;
const RECONNECT_BASE_MS = 1000;
const RECONNECT_MAX_MS = 30000;

function encodeUTF8String(str) {
  const bytes = new TextEncoder().encode(str);
  const out = new Uint8Array(2 + bytes.length);
  out[0] = (bytes.length >> 8) & 0xff;
  out[1] = bytes.length & 0xff;
  out.set(bytes, 2);
  return out;
}

function encodeRemainingLength(n) {
  const out = [];
  do {
    let digit = n % 128;
    n = Math.floor(n / 128);
    if (n > 0) digit |= 0x80;
    out.push(digit);
  } while (n > 0);
  return Uint8Array.from(out);
}

function concatBytes(parts) {
  const total = parts.reduce((sum, p) => sum + p.length, 0);
  const out = new Uint8Array(total);
  let offset = 0;
  for (const p of parts) {
    out.set(p, offset);
    offset += p.length;
  }
  return out;
}

// Reads one complete MQTT packet from the front of buf, or returns null if
// buf doesn't yet contain a full packet (caller should wait for more data).
function decodeOnePacket(buf) {
  if (buf.length < 2) return null;
  const type = buf[0] >> 4;

  let multiplier = 1;
  let remainingLength = 0;
  let idx = 1;
  let encodedByte;
  do {
    if (idx >= buf.length) return null;
    encodedByte = buf[idx++];
    remainingLength += (encodedByte & 127) * multiplier;
    multiplier *= 128;
    if (multiplier > 128 * 128 * 128) throw new Error("mqtt: malformed remaining length");
  } while (encodedByte & 128);

  const totalLength = idx + remainingLength;
  if (buf.length < totalLength) return null;
  return { type, payload: buf.slice(idx, totalLength), totalLength };
}

export class MQTTClient {
  constructor(url, { clientId, username, password, keepAliveSec = 30 } = {}) {
    this.url = url;
    this.clientId = clientId;
    this.username = username;
    this.password = password;
    this.keepAliveSec = keepAliveSec;

    this.connected = false;
    this.stats = { sent: 0, queued: 0, dropped: 0, reconnects: 0 };
    this.onStatusChange = null; // (status: 'connecting'|'connected'|'disconnected'|'error', detail?) => void

    this._ws = null;
    this._rxBuf = new Uint8Array(0);
    this._queue = [];
    this._reconnectDelay = RECONNECT_BASE_MS;
    this._reconnectTimer = null;
    this._pingTimer = null;
    this._pongDeadline = null;
    this._closedByUser = false;
  }

  start() {
    this._closedByUser = false;
    this._connect();
  }

  stop() {
    this._closedByUser = true;
    clearTimeout(this._reconnectTimer);
    clearInterval(this._pingTimer);
    if (this._ws) this._ws.close();
  }

  publish(topic, payloadObj) {
    const bytes = new TextEncoder().encode(JSON.stringify(payloadObj));
    if (this.connected) {
      this._sendPublish(topic, bytes);
    } else {
      this._enqueue(topic, bytes);
    }
  }

  _enqueue(topic, bytes) {
    this._queue.push({ topic, bytes });
    this.stats.queued = this._queue.length;
    if (this._queue.length > MAX_QUEUE) {
      this._queue.shift();
      this.stats.dropped++;
    }
  }

  _flushQueue() {
    while (this._queue.length && this.connected) {
      const { topic, bytes } = this._queue.shift();
      this._sendPublish(topic, bytes);
    }
    this.stats.queued = this._queue.length;
  }

  _sendPublish(topic, payloadBytes) {
    const topicBytes = encodeUTF8String(topic);
    const body = concatBytes([topicBytes, payloadBytes]);
    const fixedHeader = concatBytes([Uint8Array.of(0x30), encodeRemainingLength(body.length)]);
    this._ws.send(concatBytes([fixedHeader, body]));
    this.stats.sent++;
  }

  _connect() {
    this._setStatus("connecting");
    let ws;
    try {
      ws = new WebSocket(this.url, ["mqtt"]);
    } catch (err) {
      this._setStatus("error", String(err));
      this._scheduleReconnect();
      return;
    }
    ws.binaryType = "arraybuffer";
    this._ws = ws;
    this._rxBuf = new Uint8Array(0);

    ws.onopen = () => {
      ws.send(this._buildConnect());
    };
    ws.onmessage = (evt) => this._onData(new Uint8Array(evt.data));
    ws.onerror = () => this._setStatus("error");
    ws.onclose = () => {
      const wasConnected = this.connected;
      this.connected = false;
      clearInterval(this._pingTimer);
      if (!this._closedByUser) {
        this._setStatus("disconnected");
        this._scheduleReconnect();
      } else if (wasConnected) {
        this._setStatus("disconnected");
      }
    };
  }

  _scheduleReconnect() {
    if (this._closedByUser) return;
    clearTimeout(this._reconnectTimer);
    this.stats.reconnects++;
    this._reconnectTimer = setTimeout(() => this._connect(), this._reconnectDelay);
    this._reconnectDelay = Math.min(this._reconnectDelay * 2, RECONNECT_MAX_MS);
  }

  _buildConnect() {
    const protocolName = encodeUTF8String("MQTT");
    const protocolLevel = Uint8Array.of(0x04);
    let flags = 0x02; // clean session
    if (this.username) flags |= 0x80;
    if (this.password) flags |= 0x40;
    const connectFlags = Uint8Array.of(flags);
    const keepAlive = Uint8Array.of((this.keepAliveSec >> 8) & 0xff, this.keepAliveSec & 0xff);

    const payloadParts = [encodeUTF8String(this.clientId)];
    if (this.username) payloadParts.push(encodeUTF8String(this.username));
    if (this.password) payloadParts.push(encodeUTF8String(this.password));

    const variableHeader = concatBytes([protocolName, protocolLevel, connectFlags, keepAlive]);
    const body = concatBytes([variableHeader, ...payloadParts]);
    const fixedHeader = concatBytes([Uint8Array.of(0x10), encodeRemainingLength(body.length)]);
    return concatBytes([fixedHeader, body]);
  }

  _onData(chunk) {
    this._rxBuf = concatBytes([this._rxBuf, chunk]);
    for (;;) {
      let pkt;
      try {
        pkt = decodeOnePacket(this._rxBuf);
      } catch (err) {
        this._setStatus("error", String(err));
        this._ws.close();
        return;
      }
      if (!pkt) return;
      this._rxBuf = this._rxBuf.slice(pkt.totalLength);
      this._handlePacket(pkt);
    }
  }

  _handlePacket({ type, payload }) {
    if (type === 2) {
      // CONNACK
      const returnCode = payload[1];
      if (returnCode === 0) {
        this.connected = true;
        this._reconnectDelay = RECONNECT_BASE_MS;
        this._setStatus("connected");
        this._startKeepalive();
        this._flushQueue();
      } else {
        this._setStatus("error", `broker rejected connection (code ${returnCode})`);
        this._ws.close();
      }
    } else if (type === 13) {
      // PINGRESP
      this._pongDeadline = null;
    }
    // PUBACK (4) and others are ignored: telemetry publishes at QoS 0.
  }

  _startKeepalive() {
    clearInterval(this._pingTimer);
    this._pingTimer = setInterval(() => {
      if (this._pongDeadline && Date.now() > this._pongDeadline) {
        this._setStatus("error", "keepalive timeout");
        this._ws.close();
        return;
      }
      this._ws.send(Uint8Array.of(0xc0, 0x00)); // PINGREQ
      this._pongDeadline = Date.now() + PONG_GRACE_MS;
    }, PING_INTERVAL_MS);
  }

  _setStatus(status, detail) {
    if (this.onStatusChange) this.onStatusChange(status, detail);
  }
}
