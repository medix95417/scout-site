// Package config loads runtime configuration from environment variables.
//
// Kept deliberately tiny: Phase 1 has few enough settings that a small
// struct plus os.Getenv is easier to reason about than a config library.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"
)

// Config holds everything the server needs to start.
type Config struct {
	// DatabaseURL is a standard Postgres connection string, e.g.
	// postgres://user:pass@host:5432/scoutsite?sslmode=disable — see
	// resolveDatabaseURL for how this gets built.
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

	// MailProvider selects the outbound transport: "" (default) uses
	// the SMTP fields above; "fastmail-jmap" instead sends over
	// Fastmail's JMAP HTTPS API (see internal/mailer/jmap.go), for a
	// host whose network blocks outbound SMTP entirely. When set,
	// SMTPHost/Port/Username/TLSMode are unused — SMTPFrom still
	// applies, since it picks which of the account's identities to send
	// from. FastmailAPIToken is environment-only, same as SMTPPassword
	// above and for the same reason: it's a real, usable credential, not
	// something to let a database dump/backup hand out in plaintext.
	MailProvider     string
	FastmailAPIToken string

	// TrustProxyHeaders says whether X-Forwarded-For can be believed when
	// working out a visitor's address for rate limiting (see
	// internal/web.clientIP). Only turn this on when a reverse proxy is
	// the ONLY route to this app — the Docker Compose stack puts Caddy in
	// front, so it's set there. Left off, the header is ignored entirely,
	// because a client can otherwise set it to anything and pick its own
	// rate-limit bucket.
	TrustProxyHeaders bool

	// ReminderWindow is how far ahead of an event's start time
	// -send-event-reminders looks when deciding a reminder is due.
	ReminderWindow time.Duration

	// S3* configure the S3-compatible object store backing the file
	// library and event photos (see internal/storage) — a self-hosted
	// MinIO you run separately, AWS S3, Cloudflare R2, or anything else
	// speaking the S3 API. S3Endpoint empty (the default) means file
	// storage is unconfigured: the rest of the site still runs, only the
	// file library/event photos degrade with a clear error, same as
	// SMTPHost empty above. Set S3_ENDPOINT/S3_ACCESS_KEY/S3_SECRET_KEY
	// (see docker-compose.yml and .env.example) for uploads to work.
	S3Endpoint  string
	S3AccessKey string
	S3SecretKey string
	S3Bucket    string
	S3UseSSL    bool
}

// Load reads configuration from the environment, applying sane local-dev
// defaults so `go run ./cmd/server` works without a .env file present.
func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:   resolveDatabaseURL(),
		ListenAddr:    getenv("LISTEN_ADDR", ":8080"),
		CookieDomain:  getenv("COOKIE_DOMAIN", ""),
		SessionSecret: getenv("SESSION_SECRET", ""),

		SMTPHost:     getenv("SMTP_HOST", ""),
		SMTPPort:     getenv("SMTP_PORT", "587"),
		SMTPUsername: getenv("SMTP_USERNAME", ""),
		SMTPPassword: getenv("SMTP_PASSWORD", ""),
		SMTPFrom:     getenv("SMTP_FROM", ""),
		SMTPTLSMode:  getenv("SMTP_TLS_MODE", "starttls"),

		MailProvider:      getenv("MAIL_PROVIDER", ""),
		TrustProxyHeaders: getenv("TRUST_PROXY_HEADERS", "") == "true",
		FastmailAPIToken:  getenv("FASTMAIL_API_TOKEN", ""),

		// Empty S3Endpoint is the safe default — it means "storage
		// unconfigured," not "try to reach some placeholder host." See
		// storage.New's doc comment.
		S3Endpoint:  getenv("S3_ENDPOINT", ""),
		S3AccessKey: getenv("S3_ACCESS_KEY", ""),
		S3SecretKey: getenv("S3_SECRET_KEY", ""),
		S3Bucket:    getenv("S3_BUCKET", "scoutsite-files"),
		S3UseSSL:    getenv("S3_USE_SSL", "false") == "true",
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

// resolveDatabaseURL builds the Postgres connection string. DATABASE_URL,
// if set, is used verbatim — the escape hatch for an external managed
// Postgres (RDS, etc.) where a caller already has a complete, correct
// URL. Otherwise it's built from DB_HOST/DB_PORT/DB_USER/DB_PASSWORD/
// DB_NAME/DB_SSLMODE (docker-compose.yml's app service sets these) via
// net/url, which percent-encodes each piece correctly — unlike splicing
// a raw password into a "postgres://user:PASSWORD@host/db" string by
// hand, which breaks (silently produces an unparseable URL) the moment
// the password contains a character like "/" that a generator such as
// `openssl rand -base64 N` can and does produce. Local dev with no env
// vars set at all still gets the same friendly default as before.
func resolveDatabaseURL() string {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(getenv("DB_USER", "scoutsite"), getenv("DB_PASSWORD", "scoutsite")),
		Host:     getenv("DB_HOST", "localhost") + ":" + getenv("DB_PORT", "5432"),
		Path:     "/" + getenv("DB_NAME", "scoutsite"),
		RawQuery: "sslmode=" + getenv("DB_SSLMODE", "disable"),
	}
	return u.String()
}
