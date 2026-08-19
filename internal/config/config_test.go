package config

import (
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// withEnv sets env vars for the duration of the test and restores
// whatever was there before (including "unset") afterward.
func withEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		prev, had := os.LookupEnv(k)
		os.Setenv(k, v)
		t.Cleanup(func() {
			if had {
				os.Setenv(k, prev)
			} else {
				os.Unsetenv(k)
			}
		})
	}
}

func TestResolveDatabaseURL_Default(t *testing.T) {
	withEnv(t, map[string]string{"DATABASE_URL": "", "DB_HOST": "", "DB_PASSWORD": ""})
	got := resolveDatabaseURL()
	want := "postgres://scoutsite:scoutsite@localhost:5432/scoutsite?sslmode=disable"
	if got != want {
		t.Errorf("resolveDatabaseURL() = %q, want %q", got, want)
	}
}

func TestResolveDatabaseURL_ExplicitOverrideWins(t *testing.T) {
	withEnv(t, map[string]string{"DATABASE_URL": "postgres://someone:else@somewhere/db"})
	got := resolveDatabaseURL()
	if got != "postgres://someone:else@somewhere/db" {
		t.Errorf("an explicit DATABASE_URL should be used verbatim, got %q", got)
	}
}

// TestResolveDatabaseURL_PasswordWithSlash is the regression test for the
// production incident this fix addresses: `openssl rand -base64 24` (what
// DEPLOY.md tells operators to run for POSTGRES_PASSWORD) can produce a
// "/" in its output. docker-compose.yml used to splice that raw value
// into a "postgres://user:PASSWORD@host/db" string with no escaping,
// which pgx's URL parser then rejected with "invalid port ... after
// host" — the app crash-looped, which was also why Caddy's reverse proxy
// returned 502s. This confirms a password containing "/" (and other
// URL-meaningful characters) now round-trips through resolveDatabaseURL
// into something pgxpool.ParseConfig accepts.
func TestResolveDatabaseURL_PasswordWithSlash(t *testing.T) {
	withEnv(t, map[string]string{
		"DATABASE_URL": "",
		"DB_HOST":      "db",
		"DB_PORT":      "5432",
		"DB_USER":      "scoutsite",
		"DB_PASSWORD":  "0ZBSf3ha1/0jYJ0YLBJCzD88ncDqy1xl",
		"DB_NAME":      "scoutsite",
		"DB_SSLMODE":   "disable",
	})

	got := resolveDatabaseURL()

	if _, err := pgxpool.ParseConfig(got); err != nil {
		t.Fatalf("resolveDatabaseURL() produced a URL pgx can't parse: %v (url: %q)", err, got)
	}
	// The raw "/" must not appear unescaped between the credentials and
	// the "@" — that's exactly the shape that broke before this fix.
	if before, _, found := strings.Cut(got, "@"); found && strings.Contains(before, "/0jYJ0YLBJCzD88ncDqy1xl") {
		t.Errorf("password's \"/\" was not percent-encoded: %q", got)
	}
}

func TestResolveDatabaseURL_PasswordWithOtherReservedChars(t *testing.T) {
	withEnv(t, map[string]string{
		"DATABASE_URL": "",
		"DB_HOST":      "db",
		"DB_PASSWORD":  "p@ss:word/with?reserved#chars",
	})

	got := resolveDatabaseURL()

	if _, err := pgxpool.ParseConfig(got); err != nil {
		t.Fatalf("resolveDatabaseURL() produced a URL pgx can't parse: %v (url: %q)", err, got)
	}
}
