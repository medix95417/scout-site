// Package mailertest is a stand-in SMTP server for tests: it accepts mail on
// a local port and keeps what it was given, so a test can say who a message
// actually reached rather than only that the code asked for it.
//
// It speaks just enough SMTP for internal/mailer — no STARTTLS and no AUTH
// advertised, so the mailer sends in the clear and does not authenticate —
// and nothing a real server would be trusted for. Not for production use;
// it exists only to be imported from _test.go files.
package mailertest

import (
	"bufio"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/47-yonkers/scout-site/internal/mailer"
)

// Message is one message the sink accepted.
type Message struct {
	From string
	To   []string
	// Raw is everything the client sent between DATA and the closing dot,
	// headers included.
	Raw string
}

// Subject is the decoded Subject header.
func (m Message) Subject() string {
	parsed, err := mail.ReadMessage(strings.NewReader(m.Raw))
	if err != nil {
		return ""
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	if err != nil {
		return parsed.Header.Get("Subject")
	}
	return subject
}

// Body is the message body with its quoted-printable encoding undone — the
// text a mail client would show.
func (m Message) Body() string {
	parsed, err := mail.ReadMessage(strings.NewReader(m.Raw))
	if err != nil {
		return ""
	}
	var r io.Reader = parsed.Body
	if strings.EqualFold(parsed.Header.Get("Content-Transfer-Encoding"), "quoted-printable") {
		r = quotedprintable.NewReader(parsed.Body)
	}
	b, _ := io.ReadAll(r)
	return string(b)
}

// Sink is a running stand-in SMTP server.
type Sink struct {
	ln net.Listener

	mu       sync.Mutex
	messages []Message
}

// New starts a sink on a free local port and stops it when the test ends.
func New(t testing.TB) *Sink {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mailertest: listening: %v", err)
	}
	s := &Sink{ln: ln}
	go s.accept()
	t.Cleanup(func() { ln.Close() })
	return s
}

// Config is a mailer configuration that sends to this sink. Pacing is off:
// a test should not wait out the rate a real provider would impose.
func (s *Sink) Config() mailer.Config {
	host, port, _ := net.SplitHostPort(s.ln.Addr().String())
	return mailer.Config{
		Host:          host,
		Port:          port,
		From:          "Test Unit <noreply@test.invalid>",
		BulkPerMinute: -1,
	}
}

// Messages is everything accepted so far, in the order it arrived.
func (s *Sink) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.messages...)
}

// Recipients is every distinct address mail was addressed to, sorted.
func (s *Sink) Recipients() []string {
	seen := map[string]bool{}
	for _, m := range s.Messages() {
		for _, to := range m.To {
			seen[strings.ToLower(to)] = true
		}
	}
	out := make([]string, 0, len(seen))
	for to := range seen {
		out = append(out, to)
	}
	sort.Strings(out)
	return out
}

// WaitFor blocks until n messages have arrived or the timeout passes, and
// reports whether they did. Sending is done in the background, so a test that
// starts one has to wait for it rather than look straight away.
func (s *Sink) WaitFor(n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(s.Messages()) >= n {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return len(s.Messages()) >= n
}

func (s *Sink) accept() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.serve(conn)
	}
}

func (s *Sink) serve(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second)) //nolint:errcheck
	r := bufio.NewReader(conn)
	write := func(line string) { io.WriteString(conn, line+"\r\n") } //nolint:errcheck

	write("220 mailertest ready")
	var cur Message
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimSpace(line)
		upper := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			write("250 mailertest")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			cur = Message{From: address(cmd[len("MAIL FROM:"):])}
			write("250 OK")
		case strings.HasPrefix(upper, "RCPT TO:"):
			cur.To = append(cur.To, address(cmd[len("RCPT TO:"):]))
			write("250 OK")
		case upper == "DATA":
			write("354 end with <CRLF>.<CRLF>")
			var raw strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(l, "\r\n") == "." {
					break
				}
				// Undo dot-stuffing.
				l = strings.TrimPrefix(l, ".")
				raw.WriteString(l)
			}
			cur.Raw = raw.String()
			s.mu.Lock()
			s.messages = append(s.messages, cur)
			s.mu.Unlock()
			cur = Message{}
			write("250 OK queued")
		case upper == "QUIT":
			write("221 bye")
			return
		default: // RSET, NOOP and anything else harmless
			write("250 OK")
		}
	}
}

// address pulls the bare address out of "<a@b>" (with or without brackets
// and any trailing parameters).
func address(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "<"); i >= 0 {
		if j := strings.Index(s[i:], ">"); j > 0 {
			return s[i+1 : i+j]
		}
	}
	if i := strings.Index(s, " "); i >= 0 {
		s = s[:i]
	}
	return s
}
