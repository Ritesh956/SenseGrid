// Package migrations is a deliberately small, forward-only SQL migration
// runner — not golang-migrate. This project only ever needs to move the
// schema forward (no down-migrations, no dev team running divergent
// branches against the same database), so a ~100-line runner under our
// own control beats pulling in and learning a general-purpose migration
// library's API for that single use case.
//
// Each *.sql file under the migrations directory is applied at most once,
// in filename order, tracked in a schema_migrations table. A file is run
// inside its own transaction unless its first line is exactly
// "-- no-transaction", needed for TimescaleDB DDL (continuous aggregates,
// compression/retention policies) that isn't guaranteed safe inside an
// explicit BEGIN/COMMIT across TimescaleDB versions.
package migrations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres error codes that mean "another process already applied this" —
// control and processor both call Run() at startup with no coordination
// between them, so on a cold start they can race to apply the same first
// migration. Each file runs in its own transaction, so the loser's
// transaction rolls back cleanly; treating these specific codes as
// "already done" rather than fatal is what makes that race harmless
// instead of a startup crash.
var raceOK = map[string]bool{
	"42P07": true, // duplicate_table
	"42710": true, // duplicate_object
	"23505": true, // unique_violation (schema_migrations PK)
}

func isConcurrentApplyRace(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && raceOK[pgErr.Code]
}

// Run applies every not-yet-applied *.sql file in dir, in filename order.
func Run(ctx context.Context, pool *pgxpool.Pool, dir string) error {
	if err := ensureMigrationsTable(ctx, pool); err != nil && !isConcurrentApplyRace(err) {
		// Even CREATE TABLE IF NOT EXISTS isn't immune to this: two
		// concurrent callers can both pass the "does not exist" check
		// before either commits, and one loses to a catalog-level unique
		// constraint (pg_type_typname_nsp_index) despite IF NOT EXISTS —
		// a known Postgres race, not specific to this table.
		return fmt.Errorf("migrations: preparing tracking table: %w", err)
	}
	applied, err := appliedVersions(ctx, pool)
	if err != nil {
		return fmt.Errorf("migrations: reading applied versions: %w", err)
	}

	files, err := migrationFiles(dir)
	if err != nil {
		return fmt.Errorf("migrations: listing %s: %w", dir, err)
	}

	for _, path := range files {
		version := filepath.Base(path)
		if applied[version] {
			continue
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("migrations: reading %s: %w", version, err)
		}
		statements := splitStatements(string(raw))
		noTx := strings.HasPrefix(strings.TrimSpace(string(raw)), "-- no-transaction")

		if noTx {
			err = runWithoutTx(ctx, pool, statements)
		} else {
			err = runInTx(ctx, pool, statements)
		}
		if err != nil {
			if isConcurrentApplyRace(err) {
				continue // another process applied this first; move on
			}
			return fmt.Errorf("migrations: applying %s: %w", version, err)
		}

		if _, err := pool.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			if isConcurrentApplyRace(err) {
				continue
			}
			return fmt.Errorf("migrations: recording %s as applied: %w", version, err)
		}
	}
	return nil
}

func ensureMigrationsTable(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version     text PRIMARY KEY,
			applied_at  timestamptz NOT NULL DEFAULT now()
		)`)
	return err
}

func appliedVersions(ctx context.Context, pool *pgxpool.Pool) (map[string]bool, error) {
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	applied := make(map[string]bool)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

func migrationFiles(dir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func runInTx(ctx context.Context, pool *pgxpool.Pool, statements []string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	for _, stmt := range statements {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func runWithoutTx(ctx context.Context, pool *pgxpool.Pool, statements []string) error {
	for _, stmt := range statements {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// splitStatements splits a migration file into individual statements on
// top-level semicolons. It's intentionally naive (no awareness of
// semicolons inside string literals) — acceptable because every
// migration in this repo is written by us and avoids that case, not
// because it's a general-purpose SQL splitter.
func splitStatements(sqlText string) []string {
	var lines []string
	for _, line := range strings.Split(sqlText, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		lines = append(lines, line)
	}
	joined := strings.Join(lines, "\n")

	var out []string
	for _, part := range strings.Split(joined, ";") {
		if s := strings.TrimSpace(part); s != "" {
			out = append(out, s)
		}
	}
	return out
}
