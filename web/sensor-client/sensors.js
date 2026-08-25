// Wraps the DeviceMotion/DeviceOrientation permission dance and samples
// readings at a configurable rate. Both events fire as fast as the device
// provides (often 60Hz); we throttle to the requested rate rather than
// relying on the browser to do it.

export async function requestMotionPermission() {
  const gated = (typeof DeviceMotionEvent !== "undefined" && typeof DeviceMotionEvent.requestPermission === "function")
    || (typeof DeviceOrientationEvent !== "undefined" && typeof DeviceOrientationEvent.requestPermission === "function");
  if (!gated) return { granted: true, gated: false };

  try {
    let granted = true;
    if (typeof DeviceMotionEvent !== "undefined" && typeof DeviceMotionEvent.requestPermission === "function") {
      granted = (await DeviceMotionEvent.requestPermission()) === "granted" && granted;
    }
    if (typeof DeviceOrientationEvent !== "undefined" && typeof DeviceOrientationEvent.requestPermission === "function") {
      granted = (await DeviceOrientationEvent.requestPermission()) === "granted" && granted;
    }
    return { granted, gated: true };
  } catch (err) {
    return { granted: false, gated: true, error: String(err) };
  }
}

// callback(sensorType, valueOrValues) is invoked at most once per
// (1000 / rateHz) ms per sensor type.
export class SensorSampler {
  constructor(rateHz, callback) {
    this.rateHz = rateHz;
    this.callback = callback;
    this._last = { accel: 0, gyro: 0, orientation: 0 };
    // enabled_sensors (Phase 4): all on by default, matching pre-Phase-4
    // behavior — a device that's never received a desired config samples
    // exactly as it always did.
    this._enabled = { accel: true, gyro: true, orientation: true };
    this._motionHandler = this._onMotion.bind(this);
    this._orientationHandler = this._onOrientation.bind(this);
  }

  setRate(rateHz) {
    this.rateHz = rateHz;
  }

  // setEnabledSensors(null) re-enables everything (the "omit the field"
  // convention shared with cmd/hostagent's applyPartial); otherwise only
  // sensor types present in `sensors` are sampled.
  setEnabledSensors(sensors) {
    if (!sensors) {
      this._enabled = { accel: true, gyro: true, orientation: true };
      return;
    }
    this._enabled = { accel: false, gyro: false, orientation: false };
    for (const s of sensors) {
      if (s in this._enabled) this._enabled[s] = true;
    }
  }

  start() {
    window.addEventListener("devicemotion", this._motionHandler);
    window.addEventListener("deviceorientation", this._orientationHandler);
  }

  stop() {
    window.removeEventListener("devicemotion", this._motionHandler);
    window.removeEventListener("deviceorientation", this._orientationHandler);
  }

  _throttle(key) {
    const now = performance.now();
    const minInterval = 1000 / this.rateHz;
    if (now - this._last[key] < minInterval) return false;
    this._last[key] = now;
    return true;
  }

  _onMotion(evt) {
    const accel = evt.acceleration && isFinite(evt.acceleration.x)
      ? evt.acceleration
      : evt.accelerationIncludingGravity;
    if (this._enabled.accel && accel && isFinite(accel.x) && this._throttle("accel")) {
      this.callback("accel", { x: accel.x, y: accel.y, z: accel.z });
    }
    if (this._enabled.gyro && evt.rotationRate && isFinite(evt.rotationRate.alpha) && this._throttle("gyro")) {
      this.callback("gyro", { x: evt.rotationRate.alpha, y: evt.rotationRate.beta, z: evt.rotationRate.gamma });
    }
  }

  _onOrientation(evt) {
    if (!this._enabled.orientation || !isFinite(evt.alpha) || !this._throttle("orientation")) return;
    this.callback("orientation", { x: evt.alpha, y: evt.beta, z: evt.gamma });
  }
}

// Battery/network aren't sampled at sensor rate — they're polled slowly by
// the caller (e.g. every 10s) and skipped entirely where unsupported.
export async function readBattery() {
  if (!navigator.getBattery) return null;
  try {
    const b = await navigator.getBattery();
    return b.level; // 0..1
  } catch {
    return null;
  }
}

export function readNetworkType() {
  return navigator.connection?.effectiveType ?? null;
}
