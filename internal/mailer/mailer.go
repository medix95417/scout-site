// Package mailer sends plain-text email over SMTP — the transport for
// password reset links and event reminders (see internal/auth and
// internal/reminders). Deliberately built on the standard library's
// net/smtp rather than a third-party client: Phase 1's sandbox couldn't
// fetch new Go dependencies at all (see README's compile caveat), and a
// hand-rolled SMTP send is about eighty lines — not worth a dependency
// even without that constraint.
//
// Plain text, not HTML: scouting emails (a reset link, "you're signed up
// for Saturday's campout") don't need styling, and skipping HTML/MIME
// multipart entirely avoids an entire class of rendering bugs email clients
// are famous for.
package mailer

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/settings"
)

// Config configures the outbound SMTP connection. An empty Host means
// email is disabled — see Enabled.
type Config struct {
	Host     string
	Port     string // e.g. "587" (STARTTLS, the common case) or "465" (implicit TLS)
	Username string
	Password string
	From     string // e.g. "Troop 47 <noreply@47-yonkers.org>" — used as both the envelope and header From
	TLSMode  string // "starttls" (default) or "tls" (implicit TLS, typically port 465)
}

// Enabled reports whether enough configuration is present to attempt
// sending. Callers (password reset, reminders) should check this and
// degrade gracefully — a scouting site without a configured mail provider
// shouldn't be unable to log in or view its calendar.
func (c Config) Enabled() bool {
	return strings.TrimSpace(c.Host) != ""
}

// Mailer holds both the environment-sourced fallback Config (cfg — see
// internal/config) and, optionally, a database pool used to resolve
// per-send overrides for the non-secret half of that config (host, port,
// username, from address — see internal/settings.TextSettings) so an
// admin can change those from /admin/settings without editing environment
// variables or restarting the process. cfg.Password is deliberately never
// overridden this way — see effective's comment.
type Mailer struct {
	cfg  Config
	pool *pgxpool.Pool
}

// New builds a Mailer from its environment-sourced fallback Config and a
// database pool for resolving DB-editable overrides. pool may be nil (e.g.
// in a test that only needs the env-var Config) — effective then simply
// returns cfg unchanged.
func New(cfg Config, pool *pgxpool.Pool) *Mailer {
	return &Mailer{cfg: cfg, pool: pool}
}

// effective resolves the Config actually used for one send: any
// /admin/settings-stored override for Host/Port/Username/From (see
// internal/settings.GetText), falling back to the environment-sourced cfg
// for anything not overridden. Password is deliberately excluded from
// this override mechanism entirely — it always comes from cfg (i.e. the
// SMTP_PASSWORD environment variable) regardless of what's stored in the
// database. That's a deliberate security boundary, not an oversight: this
// codebase has no other precedent for storing a plaintext secret in
// Postgres (every other secret-shaped value — user passwords — is
// bcrypt-hashed before it's ever written), and an SMTP password is a
// real, usable credential a database dump/backup shouldn't hand out in
// plaintext. See README.md/SECURITY_AUDIT.md.
//
// A failure to read the overrides (e.g. a transient DB hiccup) fails open
// to cfg rather than erroring the whole send — a settings-table read
// glitch shouldn't be the reason a password reset email doesn't go out
// when the environment-configured mail server would have worked fine.
func (m *Mailer) effective(ctx context.Context) Config {
	if m.pool == nil {
		return m.cfg
	}
	overrides, err := settings.AllText(ctx, m.pool)
	if err != nil {
		log.Printf("mailer: loading SMTP setting overrides (falling back to environment config): %v", err)
		return m.cfg
	}
	eff := m.cfg
	if v := overrides[settings.SMTPHost]; v != "" {
		eff.Host = v
	}
	if v := overrides[settings.SMTPPort]; v != "" {
		eff.Port = v
	}
	if v := overrides[settings.SMTPUsername]; v != "" {
		eff.Username = v
	}
	if v := overrides[settings.SMTPFrom]; v != "" {
		eff.From = v
	}
	return eff
}

