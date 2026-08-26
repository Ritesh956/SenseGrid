package main

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/Ritesh956/SenseGrid/internal/alerts"
)

// registerAlertHandlers wires POST /v1/alerts/{id}/ack and /resolve — the
// operator-driven counterpart to Phase 3's automatic fire/clear, which
// internal/alerts.Store/Publisher (built for cmd/processor) already
// support via Acknowledge/ResolveByID; cmd/control just needs its own
// instances of the same types, pointed at the same Postgres pool and
// JetStream connection.
func registerAlertHandlers(mux *http.ServeMux, logger *slog.Logger, store *alerts.Store, pub *alerts.Publisher, issuer string, signingKey []byte) {
	// GET /v1/alerts — Phase 5's fleet alert badges and Alerts view. The
	// first read path this store has needed beyond single-alert lookups by
	// id; ?state= and ?device_id= are both optional narrowing filters.
	mux.HandleFunc("GET /v1/alerts", requireRole(roleViewer, issuer, signingKey, func(w http.ResponseWriter, r *http.Request, _ string) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		list, err := store.List(ctx, alerts.Filter{
			State:    alerts.State(r.URL.Query().Get("state")),
			DeviceID: r.URL.Query().Get("device_id"),
			Limit:    limit,
		})
		if err != nil {
			logger.Error("alerts: list failed", "err", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"alerts": list})
	}))

	mux.HandleFunc("POST /v1/alerts/{id}/ack", requireRole(roleOperator, issuer, signingKey, func(w http.ResponseWriter, r *http.Request, _ string) {
		id := r.PathValue("id")
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		a, err := store.Acknowledge(ctx, id)
		if err != nil {
			logger.Warn("alerts: acknowledge failed", "err", err, "id", id)
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := pub.Publish(ctx, a, time.Now()); err != nil {
			logger.Error("alerts: publishing ack event failed", "err", err, "id", id)
		}
		writeJSON(w, http.StatusOK, a)
	}))

	mux.HandleFunc("POST /v1/alerts/{id}/resolve", requireRole(roleOperator, issuer, signingKey, func(w http.ResponseWriter, r *http.Request, _ string) {
		id := r.PathValue("id")
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		a, err := store.ResolveByID(ctx, id, time.Now())
		if err != nil {
			logger.Warn("alerts: resolve failed", "err", err, "id", id)
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := pub.Publish(ctx, a, time.Now()); err != nil {
			logger.Error("alerts: publishing resolve event failed", "err", err, "id", id)
		}
		writeJSON(w, http.StatusOK, a)
	}))
}
