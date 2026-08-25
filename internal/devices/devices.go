// Package devices is the Postgres-backed device registry — the system of
// record for claimed devices from Phase 2 onward, replacing the Redis
// placeholder internal/devicestore used in Phase 1 (which now holds only
// short-lived registration tokens).
package devices

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

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
