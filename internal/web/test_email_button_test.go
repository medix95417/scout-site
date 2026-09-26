package web

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The "send a test email" button exists because the failures that
// matter most are silent: a mailer with nothing configured skips
// quietly, and a wrong From address reports itself only in a log line.
// A leader reading `docker compose logs` is not who this site is for.

func TestSettingsPageOffersATestEmail(t *testing.T) {
	page := readTemplate(t, "admin-settings.html")

	form := htmlBetween(t, page, `<form method="post" action="/admin/settings/test-email"`, "</form>")
	if !strings.Contains(form, `name="csrf_token"`) {
		t.Error("the test-email form has no CSRF token")
	}
	// The result has to be shown, both ways round — a button that says
	// nothing on failure is the problem it was added to solve.
	for _, want := range []string{"{{if .MailTestOK}}", "{{if .MailTestError}}", "{{.MailTestError}}"} {
		if !strings.Contains(page, want) {
			t.Errorf("the settings page never renders %s", want)
		}
	}
}

// The recipient is the sender's own login address and nothing else. A
// free-text "send a test to…" box on a page a super admin can reach
// would be a way to make this site send attacker-chosen mail from the
// unit's own domain, and no useful test needs it.
func TestTestEmailTakesNoRecipientFromTheRequest(t *testing.T) {
	form := htmlBetween(t, readTemplate(t, "admin-settings.html"),
		`<form method="post" action="/admin/settings/test-email"`, "</form>")
	for _, tag := range regexp.MustCompile(`(?s)<input\b[^>]*>`).FindAllString(form, -1) {
		if !strings.Contains(tag, `name="csrf_token"`) {
			t.Errorf("the test-email form carries a field other than the CSRF token: %s", tag)
		}
	}

	src, err := os.ReadFile("settings_admin.go")
	if err != nil {
		t.Fatal(err)
	}
	handler := htmlBetween(t, string(src),
		"func (h *Handlers) SystemSettingsSendTestEmail(", "\n}\n")
	if !strings.Contains(handler, "user.Email") {
		t.Error("the handler doesn't send to the signed-in login's own address")
	}
	for _, forbidden := range []string{"FormValue", "PostForm", "r.Form"} {
		if strings.Contains(handler, forbidden) {
			t.Errorf("the handler reads %s — the recipient must come from the session, not the request", forbidden)
		}
	}
}

// readTemplate returns one template's source.
func readTemplate(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("templates/" + name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(b)
}
