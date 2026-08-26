package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Alert is one row of the alerts table.
type Alert struct {
	ID             string
	DeviceID       string
	SensorType     string
	RuleName       string
	Severity       string
	State          State
	Detail         map[string]any
	FiredAt        time.Time
	AcknowledgedAt *time.Time
	ResolvedAt     *time.Time
	UpdatedAt      time.Time
}

// Store is the Postgres-backed alert repository. Like internal/devices.Store,
// it's a thin I/O wrapper with no unit tests of its own (no test-DB harness
// exists in this repo yet) — the state-machine logic it enforces is tested
// separately, in Machine (machine_test.go).
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

const alertColumns = "id, device_id, sensor_type, rule_name, severity, state, detail, fired_at, acknowledged_at, resolved_at, updated_at"

// Open records a new firing alert for (deviceID, sensorType, ruleName),
// generating id itself (matching internal/devices' convention of
// Go-generated UUIDs rather than a Postgres default). If an alert for the
// same key is already open (not resolved), the partial unique index
// alerts_open_idx rejects the insert with a unique_violation and Open
// instead returns that existing row with opened=false — this is what
// makes "never re-fire an already-firing alert" true even under a
// defensive double-call, on top of anomaly.Evaluator already being
// edge-triggered.
func (s *Store) Open(ctx context.Context, id, deviceID, sensorType, ruleName, severity string, detail map[string]any, firedAt time.Time) (alert *Alert, opened bool, err error) {
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		return nil, false, fmt.Errorf("alerts: marshaling detail: %w", err)
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO alerts (id, device_id, sensor_type, rule_name, severity, state, detail, fired_at)
		VALUES ($1, $2, $3, $4, $5, 'firing', $6, $7)
		RETURNING `+alertColumns,
		id, deviceID, sensorType, ruleName, severity, detailJSON, firedAt)
	a, err := scanAlert(row)
	if err == nil {
		return a, true, nil
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		existing, ferr := s.getOpen(ctx, deviceID, sensorType, ruleName)
		return existing, false, ferr
	}
	return nil, false, fmt.Errorf("alerts: opening %s/%s/%s: %w", deviceID, sensorType, ruleName, err)
}

// Resolve transitions the currently-open alert for (deviceID, sensorType,
// ruleName) to Resolved. Returns (nil, nil) if there is no open alert for
// that key — anomaly.Evaluator only calls this on an edge-triggered
// Cleared transition, so that should be rare, not an error condition.
func (s *Store) Resolve(ctx context.Context, deviceID, sensorType, ruleName string, resolvedAt time.Time) (*Alert, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE alerts SET state = 'resolved', resolved_at = $4, updated_at = now()
		WHERE device_id = $1 AND sensor_type = $2 AND rule_name = $3 AND state <> 'resolved'
		RETURNING `+alertColumns,
		deviceID, sensorType, ruleName, resolvedAt)
	a, err := scanAlert(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("alerts: resolving %s/%s/%s: %w", deviceID, sensorType, ruleName, err)
	}
	return a, nil
}

// ResolveByID transitions a non-resolved alert (by id) to Resolved — the
// operator-driven counterpart to the key-based Resolve above (which
// anomaly.Evaluator uses for automatic clear-on-condition). Used by
// Phase 4's POST /v1/alerts/{id}/resolve handler.
func (s *Store) ResolveByID(ctx context.Context, id string, resolvedAt time.Time) (*Alert, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE alerts SET state = 'resolved', resolved_at = $2, updated_at = now()
		WHERE id = $1 AND state <> 'resolved'
		RETURNING `+alertColumns, id, resolvedAt)
	a, err := scanAlert(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("alerts: %s is not an open alert", id)
	}
	if err != nil {
		return nil, fmt.Errorf("alerts: resolving %s: %w", id, err)
	}
	return a, nil
}

// Acknowledge transitions a firing alert (by id) to Acknowledged. Used by
// Phase 4's POST /v1/alerts/{id}/ack handler.
func (s *Store) Acknowledge(ctx context.Context, id string) (*Alert, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE alerts SET state = 'acknowledged', acknowledged_at = now(), updated_at = now()
		WHERE id = $1 AND state = 'firing'
		RETURNING `+alertColumns, id)
	a, err := scanAlert(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("alerts: %s is not a firing alert", id)
	}
	if err != nil {
		return nil, fmt.Errorf("alerts: acknowledging %s: %w", id, err)
	}
	return a, nil
}

// FiringCountForDevices counts how many of deviceIDs have at least one
// alert that has been firing since (opened at or after) since — the
// "error rate" signal Phase 4's rollout engine (internal/rollout) uses to
// decide whether a stage is healthy, reusing Phase 3's alert data instead
// of a parallel error-tracking mechanism.
func (s *Store) FiringCountForDevices(ctx context.Context, deviceIDs []string, since time.Time) (int, error) {
	if len(deviceIDs) == 0 {
		return 0, nil
	}
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT count(DISTINCT device_id) FROM alerts
		WHERE device_id = ANY($1::uuid[]) AND state = 'firing' AND fired_at >= $2`,
		deviceIDs, since).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("alerts: counting firing for devices: %w", err)
	}
	return count, nil
}

// Filter narrows List: a zero State matches every state, an empty
// DeviceID matches every device, and Limit <= 0 falls back to
// defaultListLimit.
type Filter struct {
	State    State
	DeviceID string
	Limit    int
}

const defaultListLimit = 200

// List returns alerts newest-first, optionally narrowed by Filter — the
// backing query for Phase 5's GET /v1/alerts (fleet alert badges and the
// console's Alerts view), the first read path this store has needed beyond
// the single-alert lookups above.
func (s *Store) List(ctx context.Context, filter Filter) ([]*Alert, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}

	query := `SELECT ` + alertColumns + ` FROM alerts WHERE 1 = 1`
	args := []any{}
	if filter.State != "" {
		args = append(args, filter.State)
		query += fmt.Sprintf(" AND state = $%d", len(args))
	}
	if filter.DeviceID != "" {
		args = append(args, filter.DeviceID)
		query += fmt.Sprintf(" AND device_id = $%d", len(args))
	}
	args = append(args, limit)
	query += fmt.Sprintf(" ORDER BY fired_at DESC LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("alerts: listing: %w", err)
	}
	defer rows.Close()

	var out []*Alert
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, fmt.Errorf("alerts: scanning list row: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) getOpen(ctx context.Context, deviceID, sensorType, ruleName string) (*Alert, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+alertColumns+`
		FROM alerts
		WHERE device_id = $1 AND sensor_type = $2 AND rule_name = $3 AND state <> 'resolved'`,
		deviceID, sensorType, ruleName)
	return scanAlert(row)
}

func scanAlert(row pgx.Row) (*Alert, error) {
	var a Alert
	var detailJSON []byte
	if err := row.Scan(&a.ID, &a.DeviceID, &a.SensorType, &a.RuleName, &a.Severity, &a.State,
		&detailJSON, &a.FiredAt, &a.AcknowledgedAt, &a.ResolvedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	if len(detailJSON) > 0 {
		if err := json.Unmarshal(detailJSON, &a.Detail); err != nil {
			return nil, fmt.Errorf("alerts: unmarshaling detail: %w", err)
		}
	}
	return &a, nil
}
