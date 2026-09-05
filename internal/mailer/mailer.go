// Package mailer sends email — the transport for password reset links
// and event reminders (see internal/auth and internal/reminders). Two
// transports: plain SMTP (net/smtp, no third-party client — Phase 1's
// sandbox couldn't fetch new Go dependencies at all, and a hand-rolled
// SMTP send is about eighty lines, not worth a dependency even without
// that constraint), or Fastmail's JMAP HTTPS API (see jmap.go) for a
// host whose network blocks outbound SMTP entirely — see Config.Provider.
//
// Plain text, not HTML: scouting emails (a reset link, "you're signed up
// for Saturday's campout") don't need styling, and skipping HTML/MIME
// multipart entirely avoids an entire class of rendering bugs email clients
// are famous for. (internal/newsletter is the one exception — see SendHTML.)
package mailer

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/settings"
)

// ProviderFastmailJMAP selects Config.Provider's JMAP transport (see
// jmap.go) instead of the default SMTP one.
const ProviderFastmailJMAP = "fastmail-jmap"

// Config configures the outbound mail transport. An empty Host (with
// Provider left at its default) means email is disabled — see Enabled.
type Config struct {
	Host     string
	Port     string // e.g. "587" (STARTTLS, the common case) or "465" (implicit TLS)
	Username string
	Password string
	From     string // e.g. "Troop 47 <noreply@47-yonkers.org>" — used as both the envelope and header From
	TLSMode  string // "starttls" (default) or "tls" (implicit TLS, typically port 465)

	// Provider selects the transport: "" (default) uses Host/Port/
	// Username/Password/TLSMode above over SMTP; ProviderFastmailJMAP
	// instead sends over Fastmail's JMAP HTTPS API using APIToken, and
	// ignores every SMTP-specific field except From (which still picks
	// the sending identity). Exists for a host whose network blocks
	// outbound SMTP entirely — JMAP rides over ordinary HTTPS instead.
	Provider string
	APIToken string // Fastmail API token — only used when Provider == ProviderFastmailJMAP

	// BulkPerMinute caps how fast the two bulk senders (newsletters and
	// prospect campaigns) send. 0 uses defaultBulkPerMinute; negative
	// turns pacing off. Transactional mail is never paced — see pace.go.
	BulkPerMinute int
}

// Enabled reports whether enough configuration is present to attempt
// sending. Callers (password reset, reminders) should check this and
// degrade gracefully — a scouting site without a configured mail provider
// shouldn't be unable to log in or view its calendar.
func (c Config) Enabled() bool {
	if c.Provider == ProviderFastmailJMAP {
		return strings.TrimSpace(c.APIToken) != "" && strings.TrimSpace(c.From) != ""
	}
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

	// jmapRoutes caches the JMAP session/identity/mailbox lookup so a
	// batch send resolves it once rather than once per recipient. Unused
	// on the SMTP path. See jmap.go.
	jmapRoutes jmapRouteCache
}

