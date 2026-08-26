package rollout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Ritesh956/SenseGrid/internal/shadow"
)

// Target is one device a rollout has pushed config to — one row of
// rollout_targets. PreDesired/PreRevision are the device's shadow state
// *before* this rollout touched it (nil PreDesired means the device had no
// desired config at all yet — see engine.go's rollback for how that's
// handled); PushedRevision is the shadow revision this rollout expects the
// device to have applied, used to check whether a Reported.Rejected
// message is about this rollout's config or a stale, unrelated one.
type Target struct {
	DeviceID        string
	IncludedAtStage int
	PreDesired      *shadow.Desired
	PreRevision     uint64
	PushedRevision  uint64
}

// RolloutStore is what Engine needs to persist rollout state — satisfied
// by *Store (Postgres, below) in production and by an in-memory fake in
// engine_test.go, which is what makes the blueprint's "resumption after
// restart" scenario testable without a real database (see this package's
// doc comment / the plan this was built from for why).
type RolloutStore interface {
	Create(ctx context.Context, r *Rollout) error
	Get(ctx context.Context, id string) (*Rollout, error)
	List(ctx context.Context) ([]*Rollout, error)
	ListNonTerminal(ctx context.Context) ([]*Rollout, error)
	UpdateState(ctx context.Context, id string, state State) error
	AdvanceStage(ctx context.Context, id string, stageIndex int, startedAt time.Time) error
	RecordTarget(ctx context.Context, rolloutID string, t Target) error
	ListTargets(ctx context.Context, rolloutID string) ([]Target, error)
}

// Store is the Postgres-backed RolloutStore. Like internal/devices.Store
// and internal/alerts.Store, it's a thin I/O wrapper with no unit tests of
// its own — the orchestration logic it supports is tested against an
// in-memory fake in engine_test.go instead (no test-DB harness exists in
// this repo).
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

const rolloutColumns = "id, name, cohort, desired_config, stages, health_criteria, state, current_stage_index, current_stage_started_at, created_at, updated_at"

