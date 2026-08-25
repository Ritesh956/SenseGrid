// Package devices is the Postgres-backed device registry — the system of
// record for claimed devices from Phase 2 onward, replacing the Redis
// placeholder internal/devicestore used in Phase 1 (which now holds only
// short-lived registration tokens).
package devices

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Device is one row of the devices table (0001_devices.sql).
type Device struct {
	ID           string
	Name         string
	Type         string
	RegisteredAt time.Time
	LastSeen     *time.Time
	Status       string
	Metadata     map[string]any
}

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

const deviceColumns = "id, name, type, registered_at, last_seen, status, metadata"

// Create records a newly claimed device. Called once, at claim time,
// before broker credentials are handed back to the client — so by the
// time a device can authenticate and publish, its row already exists,
// which is what lets readings.device_id be a foreign key into this table
// without any ordering hazard.
func (s *Store) Create(ctx context.Context, id, name, typ string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO devices (id, name, type, status)
		VALUES ($1, $2, $3, 'claimed')`,
		id, name, typ)
	if err != nil {
		return fmt.Errorf("devices: creating %s: %w", id, err)
	}
	return nil
}

// Get returns one device by id, or (nil, nil) if it doesn't exist.
func (s *Store) Get(ctx context.Context, id string) (*Device, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+deviceColumns+` FROM devices WHERE id = $1`, id)
	d, err := scanDevice(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("devices: getting %s: %w", id, err)
	}
	return d, nil
}

// List returns every claimed device, newest first — GET /v1/devices'
// backing query (Phase 4).
func (s *Store) List(ctx context.Context) ([]*Device, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+deviceColumns+` FROM devices ORDER BY registered_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("devices: listing: %w", err)
	}
	defer rows.Close()

	var out []*Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, fmt.Errorf("devices: scanning list row: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// UpdateLastSeen stamps a device's last_seen column — called by
// shadow.Reconciler on every reported-state message (Phase 4), the first
// writer this column has ever had.
func (s *Store) UpdateLastSeen(ctx context.Context, id string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE devices SET last_seen = $2 WHERE id = $1`, id, at)
	if err != nil {
		return fmt.Errorf("devices: updating last_seen for %s: %w", id, err)
	}
	return nil
}

func scanDevice(row pgx.Row) (*Device, error) {
	var d Device
	var metadata []byte
	if err := row.Scan(&d.ID, &d.Name, &d.Type, &d.RegisteredAt, &d.LastSeen, &d.Status, &metadata); err != nil {
		return nil, err
	}
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &d.Metadata); err != nil {
			return nil, fmt.Errorf("unmarshaling metadata: %w", err)
		}
	}
	return &d, nil
}
