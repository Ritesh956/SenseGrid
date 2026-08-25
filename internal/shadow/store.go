package shadow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"
)

const BucketName = "SHADOW"

// EnsureBucket idempotently creates (or reconciles) the shadow KV bucket,
// mirroring cmd/ingest/jetstream.go's ensureStreams pattern. History keeps
// the last 5 revisions per key — cheap, and gives some manual-inspection
// value ("what was desired before this") without being a real rollback
// mechanism (Postgres shadow_history is the actual audit trail).
func EnsureBucket(ctx context.Context, js jetstream.JetStream) (jetstream.KeyValue, error) {
	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:  BucketName,
		History: 5,
	})
	if err != nil {
		return nil, fmt.Errorf("shadow: ensuring %s bucket: %w", BucketName, err)
	}
	return kv, nil
}

// Store is the device shadow's hot path (JetStream KV) plus its Postgres
// audit mirror (shadow_history — insert-only, never read back on the hot
// path; see deploy/migrations/0006_shadow_history.sql).
type Store struct {
	kv   jetstream.KeyValue
	pool *pgxpool.Pool
}

func NewStore(kv jetstream.KeyValue, pool *pgxpool.Pool) *Store {
	return &Store{kv: kv, pool: pool}
}

func desiredKey(deviceID string) string  { return deviceID + ".desired" }
func reportedKey(deviceID string) string { return deviceID + ".reported" }

// SetDesired writes a device's desired config. d.Revision is ignored on
// the way in (revision is a property of the KV entry, not something a
// caller sets) and populated on the way out from what Put actually
// returned, so PublishDesired (publisher.go) can stamp the same value into
// the retained MQTT payload devices echo back in Reported.AppliedRevision.
func (s *Store) SetDesired(ctx context.Context, deviceID string, d Desired) (Desired, error) {
	d.SchemaVersion = SchemaVersion
	d.Revision = 0
	body, err := json.Marshal(d)
	if err != nil {
		return Desired{}, fmt.Errorf("shadow: marshaling desired for %s: %w", deviceID, err)
	}
	rev, err := s.kv.Put(ctx, desiredKey(deviceID), body)
	if err != nil {
		return Desired{}, fmt.Errorf("shadow: writing desired for %s: %w", deviceID, err)
	}
	d.Revision = rev

	if err := s.audit(ctx, deviceID, "desired", rev, body); err != nil {
		return Desired{}, err
	}
	return d, nil
}

// GetDesired returns a device's current desired config and its revision.
func (s *Store) GetDesired(ctx context.Context, deviceID string) (Desired, uint64, error) {
	entry, err := s.kv.Get(ctx, desiredKey(deviceID))
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return Desired{}, 0, nil
	}
	if err != nil {
		return Desired{}, 0, fmt.Errorf("shadow: reading desired for %s: %w", deviceID, err)
	}
	var d Desired
	if err := json.Unmarshal(entry.Value(), &d); err != nil {
		return Desired{}, 0, fmt.Errorf("shadow: unmarshaling desired for %s: %w", deviceID, err)
	}
	d.Revision = entry.Revision()
	return d, entry.Revision(), nil
}

// PutReported records what a device reported it actually applied — called
// by Reconciler on every message on internal/telemetry.StateTopic.
func (s *Store) PutReported(ctx context.Context, deviceID string, r Reported) error {
	r.SchemaVersion = SchemaVersion
	body, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("shadow: marshaling reported for %s: %w", deviceID, err)
	}
	rev, err := s.kv.Put(ctx, reportedKey(deviceID), body)
	if err != nil {
		return fmt.Errorf("shadow: writing reported for %s: %w", deviceID, err)
	}
	return s.audit(ctx, deviceID, "reported", rev, body)
}

// GetReported returns a device's last-reported state, or (nil, nil) if it
// has never reported.
func (s *Store) GetReported(ctx context.Context, deviceID string) (*Reported, error) {
	entry, err := s.kv.Get(ctx, reportedKey(deviceID))
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("shadow: reading reported for %s: %w", deviceID, err)
	}
	var r Reported
	if err := json.Unmarshal(entry.Value(), &r); err != nil {
		return nil, fmt.Errorf("shadow: unmarshaling reported for %s: %w", deviceID, err)
	}
	return &r, nil
}

func (s *Store) audit(ctx context.Context, deviceID, direction string, revision uint64, payload []byte) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO shadow_history (id, device_id, direction, revision, payload)
		VALUES ($1, $2, $3, $4, $5)`,
		uuid.NewString(), deviceID, direction, revision, payload)
	if err != nil {
		return fmt.Errorf("shadow: auditing %s %s: %w", direction, deviceID, err)
	}
	return nil
}
