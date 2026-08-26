package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/Ritesh956/SenseGrid/internal/rollout"
	"github.com/Ritesh956/SenseGrid/internal/shadow"
)

const maxRolloutBodyBytes = 8192

// stageRequest/rolloutRequest/stageView/rolloutView translate between the
// wire format (bake_seconds, human-friendly) and rollout.Stage's Go shape
// (BakeDuration time.Duration, which marshals as raw nanoseconds — fine
// internally, unfriendly over HTTP).
type stageRequest struct {
	Percent     int `json:"percent"`
	BakeSeconds int `json:"bake_seconds"`
}

type rolloutRequest struct {
	Name           string                 `json:"name"`
	Cohort         rollout.Cohort         `json:"cohort"`
	DesiredConfig  shadow.Desired         `json:"desired_config"`
	Stages         []stageRequest         `json:"stages"`
	HealthCriteria rollout.HealthCriteria `json:"health_criteria"`
}

type stageView struct {
	Percent     int `json:"percent"`
	BakeSeconds int `json:"bake_seconds"`
}

type rolloutView struct {
	ID                    string                 `json:"id"`
	Name                  string                 `json:"name"`
	Cohort                rollout.Cohort         `json:"cohort"`
	DesiredConfig         shadow.Desired         `json:"desired_config"`
	Stages                []stageView            `json:"stages"`
	HealthCriteria        rollout.HealthCriteria `json:"health_criteria"`
	State                 rollout.State          `json:"state"`
	CurrentStageIndex     int                    `json:"current_stage_index"`
	CurrentStageStartedAt string                 `json:"current_stage_started_at"`
}

func toRolloutView(r *rollout.Rollout) rolloutView {
	stages := make([]stageView, len(r.Stages))
	for i, s := range r.Stages {
		stages[i] = stageView{Percent: s.Percent, BakeSeconds: int(s.BakeDuration.Seconds())}
	}
	return rolloutView{
		ID: r.ID, Name: r.Name, Cohort: r.Cohort, DesiredConfig: r.DesiredConfig,
		Stages: stages, HealthCriteria: r.HealthCriteria, State: r.State,
		CurrentStageIndex: r.CurrentStageIndex, CurrentStageStartedAt: r.CurrentStageStartedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
	}
}

// registerRolloutHandlers wires the Phase 4 Pass B staged-rollout
// endpoints, all admin-only per the blueprint's REST table — including
// GET, matching Phase 5's own "Rollouts view ... (admin role)" task.
// /resume and the bare GET /v1/rollouts list go beyond the table's literal
// [/pause|/abort] bracket — see cmd/control's Phase 4 Pass B plan for why
// both are small, clearly justified additions.
func registerRolloutHandlers(mux *http.ServeMux, logger *slog.Logger, engine *rollout.Engine, issuer string, signingKey []byte) {
	mux.HandleFunc("POST /v1/rollouts", requireRole(roleAdmin, issuer, signingKey, func(w http.ResponseWriter, r *http.Request, _ string) {
		var req rolloutRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, maxRolloutBodyBytes)).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		stages := make([]rollout.Stage, len(req.Stages))
		for i, s := range req.Stages {
			stages[i] = rollout.Stage{Percent: s.Percent, BakeDuration: secondsToDuration(s.BakeSeconds)}
		}
		created, err := engine.Create(r.Context(), &rollout.Rollout{
			Name: req.Name, Cohort: req.Cohort, DesiredConfig: req.DesiredConfig,
			Stages: stages, HealthCriteria: req.HealthCriteria,
		})
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		logger.Info("rollout: created", "id", created.ID, "name", created.Name)
		writeJSON(w, http.StatusOK, toRolloutView(created))
	}))

	mux.HandleFunc("GET /v1/rollouts", requireRole(roleAdmin, issuer, signingKey, func(w http.ResponseWriter, r *http.Request, _ string) {
		list, err := engine.List(r.Context())
		if err != nil {
			logger.Error("rollout: list failed", "err", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}
		views := make([]rolloutView, len(list))
		for i, ro := range list {
			views[i] = toRolloutView(ro)
		}
		writeJSON(w, http.StatusOK, map[string]any{"rollouts": views})
	}))

	mux.HandleFunc("GET /v1/rollouts/{id}", requireRole(roleAdmin, issuer, signingKey, func(w http.ResponseWriter, r *http.Request, _ string) {
		ro, err := engine.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			logger.Error("rollout: get failed", "err", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if ro == nil {
			writeJSONError(w, http.StatusNotFound, "rollout not found")
			return
		}
		writeJSON(w, http.StatusOK, toRolloutView(ro))
	}))

	mux.HandleFunc("POST /v1/rollouts/{id}/pause", requireRole(roleAdmin, issuer, signingKey, func(w http.ResponseWriter, r *http.Request, _ string) {
		ro, err := engine.Pause(r.Context(), r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, toRolloutView(ro))
	}))

	mux.HandleFunc("POST /v1/rollouts/{id}/resume", requireRole(roleAdmin, issuer, signingKey, func(w http.ResponseWriter, r *http.Request, _ string) {
		ro, err := engine.Resume(r.Context(), r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, toRolloutView(ro))
	}))

	mux.HandleFunc("POST /v1/rollouts/{id}/abort", requireRole(roleAdmin, issuer, signingKey, func(w http.ResponseWriter, r *http.Request, _ string) {
		ro, err := engine.Abort(r.Context(), r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		logger.Info("rollout: aborted", "id", ro.ID)
		writeJSON(w, http.StatusOK, toRolloutView(ro))
	}))
}

func secondsToDuration(s int) time.Duration { return time.Duration(s) * time.Second }