func (s *Store) Create(ctx context.Context, r *Rollout) error {
	cohortJSON, err := json.Marshal(r.Cohort)
	if err != nil {
		return fmt.Errorf("rollout: marshaling cohort: %w", err)
	}
	desiredJSON, err := json.Marshal(r.DesiredConfig)
	if err != nil {
		return fmt.Errorf("rollout: marshaling desired_config: %w", err)
	}
	stagesJSON, err := json.Marshal(r.Stages)
	if err != nil {
		return fmt.Errorf("rollout: marshaling stages: %w", err)
	}
	criteriaJSON, err := json.Marshal(r.HealthCriteria)
	if err != nil {
		return fmt.Errorf("rollout: marshaling health_criteria: %w", err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO rollouts (id, name, cohort, desired_config, stages, health_criteria, state, current_stage_index, current_stage_started_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		r.ID, r.Name, cohortJSON, desiredJSON, stagesJSON, criteriaJSON, r.State, r.CurrentStageIndex, r.CurrentStageStartedAt)
	if err != nil {
		return fmt.Errorf("rollout: creating %s: %w", r.ID, err)
	}
	return nil
}

func (s *Store) Get(ctx context.Context, id string) (*Rollout, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+rolloutColumns+` FROM rollouts WHERE id = $1`, id)
	r, err := scanRollout(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("rollout: getting %s: %w", id, err)
	}
	return r, nil
}

func (s *Store) List(ctx context.Context) ([]*Rollout, error) {
	return s.query(ctx, `SELECT `+rolloutColumns+` FROM rollouts ORDER BY created_at DESC`)
}

// ListNonTerminal returns every rollout still in a state the Engine needs
// to keep advancing — running or paused — which is what cmd/control calls
// at startup (Engine.Resume) to pick back up any rollout that was
// in-flight when the process last stopped.
func (s *Store) ListNonTerminal(ctx context.Context) ([]*Rollout, error) {
	return s.query(ctx, `SELECT `+rolloutColumns+` FROM rollouts WHERE state IN ('running', 'paused') ORDER BY created_at`)
}

func (s *Store) query(ctx context.Context, sql string, args ...any) ([]*Rollout, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("rollout: querying: %w", err)
	}
	defer rows.Close()

	var out []*Rollout
	for rows.Next() {
		r, err := scanRollout(rows)
		if err != nil {
			return nil, fmt.Errorf("rollout: scanning row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) UpdateState(ctx context.Context, id string, state State) error {
	_, err := s.pool.Exec(ctx, `UPDATE rollouts SET state = $2, updated_at = now() WHERE id = $1`, id, state)
	if err != nil {
		return fmt.Errorf("rollout: updating state for %s: %w", id, err)
	}
	return nil
}

func (s *Store) AdvanceStage(ctx context.Context, id string, stageIndex int, startedAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE rollouts SET current_stage_index = $2, current_stage_started_at = $3, updated_at = now()
		WHERE id = $1`,
		id, stageIndex, startedAt)
	if err != nil {
		return fmt.Errorf("rollout: advancing stage for %s: %w", id, err)
	}
	return nil
}

// RecordTarget is a no-op if deviceID is already recorded for rolloutID —
// a device's pre-rollout snapshot must reflect state before the *first*
// time this rollout touched it, not before whichever later stage happens
// to call RecordTarget again.
func (s *Store) RecordTarget(ctx context.Context, rolloutID string, t Target) error {
	var preDesiredJSON []byte
	if t.PreDesired != nil {
		var err error
		preDesiredJSON, err = json.Marshal(t.PreDesired)
		if err != nil {
			return fmt.Errorf("rollout: marshaling pre-rollout desired for %s: %w", t.DeviceID, err)
		}
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO rollout_targets (rollout_id, device_id, included_at_stage, pre_rollout_desired, pre_rollout_revision, pushed_revision)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (rollout_id, device_id) DO NOTHING`,
		rolloutID, t.DeviceID, t.IncludedAtStage, preDesiredJSON, t.PreRevision, t.PushedRevision)
	if err != nil {
		return fmt.Errorf("rollout: recording target %s for %s: %w", t.DeviceID, rolloutID, err)
	}
	return nil
}

func (s *Store) ListTargets(ctx context.Context, rolloutID string) ([]Target, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT device_id, included_at_stage, pre_rollout_desired, pre_rollout_revision, pushed_revision
		FROM rollout_targets WHERE rollout_id = $1`, rolloutID)
	if err != nil {
		return nil, fmt.Errorf("rollout: listing targets for %s: %w", rolloutID, err)
	}
	defer rows.Close()

	var out []Target
	for rows.Next() {
		var t Target
		var preDesiredJSON []byte
		if err := rows.Scan(&t.DeviceID, &t.IncludedAtStage, &preDesiredJSON, &t.PreRevision, &t.PushedRevision); err != nil {
			return nil, fmt.Errorf("rollout: scanning target row: %w", err)
		}
		if len(preDesiredJSON) > 0 {
			var d shadow.Desired
			if err := json.Unmarshal(preDesiredJSON, &d); err != nil {
				return nil, fmt.Errorf("rollout: unmarshaling pre-rollout desired: %w", err)
			}
			t.PreDesired = &d
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func scanRollout(row pgx.Row) (*Rollout, error) {
	var r Rollout
	var cohortJSON, desiredJSON, stagesJSON, criteriaJSON []byte
	if err := row.Scan(&r.ID, &r.Name, &cohortJSON, &desiredJSON, &stagesJSON, &criteriaJSON,
		&r.State, &r.CurrentStageIndex, &r.CurrentStageStartedAt, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(cohortJSON, &r.Cohort); err != nil {
		return nil, fmt.Errorf("unmarshaling cohort: %w", err)
	}
	if err := json.Unmarshal(desiredJSON, &r.DesiredConfig); err != nil {
		return nil, fmt.Errorf("unmarshaling desired_config: %w", err)
	}
	if err := json.Unmarshal(stagesJSON, &r.Stages); err != nil {
		return nil, fmt.Errorf("unmarshaling stages: %w", err)
	}
	if err := json.Unmarshal(criteriaJSON, &r.HealthCriteria); err != nil {
		return nil, fmt.Errorf("unmarshaling health_criteria: %w", err)
	}
	return &r, nil
}
