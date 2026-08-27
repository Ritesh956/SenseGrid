#!/usr/bin/env bash
# Generates a throwaway dev CA plus per-service TLS certs for local
# docker-compose. Never use these certs outside local development — the CA
# key sits unencrypted on disk in deploy/certs/, which is gitignored.
set -euo pipefail

# Git Bash (MSYS) rewrites leading "/CN=..." style openssl subjects as
# Windows paths; this disables that path-conversion for this script.
export MSYS_NO_PATHCONV=1
export MSYS2_ARG_CONV_EXCL="*"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CERT_DIR="$ROOT_DIR/deploy/certs"
mkdir -p "$CERT_DIR"
cd "$CERT_DIR"

# LAN_IP: set this to your laptop's LAN IP (e.g. LAN_IP=192.168.1.23) to
# also cover it in the control-plane and mosquitto certs' SANs, so a phone
# or real device on the same network sees only an "untrusted CA" warning
# instead of also a hostname mismatch. Either way the warning is one
# "proceed anyway" click for a browser; this just removes the second one —
# but it's not optional for cmd/hostagent, cmd/fleet, or the Phase 9 ESP32
# firmware's mosquitto connection specifically, since none of those clients
# skip hostname/SAN verification the way a browser's click-through can (see
# internal/tlsutil.FromCAFile / firmware/esp32's WiFiClientSecure — both
# verify the cert chain against the CA, and mbedTLS's default is stricter
# about the SAN actually covering the address dialed). Re-run with
# `rm deploy/certs/mosquitto.* deploy/certs/control.*` first to pick up a
# changed LAN_IP.
: "${LAN_IP:=}"

if [ ! -f ca.pem ]; then
  echo "generating dev CA..."
  openssl genrsa -out ca.key 4096 >/dev/null 2>&1
  openssl req -x509 -new -nodes -key ca.key -sha256 -days 825 \
    -subj "/CN=SenseGrid Dev CA" -out ca.pem >/dev/null 2>&1
fi

issue() {
  local name="$1" sans="$2"
  if [ -f "${name}.pem" ]; then
    echo "  ${name}.pem already exists, skipping"
    return
  fi
  local extfile="${name}.ext.cnf"
  printf "subjectAltName=%s\n" "$sans" > "$extfile"
  openssl genrsa -out "${name}.key" 2048 >/dev/null 2>&1
  openssl req -new -key "${name}.key" -subj "/CN=${name}" -out "${name}.csr" >/dev/null 2>&1
  openssl x509 -req -in "${name}.csr" -CA ca.pem -CAkey ca.key -CAcreateserial \
    -out "${name}.pem" -days 825 -sha256 \
    -extfile "$extfile" >/dev/null 2>&1
  rm -f "${name}.csr" "$extfile"
  chmod 600 "${name}.key"
  echo "  issued ${name}.pem / ${name}.key"
}

echo "issuing service certs..."
mosquitto_sans="DNS:mosquitto,DNS:localhost,IP:127.0.0.1"
control_sans="DNS:control,DNS:localhost,IP:127.0.0.1"
if [ -n "$LAN_IP" ]; then
  mosquitto_sans="${mosquitto_sans},IP:${LAN_IP}"
  control_sans="${control_sans},IP:${LAN_IP}"
fi
issue mosquitto   "$mosquitto_sans"
issue timescaledb "DNS:timescaledb,DNS:localhost,IP:127.0.0.1"
issue control "$control_sans"

echo "done. certs in $CERT_DIR (gitignored, dev-only)."
