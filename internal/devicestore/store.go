// Package devicestore is a Redis-backed store for registration tokens and
// claimed device records. It is a deliberate placeholder: Phase 2 moves the
// device registry into TimescaleDB (the devices table) as the system of
// record, at which point this package's device-record half goes away and
// only the short-lived, naturally TTL'd registration tokens stay in Redis.
package devicestore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrTokenNotFound means the token does not exist, already expired, or was
// already consumed by a prior claim.
var ErrTokenNotFound = errors.New("devicestore: registration token not found or already used")

// TokenRecord is what an admin declared when issuing a registration token.
type TokenRecord struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Device is a provisioned device's record.
type Device struct {
	DeviceID  string    `json:"device_id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	ClaimedAt time.Time `json:"claimed_at"`
}

type Store struct {
	rdb *redis.Client
}

func New(addr string) *Store {
	return &Store{rdb: redis.NewClient(&redis.Options{Addr: addr})}
}

func (s *Store) Ping(ctx context.Context) error {
	return s.rdb.Ping(ctx).Err()
}

func (s *Store) Close() error {
	return s.rdb.Close()
}

// CreateToken generates a new single-use registration token for a device
// with the given name/type, valid for ttl.
func (s *Store) CreateToken(ctx context.Context, name, typ string, ttl time.Duration) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", fmt.Errorf("devicestore: generating token: %w", err)
	}
	rec, err := json.Marshal(TokenRecord{Name: name, Type: typ})
	if err != nil {
		return "", err
	}
	if err := s.rdb.Set(ctx, tokenKey(token), rec, ttl).Err(); err != nil {
		return "", fmt.Errorf("devicestore: storing token: %w", err)
	}
	return token, nil
}

// ConsumeToken atomically fetches and deletes a registration token so it
// cannot be replayed against a second claim request.
func (s *Store) ConsumeToken(ctx context.Context, token string) (TokenRecord, error) {
	raw, err := s.rdb.GetDel(ctx, tokenKey(token)).Result()
	if errors.Is(err, redis.Nil) {
		return TokenRecord{}, ErrTokenNotFound
	}
	if err != nil {
		return TokenRecord{}, fmt.Errorf("devicestore: consuming token: %w", err)
	}
	var rec TokenRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return TokenRecord{}, fmt.Errorf("devicestore: decoding token record: %w", err)
	}
	return rec, nil
}

// SaveDevice records a newly claimed device.
func (s *Store) SaveDevice(ctx context.Context, d Device) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if err := s.rdb.Set(ctx, deviceKey(d.DeviceID), raw, 0).Err(); err != nil {
		return fmt.Errorf("devicestore: saving device: %w", err)
	}
	return nil
}

func tokenKey(token string) string { return "sensegrid:regtoken:" + token }
func deviceKey(id string) string   { return "sensegrid:device:" + id }

func randomToken() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
