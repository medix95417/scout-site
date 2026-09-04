package db

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Migrate has to be safe to run from several processes at once.
//
// This is not a theoretical concern. `go test ./...` runs packages in
// parallel, and internal/ledger, internal/roster and internal/prospect
// each migrate the same TEST_DATABASE_URL on startup — which is how this
// surfaced: three packages racing, each failing on a different migration
// with "already exists" depending on who got there first. The same thing
// is reachable in production the moment the app runs more than one
// replica, or is restarted before the old container has exited.
//
// Skips without a database, like the other DB-backed tests here, so
// `go test ./...` still works on a laptop with no Postgres.
func TestConcurrentMigrateIsSafe(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping concurrent migration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connecting to test database: %v", err)
	}
	defer pool.Close()

	const racers = 6
	errs := make([]error, racers)
	var wg sync.WaitGroup
	// Released all at once, so the migrators overlap as hard as possible
	// rather than trickling in and accidentally serialising themselves.
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = Migrate(ctx, pool)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent migrator %d failed: %v", i, err)
		}
	}
}
