// Package db owns the Postgres connection pool and a minimal migration
// runner. Deliberately hand-rolled instead of pulling in a migration
// framework: Phase 1 has a handful of migration files and the logic to
// apply "any .sql file not yet recorded in schema_migrations" is about
// thirty lines — not worth a dependency yet. Revisit if Phase 2 needs
// down-migrations or more sophistication.
package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed all:migrations
var migrationsFS embed.FS

// migrationFilePattern matches numbered migration files (0001_init.sql) but
// deliberately excludes seed.sql, which is applied separately and on
// purpose, not automatically at every startup.
var migrationFilePattern = regexp.MustCompile(`^\d+_.*\.sql$`)

// Connect opens a connection pool to Postgres and verifies it's reachable.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("db: creating pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping failed (is Postgres running and DATABASE_URL correct?): %w", err)
	}
	return pool, nil
}

// Migrate applies any migration files that haven't been recorded yet, in
// filename order, each inside its own transaction.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename    text PRIMARY KEY,
			applied_at  timestamptz NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("db: creating schema_migrations table: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("db: reading embedded migrations: %w", err)
	}

	var filenames []string
	for _, e := range entries {
		if e.IsDir() || !migrationFilePattern.MatchString(e.Name()) {
			continue
		}
		filenames = append(filenames, e.Name())
	}
	sort.Strings(filenames)

	for _, filename := range filenames {
		var already bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE filename = $1)`,
			filename,
		).Scan(&already)
		if err != nil {
			return fmt.Errorf("db: checking migration status for %s: %w", filename, err)
		}
		if already {
			continue
		}

		sqlBytes, err := migrationsFS.ReadFile("migrations/" + filename)
		if err != nil {
			return fmt.Errorf("db: reading %s: %w", filename, err)
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("db: beginning transaction for %s: %w", filename, err)
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("db: applying %s: %w", filename, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (filename) VALUES ($1)`, filename,
		); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("db: recording %s: %w", filename, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("db: committing %s: %w", filename, err)
		}
		fmt.Printf("db: applied migration %s\n", filename)
	}

	return nil
}

// Seed applies migrations/seed.sql directly (not tracked in
// schema_migrations, since it's meant to be safe to re-run and isn't part
// of the schema's version history).
func Seed(ctx context.Context, pool *pgxpool.Pool) error {
	sqlBytes, err := migrationsFS.ReadFile("migrations/seed.sql")
	if err != nil {
		return fmt.Errorf("db: reading seed.sql: %w", err)
	}
	if _, err := pool.Exec(ctx, string(sqlBytes)); err != nil {
		return fmt.Errorf("db: applying seed.sql: %w", err)
	}
	fmt.Println("db: seed applied")
	return nil
}
