package mailer

import (
	"strings"
	"testing"
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
