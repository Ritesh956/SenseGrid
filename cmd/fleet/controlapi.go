// The fleet's own HTTP control surface — what test/chaos's scripts drive
// instead of docker/network-level manipulation, since 1000 virtual devices
// live as goroutines inside one process rather than one container each.
// Ramping and partitioning are simulated here for exactly that reason: the
// realistic failure mode for a synthetic fleet is "the client's own
// connection drops," which is what Partition actually does (device.go
// disconnects the MQTT client, not the process).
package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const maxControlBodyBytes = 4096

func registerFleetAPI(mux *http.ServeMux, manager *FleetManager, logger *slog.Logger) {
	mux.HandleFunc("GET /fleet/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, manager.Status())
	})

	mux.HandleFunc("POST /fleet/scale", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Count int `json:"count"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, maxControlBodyBytes)).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Count < 0 {
			writeJSONError(w, http.StatusBadRequest, "count must be >= 0")
			return
		}
		logger.Info("fleet: scale requested", "count", req.Count)
		manager.Scale(req.Count)
		writeJSON(w, http.StatusOK, manager.Status())
	})

	mux.HandleFunc("POST /fleet/partition", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Count      int `json:"count"`
			DurationMS int `json:"duration_ms"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, maxControlBodyBytes)).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Count <= 0 || req.DurationMS <= 0 {
			writeJSONError(w, http.StatusBadRequest, "count and duration_ms must be positive")
			return
		}
		duration := time.Duration(req.DurationMS) * time.Millisecond
		ids := manager.Partition(req.Count, duration)
		logger.Info("fleet: partition requested", "requested", req.Count, "partitioned", len(ids), "duration", duration)
		writeJSON(w, http.StatusOK, map[string]any{
			"partitioned_device_ids": ids,
			"duration_ms":            req.DurationMS,
		})
	})

	mux.HandleFunc("POST /fleet/config", func(w http.ResponseWriter, r *http.Request) {
		var patch configPatch
		if err := json.NewDecoder(io.LimitReader(r.Body, maxControlBodyBytes)).Decode(&patch); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		next := manager.UpdateRuntimeConfig(patch)
		logger.Info("fleet: runtime config updated", "config", next)
		writeJSON(w, http.StatusOK, next)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
