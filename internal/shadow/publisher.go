package shadow

import (
	"encoding/json"
	"fmt"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/Ritesh956/SenseGrid/internal/telemetry"
)

// Publisher retained-publishes a device's desired config to the MQTT
// broker, using cmd/control's own "control" identity (see main.go's
// connectDynsec) — a device's ACL already grants it subscribe+receive on
// its own config topic (provisioned in Phase 1/2), but something needs
// wildcard publish rights across every device's config topic to actually
// push updates, which is what the control MQTT client is for.
type Publisher struct {
	client mqtt.Client
}

func NewPublisher(client mqtt.Client) *Publisher {
	return &Publisher{client: client}
}

// PublishDesired retained-publishes d (QoS 1) to a device's config topic,
// so a reconnecting device picks up the latest desired config immediately
// with no poll — the blueprint's Phase 4 Definition of Done.
func (p *Publisher) PublishDesired(deviceID string, d Desired) error {
	body, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("shadow: marshaling desired for publish to %s: %w", deviceID, err)
	}
	token := p.client.Publish(telemetry.ConfigTopic(deviceID), 1, true, body)
	token.Wait()
	if err := token.Error(); err != nil {
		return fmt.Errorf("shadow: publishing desired config to %s: %w", deviceID, err)
	}
	return nil
}
