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

if [ -f ca.pem ]; then
  echo "certs already exist in $CERT_DIR — delete the directory to regenerate"
  exit 0
fi

echo "generating dev CA..."
openssl genrsa -out ca.key 4096 >/dev/null 2>&1
openssl req -x509 -new -nodes -key ca.key -sha256 -days 825 \
  -subj "/CN=SenseGrid Dev CA" -out ca.pem >/dev/null 2>&1

issue() {
  local name="$1" sans="$2"
  local extfile="${name}.ext.cnf"
  printf "subjectAltName=%s\n" "$sans" > "$extfile"
  openssl genrsa -out "${name}.key" 2048 >/dev/null 2>&1
  openssl req -new -key "${name}.key" -subj "/CN=${name}" -out "${name}.csr" >/dev/null 2>&1
  openssl x509 -req -in "${name}.csr" -CA ca.pem -CAkey ca.key -CAcreateserial \
    -out "${name}.pem" -days 825 -sha256 \
    -extfile "$extfile" >/dev/null 2>&1
  rm -f "${name}.csr" "$extfile"
  echo "  issued ${name}.pem / ${name}.key"
}

echo "issuing service certs..."
issue mosquitto   "DNS:mosquitto,DNS:localhost,IP:127.0.0.1"
issue timescaledb "DNS:timescaledb,DNS:localhost,IP:127.0.0.1"

chmod 600 ./*.key
echo "done. certs written to $CERT_DIR (gitignored, dev-only)."