// New builds a Mailer from its environment-sourced fallback Config and a
// database pool for resolving DB-editable overrides. pool may be nil (e.g.
// in a test that only needs the env-var Config) — effective then simply
// returns cfg unchanged.
func New(cfg Config, pool *pgxpool.Pool) *Mailer {
	if cfg.BulkPerMinute == 0 {
		cfg.BulkPerMinute = defaultBulkPerMinute
	}
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

// dialTimeout bounds the initial TCP connect; overallTimeout bounds
// everything after that — the SMTP handshake, STARTTLS, AUTH, and the
// MAIL/RCPT/DATA/QUIT exchange — via a deadline set directly on the
// connection (see deliver). net/smtp has no context support of its own,
// so without that deadline a server that accepts the TCP connection but
// then never responds (a slow/hung server, or a firewall that silently
// drops packets after the handshake) would block the caller forever, not
// just for dialTimeout. Both are vars rather than consts so a test can
// shrink them instead of waiting out the real duration.
var (
	dialTimeout    = 15 * time.Second
	overallTimeout = 30 * time.Second
)

// Send delivers a single plain-text email. Bounded by a connection timeout
// so a misconfigured or unreachable mail server can't hang the caller
// indefinitely — that matters here since this can be called synchronously
// from an HTTP handler (forgot-password) as well as from the batch
// reminders job.
func (m *Mailer) Send(ctx context.Context, to, subject, body string) error {
	return m.deliver(ctx, to, subject, body, "text/plain")
}

// SendHTML delivers a single HTML email — used only by internal/newsletter,
// where a leader has authored real formatted content via a WYSIWYG editor
// (see internal/newsletter.Sanitize, which the caller must have already run
// on body). Every other send in this codebase (password reset, event
// reminders) stays plain-text via Send; this doesn't change that.
func (m *Mailer) SendHTML(ctx context.Context, to, subject, body string) error {
	return m.deliver(ctx, to, subject, body, "text/html")
}

// deliver validates the shared preconditions (configured, valid addresses)
// then routes to whichever transport Config.Provider selects — deliverSMTP
// below, or sendViaFastmailJMAP (jmap.go).
func (m *Mailer) deliver(ctx context.Context, to, subject, body, contentType string) error {
	cfg := m.effective(ctx)
	if !cfg.Enabled() {
		return fmt.Errorf("mailer: not configured — set SMTP_HOST (and SMTP_PORT/SMTP_USERNAME/SMTP_PASSWORD/SMTP_FROM) or MAIL_PROVIDER=fastmail-jmap (and FASTMAIL_API_TOKEN/SMTP_FROM), or fill in the host/port/username/from fields on /admin/settings, to enable email")
	}
	if _, err := extractAddr(cfg.From); err != nil {
		return fmt.Errorf("mailer: invalid From address %q: %w", cfg.From, err)
	}
	if _, err := mail.ParseAddress(to); err != nil {
		return fmt.Errorf("mailer: invalid recipient address %q: %w", to, err)
	}

	if cfg.Provider == ProviderFastmailJMAP {
		return m.sendViaFastmailJMAP(ctx, cfg, to, subject, body, contentType)
	}
	return deliverSMTP(ctx, cfg, to, subject, body, contentType)
}

// deliverSMTP is the SMTP dial/handshake/transmit path — deliver's
// default, used whenever Config.Provider isn't ProviderFastmailJMAP.
func deliverSMTP(ctx context.Context, cfg Config, to, subject, body, contentType string) error {
	fromAddr, err := extractAddr(cfg.From)
	if err != nil {
		return fmt.Errorf("mailer: invalid SMTP From address %q: %w", cfg.From, err)
	}

	addr := net.JoinHostPort(cfg.Host, cfg.Port)
	dialer := &net.Dialer{Timeout: dialTimeout}

	rawConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("mailer: connecting to %s: %w", addr, err)
	}
	defer rawConn.Close() //nolint:errcheck // best-effort; the real error (if any) already surfaced above

	// Bounds the entire conversation from here on — see overallTimeout's
	// comment above. Set on the raw connection (not the TLS-wrapped one
	// below) since a deadline applies to the underlying socket either way,
	// regardless of which layer is doing the reading/writing through it.
	if err := rawConn.SetDeadline(time.Now().Add(overallTimeout)); err != nil {
		return fmt.Errorf("mailer: setting connection deadline: %w", err)
	}

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
	if _, err := w.Write([]byte(buildMessage(contentType, cfg.From, to, subject, body))); err != nil {
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
// a UTF-8 body of the given contentType ("text/plain" or "text/html"), CRLF
// line endings throughout (matching the canonical net/smtp.SendMail usage
// pattern from the standard library's own documentation). Note: this
// deliberately does NOT hand-roll SMTP dot-stuffing (escaping a body line
// that's just ".") — client.Data() returns a textproto DotWriter, which
// already does that transparently for whatever well-formed, CRLF-terminated
// message it's given. Doing it here too would double-escape.
//
// The body is quoted-printable, not the "8bit" this used to declare, and
// the reason is line length: SMTP caps a line at 1000 octets including
// the CRLF (RFC 5321 §4.5.3.1.6), and an HTML email that embeds its logo
// as a data: URI carries that whole base64 blob on ONE line — 17 KB of it
// in the template that surfaced this. A server is entitled to reject or
// fold a line that long, and folding inside base64 corrupts the image.
// Quoted-printable soft-wraps at 76 characters, so no line can run over,
// and it is 7-bit clean besides — where "8bit" needed the server to
// advertise 8BITMIME before a body with an emoji in it was even legal.
func buildMessage(contentType, from, to, subject, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: %s; charset=\"utf-8\"\r\n", contentType)
	b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n")
	b.WriteString("\r\n")
	b.WriteString(quotedPrintable(body))
	return b.String()
}

// quotedPrintable encodes a body for transport, normalising line endings
// to CRLF on the way through — the encoder emits whatever it is given, so
// a body with bare LFs (every template stored in the database) would
// otherwise produce a message with mixed endings.
func quotedPrintable(body string) string {
	var out strings.Builder
	w := quotedprintable.NewWriter(&out)
	for i, line := range strings.Split(body, "\n") {
		if i > 0 {
			io.WriteString(w, "\r\n")
		}
		io.WriteString(w, strings.TrimSuffix(line, "\r"))
	}
	// Writes to a strings.Builder cannot fail, so the only error Close
	// could report is one from those writes.
	w.Close()
	s := out.String()
	if !strings.HasSuffix(s, "\r\n") {
		s += "\r\n"
	}
	return s
}
