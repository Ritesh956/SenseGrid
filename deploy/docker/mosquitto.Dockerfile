# Build context is the repo root (see docker-compose.yml), so paths below
# are relative to it. Certs are baked into the image at build time — run
# `scripts/gen-certs.sh` first — rather than bind-mounted, since bind-mounted
# key files pick up whatever permission bits the host filesystem gives them
# (unreliable from a Windows host) instead of what the broker expects.
FROM eclipse-mosquitto:2

COPY deploy/mosquitto/mosquitto.conf /mosquitto/config/mosquitto.conf
COPY deploy/certs/mosquitto.pem /mosquitto/certs/server.crt
COPY deploy/certs/mosquitto.key /mosquitto/certs/server.key
COPY deploy/certs/ca.pem /mosquitto/certs/ca.crt

RUN chmod 644 /mosquitto/certs/server.crt /mosquitto/certs/server.key /mosquitto/certs/ca.crt
