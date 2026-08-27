# Build context is the repo root (see docker-compose.yml). Certs are baked
# into the image at build time (run `scripts/gen-certs.sh` first) so
# ownership/permissions are set deterministically inside the image — Postgres
# refuses to start if server.key is group- or world-readable, and a
# bind-mounted file from a Windows host can't reliably guarantee that.
FROM timescale/timescaledb:latest-pg16

# Same rationale as mosquitto.Dockerfile's apk upgrade: Phase 8's Trivy scan
# found 4 HIGH CVEs (libcrypto3/libssl3/sqlite-libs) already fixed in
# Alpine's own repos but not yet in this floating base tag's last rebuild.
RUN apk update && apk upgrade --no-cache

COPY deploy/certs/timescaledb.pem /var/lib/postgresql/server.crt
COPY deploy/certs/timescaledb.key /var/lib/postgresql/server.key
COPY deploy/certs/ca.pem /var/lib/postgresql/ca.crt

RUN chown postgres:postgres /var/lib/postgresql/server.crt /var/lib/postgresql/server.key /var/lib/postgresql/ca.crt \
    && chmod 600 /var/lib/postgresql/server.key \
    && chmod 644 /var/lib/postgresql/server.crt /var/lib/postgresql/ca.crt

CMD ["postgres", "-c", "ssl=on", \
     "-c", "ssl_cert_file=/var/lib/postgresql/server.crt", \
     "-c", "ssl_key_file=/var/lib/postgresql/server.key", \
     "-c", "ssl_ca_file=/var/lib/postgresql/ca.crt"]
