# Build context is the repo root (see docker-compose.yml), so paths below
# are relative to it. Certs are baked into the image at build time — run
# `scripts/gen-certs.sh` first — rather than bind-mounted, since bind-mounted
# key files pick up whatever permission bits the host filesystem gives them
# (unreliable from a Windows host) instead of what the broker expects.
FROM eclipse-mosquitto:2

# eclipse-mosquitto:2 is a floating tag; its baked-in Alpine packages lag
# behind Alpine's own repos between upstream rebuilds (Phase 8's Trivy scan
# found 5 HIGH CVEs here — libcrypto3/libssl3/p11-kit/sqlite-libs — all
# already fixed in the distro's repos, just not yet in this image tag).
# Upgrading in our derived image rather than waiting on upstream keeps this
# patched regardless of when eclipse-mosquitto's next rebuild lands.
RUN apk update && apk upgrade --no-cache

COPY deploy/mosquitto/mosquitto.conf /mosquitto/config/mosquitto.conf
COPY deploy/mosquitto/entrypoint.sh /sensegrid-entrypoint.sh
COPY deploy/certs/mosquitto.pem /mosquitto/certs/server.crt
COPY deploy/certs/mosquitto.key /mosquitto/certs/server.key
COPY deploy/certs/ca.pem /mosquitto/certs/ca.crt

RUN chmod 644 /mosquitto/certs/server.crt /mosquitto/certs/server.key /mosquitto/certs/ca.crt \
    && chmod +x /sensegrid-entrypoint.sh

ENTRYPOINT ["/sensegrid-entrypoint.sh"]
# Declaring a new ENTRYPOINT drops the base image's inherited CMD (verified
# empirically: `docker inspect` shows Cmd=null after just an ENTRYPOINT
# override here), so it has to be restated explicitly rather than assumed
# to carry over.
CMD ["/usr/sbin/mosquitto", "-c", "/mosquitto/config/mosquitto.conf"]
