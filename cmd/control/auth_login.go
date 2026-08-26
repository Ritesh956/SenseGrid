package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/Ritesh956/SenseGrid/internal/users"
)

const maxLoginBodyBytes = 4096

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	AccessToken string    `json:"access_token"`
	Role        string    `json:"role"`
	Username    string    `json:"username"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// registerAuthHandlers wires POST /v1/auth/login — the Phase 5 console's
// entry point, exchanging a username/password (internal/users) for the
// same kind of role JWT `control jwt create` mints, just with a subject
// attached and consoleTTL instead of the CLI's default TTL. Deliberately
// unauthenticated, like registerClaimHandler's claim endpoint: the whole
// point is that the caller doesn't have a token yet.
func registerAuthHandlers(mux *http.ServeMux, logger *slog.Logger, store *users.Store, issuer string, signingKey []byte, consoleTTL time.Duration) {
	mux.HandleFunc("POST /v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var req loginRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, maxLoginBodyBytes)).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Username == "" || req.Password == "" {
			writeJSONError(w, http.StatusBadRequest, "username and password are required")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		u, err := store.GetByUsername(ctx, req.Username)
		if err != nil {
			logger.Error("auth: login lookup failed", "err", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}
		// Unknown username and wrong password both fail identically below
		// (compare against a fixed hash when u is nil) so the response
		// never signals which one it was.
		hash := []byte("$2a$10$CwTycUXWue0Thq9StjUM0uJ8vGaJ07k4PQXHt5cJ1G0/PkQ8p2p3O")
		if u != nil {
			hash = []byte(u.PasswordHash)
		}
		if bcrypt.CompareHashAndPassword(hash, []byte(req.Password)) != nil || u == nil {
			writeJSONError(w, http.StatusUnauthorized, "invalid username or password")
			return
		}

		expiresAt := time.Now().Add(consoleTTL)
		token, err := signToken(u.Role, issuer, consoleTTL, signingKey, u.Username)
		if err != nil {
			logger.Error("auth: signing login token failed", "err", err, "username", u.Username)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}
		logger.Info("console login", "username", u.Username, "role", u.Role)
		writeJSON(w, http.StatusOK, loginResponse{
			AccessToken: token,
			Role:        u.Role,
			Username:    u.Username,
			ExpiresAt:   expiresAt,
		})
	})
}
