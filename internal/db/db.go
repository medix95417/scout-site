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
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"regexp"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed all:migrations
var migrationsFS embed.FS

// migrationFilePattern matches numbered migration files (0001_init.sql) but
// deliberately excludes seed.sql, which is applied separately and on
// purpose, not automatically at every startup.
var migrationFilePattern = regexp.MustCompile(`^\d+_.*\.sql$`)

// connectWait is how long Connect keeps retrying an unreachable
// Postgres before giving up, and connectFirstBackoff the pause before
// the first retry (each subsequent one waits twice as long, capped at a
// second). Both are vars rather than consts so a test can shrink them.
var (
	connectWait         = 30 * time.Second
	connectFirstBackoff = 100 * time.Millisecond
	connectMaxBackoff   = 1 * time.Second
)

// Connect opens a connection pool to Postgres and verifies it's
// reachable, retrying a refused or unresolvable server for up to
// connectWait before giving up.
//
// The retry is for one situation, which happens on nearly every reboot:
// this process and Postgres start together, and Postgres is not
// accepting connections yet. Without the retry the first ping fails, the
// caller exits, and the container's restart policy brings it back —
// which does eventually work, but the restart backoff doubles each time,
// so a Postgres that takes a while over WAL recovery leaves the site
// down for considerably longer than the database itself took. Waiting a
// few seconds in-process turns that into a clean single start.
//
// Only connection-level failures are retried. A wrong password or a
// missing database is a configuration mistake that will read the same
// way in thirty seconds, so those fail immediately rather than making
// someone watch a doomed loop.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	deadline := time.Now().Add(connectWait)
	backoff := connectFirstBackoff
	for attempt := 1; ; attempt++ {
		pool, err := pgxpool.New(ctx, databaseURL)
		if err != nil {
			// A malformed URL: retrying cannot help.
			return nil, fmt.Errorf("db: creating pool: %w", err)
		}
		err = pool.Ping(ctx)
		if err == nil {
			return pool, nil
		}
		pool.Close()

		if !worthRetrying(err) || time.Now().After(deadline) || ctx.Err() != nil {
			return nil, fmt.Errorf("db: ping failed (is Postgres running and DATABASE_URL correct?): %w", err)
		}
		if attempt == 1 {
			log.Printf("db: Postgres not reachable yet (%v) — retrying for up to %s", err, connectWait)
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil, fmt.Errorf("db: ping failed (is Postgres running and DATABASE_URL correct?): %w", err)
		}
		if backoff *= 2; backoff > connectMaxBackoff {
			backoff = connectMaxBackoff
		}
	}
}

// worthRetrying reports whether an error looks like "Postgres isn't up
// yet" rather than "you configured this wrong".
//
// Matched on the error's shape where possible — a dial failure or a DNS
// lookup failure is unambiguous — and otherwise on the one server-side
// code that means the same thing: 57P03, the server is running but still
// starting up and refusing connections. A pgconn.PgError of any other
// code came from a server that answered, so it is an answer, not an
// outage: the wrong password (28P01) and a missing database (3D000) both
// land here and fail at once.
func worthRetrying(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "57P03" // cannot_connect_now
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr)
}

// Migrate applies any migration files that haven't been recorded yet, in
// filename order, each inside its own transaction.
// migrateLockKey is an arbitrary but fixed identifier for the Postgres
// advisory lock that serialises Migrate. Any constant works as long as
// nothing else in the system picks the same one.
const migrateLockKey int64 = 8412559930274160001

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	// Only one migration run at a time, per database.
	//
	// Without this, two processes starting together each read
	// schema_migrations, both conclude a migration is unapplied, and both
	// try to apply it — the second failing with "already exists" and
	// taking its process down with it. That is not hypothetical: it is
	// exactly what the test suite does, since `go test ./...` runs
	// packages in parallel and internal/ledger, internal/roster and
	// internal/prospect each migrate the same test database. It is also
	// reachable in production the moment the app is started with more
	// than one replica, or restarted while the old container is still up.
	//
	// The lock is held on one connection for the whole run and released
	// by the defer, so a crashed process drops it when its connection
	// closes rather than wedging every future start.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("db: acquiring connection for migration lock: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrateLockKey); err != nil {
		return fmt.Errorf("db: taking migration lock: %w", err)
	}
	defer func() {
		// Best effort: if this fails the connection is being returned to
		// the pool in a bad state anyway, and closing it drops the lock.
		if _, err := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrateLockKey); err != nil {
			log.Printf("db: releasing migration lock: %v", err)
		}
	}()

	if _, err := conn.Exec(ctx, `
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
		err := conn.QueryRow(ctx,
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

		tx, err := conn.Begin(ctx)
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
