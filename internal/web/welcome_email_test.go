package web

import (
	"net/http/httptest"
	"testing"
)

// TestCredentialsTemplate_RendersEveryDataShape guards admin-roster-
// credentials.html against the same class of bug fixed for
// forgot-password.html (see TestForgotPasswordTemplate_RendersEveryDataShape):
// a template evaluating a field the handler's data struct doesn't set.
// Uses credentialsData itself, the real type renderCredentials builds.
func TestCredentialsTemplate_RendersEveryDataShape(t *testing.T) {
	h, err := New(nil, "", false, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cases := []struct {
		name string
		data credentialsData
	}{
		{"reset, no welcome email", credentialsData{baseData: baseData{PageTitle: "Password reset"}, Heading: "Password reset", Email: "a@example.com", TempPassword: "abc123"}},
		{"created, welcome email sent", credentialsData{baseData: baseData{PageTitle: "Family created"}, Heading: "Family created", Email: "a@example.com", TempPassword: "abc123", WelcomeEmailRequested: true, WelcomeEmailSent: true}},
		{"created, welcome email requested but failed", credentialsData{baseData: baseData{PageTitle: "Family created"}, Heading: "Family created", Email: "a@example.com", TempPassword: "abc123", WelcomeEmailRequested: true, WelcomeEmailSent: false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.render(rec, h.rosterCredentials, c.data)
			if rec.Code != 200 {
				t.Errorf("render produced status %d, body: %s", rec.Code, rec.Body.String())
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
