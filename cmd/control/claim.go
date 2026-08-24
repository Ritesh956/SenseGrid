package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/Ritesh956/SenseGrid/internal/devicestore"
	"github.com/Ritesh956/SenseGrid/internal/dynsec"
	"github.com/google/uuid"
)

const maxClaimBodyBytes = 4096

type claimRequest struct {
	Token    string `json:"token"`
	DeviceID string `json:"device_id"`
}

type claimResponse struct {
	DeviceID     string `json:"device_id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	MQTTUsername string `json:"mqtt_username"`
	MQTTPassword string `json:"mqtt_password"`
	MQTTWSURL    string `json:"mqtt_ws_url"`
}

// registerClaimHandler wires POST /v1/devices/claim: a one-time
// registration token (see token.go) is exchanged for broker credentials
// scoped to exactly one device_id. Deliberately unauthenticated beyond the
// token itself — the whole point is that the device doesn't have
// credentials yet.
func registerClaimHandler(mux *http.ServeMux, logger *slog.Logger, store *devicestore.Store, dynsecClient *dynsec.Client) {
	mux.HandleFunc("POST /v1/devices/claim", func(w http.ResponseWriter, r *http.Request) {
		if dynsecClient == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "device provisioning is temporarily unavailable")
			return
		}

		var req claimRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, maxClaimBodyBytes)).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Token == "" || req.DeviceID == "" {
			writeJSONError(w, http.StatusBadRequest, "token and device_id are required")
			return
		}
		if _, err := uuid.Parse(req.DeviceID); err != nil {
			writeJSONError(w, http.StatusBadRequest, "device_id must be a uuid")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		rec, err := store.ConsumeToken(ctx, req.Token)
		if errors.Is(err, devicestore.ErrTokenNotFound) {
			writeJSONError(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		if err != nil {
			logger.Error("claim: consume token", "err", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		password, err := randomPassword()
		if err != nil {
			logger.Error("claim: generate password", "err", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if err := dynsecClient.CreateClient(ctx, req.DeviceID, password, rec.Name, deviceRoleName); err != nil {
			logger.Error("claim: provision broker credential", "err", err, "device_id", req.DeviceID)
			writeJSONError(w, http.StatusInternalServerError, "failed to provision broker credentials")
			return
		}

		if err := store.SaveDevice(ctx, devicestore.Device{
			DeviceID:  req.DeviceID,
			Name:      rec.Name,
			Type:      rec.Type,
			ClaimedAt: time.Now().UTC(),
		}); err != nil {
			// Broker credentials are already provisioned; don't fail the
			// claim over a bookkeeping write the device doesn't need to
			// proceed. Phase 2 replaces this store with TimescaleDB anyway.
			logger.Error("claim: save device record", "err", err, "device_id", req.DeviceID)
		}

		resp := claimResponse{
			DeviceID:     req.DeviceID,
			Name:         rec.Name,
			Type:         rec.Type,
			MQTTUsername: req.DeviceID,
			MQTTPassword: password,
			MQTTWSURL:    "wss://" + hostOnly(r.Host) + ":9001",
		}
		logger.Info("device claimed", "device_id", req.DeviceID, "name", rec.Name, "type", rec.Type)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

func randomPassword() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