// Enabled reports whether enough configuration — environment, database
// override, or a mix of both — is present to attempt sending.
func (m *Mailer) Enabled(ctx context.Context) bool {
	return m.effective(ctx).Enabled()
}

// Send delivers a single plain-text email. Bounded by a connection timeout
// so a misconfigured or unreachable SMTP server can't hang the caller
// indefinitely — that matters here since this can be called synchronously
// from an HTTP handler (forgot-password) as well as from the batch
// reminders job.
func (m *Mailer) Send(ctx context.Context, to, subject, body string) error {
	cfg := m.effective(ctx)
	if !cfg.Enabled() {
		return fmt.Errorf("mailer: not configured — set SMTP_HOST (and SMTP_PORT/SMTP_USERNAME/SMTP_PASSWORD/SMTP_FROM), or fill in the host/port/username/from fields on /admin/settings, to enable email")
	}

	fromAddr, err := extractAddr(cfg.From)
	if err != nil {
		return fmt.Errorf("mailer: invalid SMTP From address %q: %w", cfg.From, err)
	}
	if _, err := mail.ParseAddress(to); err != nil {
		return fmt.Errorf("mailer: invalid recipient address %q: %w", to, err)
	}

	addr := net.JoinHostPort(cfg.Host, cfg.Port)
	dialer := &net.Dialer{Timeout: 15 * time.Second}

	rawConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("mailer: connecting to %s: %w", addr, err)
	}
	defer rawConn.Close() //nolint:errcheck // best-effort; the real error (if any) already surfaced above

	var conn net.Conn = rawConn
	if cfg.TLSMode == "tls" {
		conn = tls.Client(rawConn, &tls.Config{ServerName: cfg.Host})
	}

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("mailer: SMTP handshake with %s: %w", addr, err)
	}
	defer client.Close() //nolint:errcheck // best-effort cleanup

	if cfg.TLSMode != "tls" {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
				return fmt.Errorf("mailer: STARTTLS: %w", err)
			}
		}
	}

	if cfg.Username != "" {
		auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
		if ok, _ := client.Extension("AUTH"); ok {
			if err := client.Auth(auth); err != nil {
				return fmt.Errorf("mailer: authenticating as %s: %w", cfg.Username, err)
			}
		}
	}

	if err := client.Mail(fromAddr); err != nil {
		return fmt.Errorf("mailer: MAIL FROM: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("mailer: RCPT TO %s: %w", to, err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("mailer: DATA: %w", err)
	}
	if _, err := w.Write([]byte(buildMessage(cfg.From, to, subject, body))); err != nil {
		return fmt.Errorf("mailer: writing message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mailer: closing message: %w", err)
	}

	return client.Quit()
}

// extractAddr pulls just the bare address (no display name) out of a
// header-style From value like "Troop 47 <noreply@47-yonkers.org>", since
// the SMTP envelope MAIL FROM command wants a bare address.
func extractAddr(from string) (string, error) {
	addr, err := mail.ParseAddress(from)
	if err != nil {
		return "", err
	}
	return addr.Address, nil
}

// buildMessage assembles a minimal, correct RFC 5322 message: headers plus
// a plain-text UTF-8 body, CRLF line endings throughout (matching the
// canonical net/smtp.SendMail usage pattern from the standard library's own
// documentation). Note: this deliberately does NOT hand-roll SMTP dot-
// stuffing (escaping a body line that's just ".") — client.Data() returns a
// textproto DotWriter, which already does that transparently for whatever
// well-formed, CRLF-terminated message it's given. Doing it here too would
// double-escape.
func buildMessage(from, to, subject, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=\"utf-8\"\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	for _, line := range strings.Split(body, "\n") {
		b.WriteString(strings.TrimSuffix(line, "\r"))
		b.WriteString("\r\n")
	}
	return b.String()
}
