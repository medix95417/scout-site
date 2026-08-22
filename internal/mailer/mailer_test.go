package mailer

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestBuildMessage_PlainTextContentType(t *testing.T) {
	msg := buildMessage("text/plain", "Troop 47 <noreply@example.com>", "family@example.com", "Subject", "Hello\nWorld")
	if !strings.Contains(msg, `Content-Type: text/plain; charset="utf-8"`) {
		t.Errorf("expected text/plain Content-Type header, got:\n%s", msg)
	}
}

func TestBuildMessage_HTMLContentType(t *testing.T) {
	msg := buildMessage("text/html", "Troop 47 <noreply@example.com>", "family@example.com", "Subject", "<p>Hello <strong>World</strong></p>")
	if !strings.Contains(msg, `Content-Type: text/html; charset="utf-8"`) {
		t.Errorf("expected text/html Content-Type header, got:\n%s", msg)
	}
	if !strings.Contains(msg, "<strong>World</strong>") {
		t.Errorf("HTML body was not preserved verbatim in the message, got:\n%s", msg)
	}
}

// TestSend_HungServerDoesNotBlockForever guards against exactly the bug
// this codebase once had: dialTimeout only bounded the initial TCP
// connect, so a server that accepted the connection and then never
// responded (no SMTP greeting) — a hung server, or a firewall silently
// dropping packets past the handshake — would block Send indefinitely,
// since net/smtp has no context support of its own. overallTimeout (set
// as a deadline on the raw connection in deliver) is what's supposed to
// prevent that.
func TestSend_HungServerDoesNotBlockForever(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Accept the connection but deliberately never write the SMTP
		// greeting — held open well past the test's shrunk overallTimeout
		// below, so the test fails (via the select's timeout case) if
		// deliver isn't actually enforcing that deadline.
		time.Sleep(2 * time.Second)
	}()

	origDial, origOverall := dialTimeout, overallTimeout
	dialTimeout = 200 * time.Millisecond
	overallTimeout = 200 * time.Millisecond
	defer func() { dialTimeout, overallTimeout = origDial, origOverall }()

	host, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}

	m := New(Config{Host: host, Port: port, From: "Test <test@example.com>"}, nil)

	done := make(chan error, 1)
	go func() {
		done <- m.Send(context.Background(), "family@example.com", "Subject", "Body")
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error from a server that never sends a greeting, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not return within 2s of a hung server — the connection deadline isn't being enforced")
	}
}
