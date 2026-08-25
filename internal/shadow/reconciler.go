package shadow

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/Ritesh956/SenseGrid/internal/devices"
	"github.com/Ritesh956/SenseGrid/internal/telemetry"
)

// Reconciler subscribes to every device's state topic (using cmd/control's
// wildcard-subscribe "control" MQTT identity) and folds each reported
// state into the shadow Store, so GET /v1/devices/{id}/shadow and
// GET /v1/devices/drift always reflect what devices most recently said
// they applied — "reconciles against the KV revision, not arrival order",
// per the blueprint, is what Store.PutReported + Drift (drift.go) give us:
// correctness here doesn't depend on MQTT delivery order.
type Reconciler struct {
	store   *Store
	devices *devices.Store
	logger  *slog.Logger
}

func NewReconciler(store *Store, deviceStore *devices.Store, logger *slog.Logger) *Reconciler {
	return &Reconciler{store: store, devices: deviceStore, logger: logger}
}

// Start subscribes client to sensegrid/v1/+/state at QoS 1. client is
// passed in rather than stored at construction time so cmd/control can
// call this from its MQTT client's OnConnectHandler (paho doesn't
// automatically resubscribe on reconnect — see cmd/hostagent's
// connectMQTT for the same pattern), which is handed the connected client
// as its own callback argument.
func (r *Reconciler) Start(client mqtt.Client) error {
	token := client.Subscribe("sensegrid/v1/+/state", 1, r.onMessage)
	token.Wait()
	return token.Error()
}

func (r *Reconciler) onMessage(_ mqtt.Client, msg mqtt.Message) {
	deviceID, ok := telemetry.DeviceIDFromStateTopic(msg.Topic())
	if !ok {
		r.logger.Warn("shadow: state message on unrecognized topic, ignoring", "topic", msg.Topic())
		return
	}

	var rep Reported
	if err := json.Unmarshal(msg.Payload(), &rep); err != nil {
		r.logger.Error("shadow: unparseable reported state, ignoring", "err", err, "device_id", deviceID)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := r.store.PutReported(ctx, deviceID, rep); err != nil {
		r.logger.Error("shadow: recording reported state failed", "err", err, "device_id", deviceID)
		return
	}
	if err := r.devices.UpdateLastSeen(ctx, deviceID, time.Now()); err != nil {
		r.logger.Error("shadow: updating last_seen failed", "err", err, "device_id", deviceID)
	}
}
