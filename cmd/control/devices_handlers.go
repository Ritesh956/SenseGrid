package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/Ritesh956/SenseGrid/internal/devices"
	"github.com/Ritesh956/SenseGrid/internal/shadow"
)

const maxShadowBodyBytes = 4096

type deviceView struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Type         string     `json:"type"`
	Status       string     `json:"status"`
	RegisteredAt time.Time  `json:"registered_at"`
	LastSeen     *time.Time `json:"last_seen,omitempty"`
}

type shadowView struct {
	Desired         shadow.Desired   `json:"desired"`
	DesiredRevision uint64           `json:"desired_revision"`
	Reported        *shadow.Reported `json:"reported"`
	Drift           bool             `json:"drift"`
}

// registerDeviceHandlers wires the Phase 4 device/shadow read+write
// endpoints. deviceStore and shadowStore are shared with the rest of
// cmd/control (built once in runServer); shadowPub is nil-safe callers
// never see — PUT .../shadow/desired 503s instead if the control MQTT
// client never connected, mirroring how registerClaimHandler already
// handles a nil dynsecClient.
func registerDeviceHandlers(mux *http.ServeMux, logger *slog.Logger, deviceStore *devices.Store,
	shadowStore *shadow.Store, shadowPub *shadow.Publisher, driftStaleAfter time.Duration,
	issuer string, signingKey []byte) {

	mux.HandleFunc("GET /v1/devices", requireRole(roleViewer, issuer, signingKey, func(w http.ResponseWriter, r *http.Request, _ string) {
		list, err := deviceStore.List(r.Context())
		if err != nil {
			logger.Error("devices: list failed", "err", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"devices": toDeviceViews(list)})
	}))

	mux.HandleFunc("GET /v1/devices/{id}/shadow", requireRole(roleViewer, issuer, signingKey, func(w http.ResponseWriter, r *http.Request, _ string) {
		id := r.PathValue("id")
		ctx := r.Context()

		desired, rev, err := shadowStore.GetDesired(ctx, id)
		if err != nil {
			logger.Error("shadow: get desired failed", "err", err, "device_id", id)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}
		reported, err := shadowStore.GetReported(ctx, id)
		if err != nil {
			logger.Error("shadow: get reported failed", "err", err, "device_id", id)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusOK, shadowView{
			Desired: desired, DesiredRevision: rev, Reported: reported,
			Drift: shadow.Drift(rev, reported, time.Now(), driftStaleAfter),
		})
	}))

	mux.HandleFunc("PUT /v1/devices/{id}/shadow/desired", requireRole(roleAdmin, issuer, signingKey, func(w http.ResponseWriter, r *http.Request, _ string) {
		if shadowPub == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "shadow publishing is temporarily unavailable")
			return
		}
		id := r.PathValue("id")

		var desired shadow.Desired
		if err := json.NewDecoder(io.LimitReader(r.Body, maxShadowBodyBytes)).Decode(&desired); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		ctx := r.Context()
		stored, err := shadowStore.SetDesired(ctx, id, desired)
		if err != nil {
			logger.Error("shadow: set desired failed", "err", err, "device_id", id)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if err := shadowPub.PublishDesired(id, stored); err != nil {
			logger.Error("shadow: publish desired failed", "err", err, "device_id", id)
			writeJSONError(w, http.StatusInternalServerError, "desired state saved but publish to the device failed")
			return
		}
		logger.Info("shadow: desired config updated", "device_id", id, "revision", stored.Revision)
		writeJSON(w, http.StatusOK, stored)
	}))

	mux.HandleFunc("GET /v1/devices/drift", requireRole(roleViewer, issuer, signingKey, func(w http.ResponseWriter, r *http.Request, _ string) {
		ctx := r.Context()
		list, err := deviceStore.List(ctx)
		if err != nil {
			logger.Error("devices: list failed", "err", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		// []deviceView{}, not var drifted []deviceView: a nil slice
		// marshals to JSON `null` rather than `[]` when nothing has
		// drifted, which breaks naive consumers doing `.devices[]`
		// (test/chaos/partition.sh's drift poll hit exactly this — found
		// live, not theoretical). GET /v1/devices's toDeviceViews already
		// avoids this the same way.
		drifted := []deviceView{}
		for _, d := range list {
			_, rev, err := shadowStore.GetDesired(ctx, d.ID)
			if err != nil {
				logger.Error("shadow: get desired failed during drift scan", "err", err, "device_id", d.ID)
				continue
			}
			if rev == 0 {
				continue // never given a desired config — nothing to drift from
			}
			reported, err := shadowStore.GetReported(ctx, d.ID)
			if err != nil {
				logger.Error("shadow: get reported failed during drift scan", "err", err, "device_id", d.ID)
				continue
			}
			if shadow.Drift(rev, reported, time.Now(), driftStaleAfter) {
				drifted = append(drifted, toDeviceView(d))
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"devices": drifted})
	}))
}

func toDeviceViews(list []*devices.Device) []deviceView {
	out := make([]deviceView, 0, len(list))
	for _, d := range list {
		out = append(out, toDeviceView(d))
	}
	return out
}

func toDeviceView(d *devices.Device) deviceView {
	return deviceView{ID: d.ID, Name: d.Name, Type: d.Type, Status: d.Status, RegisteredAt: d.RegisteredAt, LastSeen: d.LastSeen}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
