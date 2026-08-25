// Package devicestore is a Redis-backed store for one-time device
// registration tokens. It used to also hold claimed device records; as of
// Phase 2 those live in Postgres (internal/devices) as the system of
// record, and this package is left with exactly the part Redis was
// actually the right fit for — short-lived, naturally-TTL'd tokens.
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

func tokenKey(token string) string { return "sensegrid:regtoken:" + token }

func randomToken() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
