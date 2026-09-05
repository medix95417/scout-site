package mailer

import (
	"bufio"
	"context"
	"io"
	"mime/quotedprintable"
	"net"
	"strings"
	"testing"
	"time"
)

// bigDataURIBody is the shape of email that broke: an inlined logo, so
// the src attribute — and therefore the whole line — is far longer than
// SMTP permits a line to be.
func bigDataURIBody() string {
	return `<html><body><table><tr><td style="background-color:#003F87">` +
		`<img src='data:image/jpeg;base64,` + strings.Repeat("/9j/4AAQSkZJRgABAQAAAQ", 900) + `' alt="Pack 47">` +
		"</td></tr></table>\n<p>Join us on September 18 &mdash; it&rsquo;s going to be fun 🏕️</p>\n</body></html>"
}

// smtpLineLimit is RFC 5321 §4.5.3.1.6: 1000 octets, CRLF included, so
// 998 of content. A server may reject or fold anything longer, and a fold
// inside base64 corrupts the image it encodes.
const smtpLineLimit = 998

// TestBuildMessageWrapsLongLines is the direct guard on the bug: with
// Content-Transfer-Encoding: 8bit the 20 KB data: URI went out as a
// single line and arrived truncated.
func TestBuildMessageWrapsLongLines(t *testing.T) {
	body := bigDataURIBody()
	if longest := longestLine(body); longest <= smtpLineLimit {
		t.Fatalf("test body no longer exercises the bug: longest input line is %d octets", longest)
	}

	msg := buildMessage("text/html", "Pack 47 <noreply@example.com>", "family@example.com", "Open House", body)

	for i, line := range strings.Split(msg, "\r\n") {
		if len(line) > smtpLineLimit {
			t.Fatalf("line %d is %d octets, over the %d-octet SMTP limit: %.80s...", i+1, len(line), smtpLineLimit, line)
		}
	}
	if !strings.Contains(msg, "Content-Transfer-Encoding: quoted-printable") {
		t.Errorf("body was wrapped but the encoding was not declared, so a reader will show the soft breaks:\n%.400s", msg)
	}
}

// TestBuildMessageBodyDecodesBackExactly is the other half: wrapping is
// only correct if the reader can undo it. An image that decodes to
// almost-the-right bytes is as broken as no image at all.
func TestBuildMessageBodyDecodesBackExactly(t *testing.T) {
	body := bigDataURIBody()
	msg := buildMessage("text/html", "Pack 47 <noreply@example.com>", "family@example.com", "Open House", body)

	_, encoded, ok := strings.Cut(msg, "\r\n\r\n")
	if !ok {
		t.Fatal("message has no header/body separator")
	}
	got, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(encoded)))
	if err != nil {
		t.Fatalf("decoding the body: %v", err)
	}

	want := strings.ReplaceAll(body, "\n", "\r\n") + "\r\n"
	if string(got) != want {
		t.Errorf("body did not survive the round trip (in %d bytes, out %d)\n%s", len(want), len(got), firstDiff(want, string(got)))
	}
}

// TestDeliverSMTPSendsTheWholeBody runs the real delivery path against a
// real (if minimal) SMTP server, so it covers what the unit tests above
// cannot: the textproto DotWriter, the CRLF handling, and the possibility
// of the body being cut off somewhere between buildMessage and the wire.
func TestDeliverSMTPSendsTheWholeBody(t *testing.T) {
	received := make(chan string, 1)
	addr := startSMTPSink(t, received)

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Host: host, Port: port, From: "Pack 47 <noreply@example.com>", TLSMode: "none"}

	body := bigDataURIBody()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := deliverSMTP(ctx, cfg, "family@example.com", "Open House", body, "text/html"); err != nil {
		t.Fatalf("deliverSMTP: %v", err)
	}

	var raw string
	select {
	case raw = <-received:
	case <-time.After(10 * time.Second):
		t.Fatal("the sink never received a message")
	}

	_, encoded, ok := strings.Cut(raw, "\r\n\r\n")
	if !ok {
		t.Fatalf("delivered message has no header/body separator:\n%.400s", raw)
	}
	got, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(encoded)))
	if err != nil {
		t.Fatalf("decoding the delivered body: %v", err)
	}

	// The whole inlined image, not a prefix of it.
	want := strings.ReplaceAll(body, "\n", "\r\n") + "\r\n"
	if string(got) != want {
		t.Errorf("delivered body differs from what was sent (sent %d bytes, delivered %d)\n%s",
			len(want), len(got), firstDiff(want, string(got)))
	}
	if !strings.Contains(string(got), "🏕️") {
		t.Error("the emoji did not survive delivery")
	}
}

// startSMTPSink runs a bare-minimum SMTP server that accepts one message
// and hands back everything between DATA and the terminating dot. It
// speaks only what net/smtp's client needs, and advertises no extensions
// — in particular no 8BITMIME, which is the point: a body this code
// declares as 8bit is not something a plain server has agreed to carry.
func startSMTPSink(t *testing.T, out chan<- string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() }) //nolint:errcheck // test cleanup

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close() //nolint:errcheck // test cleanup
		conn.SetDeadline(time.Now().Add(20 * time.Second))

		r := bufio.NewReader(conn)
		io.WriteString(conn, "220 sink ESMTP\r\n")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
				io.WriteString(conn, "250 sink\r\n")
			case strings.HasPrefix(line, "DATA"):
				io.WriteString(conn, "354 go ahead\r\n")
				var b strings.Builder
				for {
					l, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if l == ".\r\n" {
						break
					}
					// Undo the dot-stuffing the client applies.
					b.WriteString(strings.TrimPrefix(l, "."))
				}
				out <- b.String()
				io.WriteString(conn, "250 queued\r\n")
			case strings.HasPrefix(line, "QUIT"):
				io.WriteString(conn, "221 bye\r\n")
				return
			default:
				io.WriteString(conn, "250 ok\r\n")
			}
		}
	}()
	return ln.Addr().String()
}

func longestLine(s string) int {
	longest := 0
	for _, line := range strings.Split(s, "\n") {
		if len(line) > longest {
			longest = len(line)
		}
	}
	return longest
}

// firstDiff describes where two bodies stop agreeing, since printing 20 KB
// of base64 twice tells you nothing.
func firstDiff(want, got string) string {
	n := len(want)
	if len(got) < n {
		n = len(got)
	}
	for i := 0; i < n; i++ {
		if want[i] != got[i] {
			lo := max(0, i-40)
			return "first difference at byte " + itoa(i) + ":\n want ..." + want[lo:min(len(want), i+40)] +
				"\n  got ..." + got[lo:min(len(got), i+40)]
		}
	}
	if len(want) != len(got) {
		return "identical for the first " + itoa(n) + " bytes, then one ends"
	}
	return ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}
