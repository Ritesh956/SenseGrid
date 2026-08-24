#!/bin/ash
# Bootstraps the dynamic-security plugin's state file on first run (a purely
# offline operation — mosquitto_ctrl writes the JSON file directly, no
# broker connection needed) then hands off to the base image's own
# entrypoint, which fixes up /mosquitto/data ownership and execs mosquitto.
#
# Runtime role/client creation (the "device" role, per-device credentials)
# happens later over a live MQTT connection — see internal/dynsec — since
# that part of the protocol only works against a running broker.
set -e

STATE_FILE=/mosquitto/data/dynamic-security.json

if [ ! -f "$STATE_FILE" ]; then
	: "${MOSQUITTO_ADMIN_USER:?MOSQUITTO_ADMIN_USER is required}"
	: "${MOSQUITTO_ADMIN_PASSWORD:?MOSQUITTO_ADMIN_PASSWORD is required}"
	echo "bootstrapping dynamic-security state file for admin '$MOSQUITTO_ADMIN_USER'..."
	mosquitto_ctrl dynsec init "$STATE_FILE" "$MOSQUITTO_ADMIN_USER" "$MOSQUITTO_ADMIN_PASSWORD"
fi

exec /docker-entrypoint.sh "$@"
