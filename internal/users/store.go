// Package users is the Postgres-backed console login store (Phase 5) — the
// system of record for the console's username/password/role identities,
// mirroring internal/devices' pgx pattern. This is deliberately separate
// from cmd/control's JWT role model (auth.go): a User is what
// POST /v1/auth/login authenticates against to decide which role JWT to
// mint; once minted, the token itself carries no reference back to this
// table (see auth.go's doc comment on why roles, not per-user identity,
// are what requireRole checks).
package users

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// User is one row of the users table (0008_users.sql).
type User struct {
	ID           string
	Username     string
	PasswordHash string
	Role         string
	CreatedAt    time.Time
}

// Store is a thin I/O wrapper, like internal/devices.Store — no unit tests
// of its own (no test-DB harness exists in this repo).
type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

const userColumns = "id, username, password_hash, role, created_at"

// Create records a new console user, generating id itself (matching
// alerts.Store.Open's convention of Go-generated UUIDs). Used only by
// `control user create` (cmd/control/user_cli.go) — there is no HTTP
// endpoint that creates users, matching the project's existing
// CLI-only-provisioning pattern for tokens and JWTs.
func (s *Store) Create(ctx context.Context, username, passwordHash, role string) (*User, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO users (id, username, password_hash, role)
		VALUES ($1, $2, $3, $4)
		RETURNING `+userColumns,
		uuid.New().String(), username, passwordHash, role)
	u, err := scanUser(row)
	if err != nil {
		return nil, fmt.Errorf("users: creating %s: %w", username, err)
	}
	return u, nil
}

// GetByUsername returns one user by username, or (nil, nil) if it doesn't
// exist — POST /v1/auth/login's lookup, matching devices.Store.Get's
// not-found convention.
func (s *Store) GetByUsername(ctx context.Context, username string) (*User, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE username = $1`, username)
	u, err := scanUser(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("users: getting %s: %w", username, err)
	}
	return u, nil
}

func scanUser(row pgx.Row) (*User, error) {
	var u User
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt); err != nil {
		return nil, err
	}
	return &u, nil
}
