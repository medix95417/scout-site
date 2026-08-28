package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCredentialsTemplate_RendersEveryDataShape guards admin-roster-
// credentials.html against the same class of bug fixed for
// forgot-password.html (see TestForgotPasswordTemplate_RendersEveryDataShape):
// a template evaluating a field the handler's data struct doesn't set.
// Uses credentialsData itself, the real type renderCredentials builds —
// including leaving TempPassword "" whenever WelcomeEmailSent is true,
// exactly as renderCredentials does, since that's the actual shape this
// template needs to handle.
//
// Also asserts the plaintext password shows up in the rendered HTML if
// and only if it wasn't already emailed — the property this whole change
// exists for: once a leader has confirmation the family got it by mail,
// the password itself should never reach this page's HTML at all, not
// just be hidden behind CSS.
func TestCredentialsTemplate_RendersEveryDataShape(t *testing.T) {
	h, err := New(nil, "", false, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const password = "correct-horse-battery-staple"
	cases := []struct {
		name          string
		data          credentialsData
		passwordShown bool
	}{
		{"reset, no welcome email", credentialsData{baseData: baseData{PageTitle: "Password reset"}, Heading: "Password reset", Email: "a@example.com", TempPassword: password}, true},
		{"created, welcome email sent", credentialsData{baseData: baseData{PageTitle: "Family created"}, Heading: "Family created", Email: "a@example.com", WelcomeEmailRequested: true, WelcomeEmailSent: true}, false},
		{"created, welcome email requested but failed", credentialsData{baseData: baseData{PageTitle: "Family created"}, Heading: "Family created", Email: "a@example.com", TempPassword: password, WelcomeEmailRequested: true, WelcomeEmailSent: false}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.render(rec, h.rosterCredentials, c.data)
			if rec.Code != 200 {
				t.Errorf("render produced status %d, body: %s", rec.Code, rec.Body.String())
			}
			shown := strings.Contains(rec.Body.String(), password)
			if shown != c.passwordShown {
				t.Errorf("password shown in HTML = %v, want %v", shown, c.passwordShown)
			}
		})
	}
}

// TestWelcomeEmailReplacer verifies every documented placeholder
// ({{name}}, {{email}}, {{password}}, {{login_url}}, {{unit_name}}) is
// actually substituted — these are typed into the settings page as plain
// text by a leader, so a missed placeholder would silently leak into a
// real family's inbox.
func TestWelcomeEmailReplacer(t *testing.T) {
	r := welcomeEmailReplacer("Jamie", "jamie@example.com", "hunter2", "https://example.com/login", "Troop 47")
	got := r.Replace("{{name}}/{{email}}/{{password}}/{{login_url}}/{{unit_name}}")
	want := "Jamie/jamie@example.com/hunter2/https://example.com/login/Troop 47"
	if got != want {
		t.Errorf("welcomeEmailReplacer substitution = %q, want %q", got, want)
	}
}
