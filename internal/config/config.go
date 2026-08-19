// Package config loads runtime configuration from environment variables.
//
// Kept deliberately tiny: Phase 1 has few enough settings that a small
// struct plus os.Getenv is easier to reason about than a config library.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds everything the server needs to start.
type Config struct {
	// DatabaseURL is a standard Postgres connection string, e.g.
	// postgres://user:pass@host:5432/scoutsite?sslmode=disable
	DatabaseURL string

	// ListenAddr is the host:port the HTTP server binds to.
	ListenAddr string

	// CookieDomain is the parent domain session cookies are scoped to,
	// e.g. ".47-yonkers.org" in production, or "" for local development
	// (empty scopes the cookie to the exact host only, which is what you
	// want when testing against localhost/127.0.0.1).
	CookieDomain string

	// SessionSecret is required at startup and must be at least 32 bytes,
	// as a deliberate operational forcing function: it makes every deploy
	// generate and store a real secret (openssl rand -base64 32) up front,
	// the same secret this config will use once Phase 2 needs to sign
	// something (e.g. a webhook payload). It is NOT currently used to sign
	// anything — see package auth's doc comment: sessions are opaque
	// crypto/rand tokens validated against a server-side sessions table,
	// not signed cookies, so there's nothing to sign yet. Kept required
	// now rather than added later so "set SESSION_SECRET" is already a
	// forgotten step nobody has to remember to add.
	SessionSecret string

	// SMTP* configure outbound email (password reset links, event
	// reminders — see internal/mailer). SMTPHost empty means email is
	// disabled: the site still runs, but password reset and reminders
	// degrade gracefully with a clear error rather than crashing.
	SMTPHost     string
	SMTPPort     string
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string
	SMTPTLSMode  string // "starttls" (default, port 587) or "tls" (implicit TLS, port 465)

	// ReminderWindow is how far ahead of an event's start time
	// -send-event-reminders looks when deciding a reminder is due.
	ReminderWindow time.Duration
}

// Load reads configuration from the environment, applying sane local-dev
// defaults so `go run ./cmd/server` works without a .env file present.
func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:   getenv("DATABASE_URL", "postgres://scoutsite:scoutsite@localhost:5432/scoutsite?sslmode=disable"),
		ListenAddr:    getenv("LISTEN_ADDR", ":8080"),
		CookieDomain:  getenv("COOKIE_DOMAIN", ""),
		SessionSecret: getenv("SESSION_SECRET", ""),

		SMTPHost:     getenv("SMTP_HOST", ""),
		SMTPPort:     getenv("SMTP_PORT", "587"),
		SMTPUsername: getenv("SMTP_USERNAME", ""),
		SMTPPassword: getenv("SMTP_PASSWORD", ""),
		SMTPFrom:     getenv("SMTP_FROM", ""),
		SMTPTLSMode:  getenv("SMTP_TLS_MODE", "starttls"),
	}

	if cfg.SessionSecret == "" {
		return cfg, fmt.Errorf("config: SESSION_SECRET is required (generate one with: openssl rand -base64 32)")
	}
	if len(cfg.SessionSecret) < 32 {
		return cfg, fmt.Errorf("config: SESSION_SECRET must be at least 32 characters")
	}

	reminderHours := getenv("REMINDER_WINDOW_HOURS", "24")
	hours, err := strconv.ParseFloat(reminderHours, 64)
	if err != nil || hours <= 0 {
		return cfg, fmt.Errorf("config: REMINDER_WINDOW_HOURS must be a positive number (got %q)", reminderHours)
	}
	cfg.ReminderWindow = time.Duration(hours * float64(time.Hour))

	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
