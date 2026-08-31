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
	r := welcomeEmailReplacer("Jamie", "jamie@example.com", "hunter2", "https://example.com/login", "Troop 47", false)
	got := r.Replace("{{name}}/{{email}}/{{password}}/{{login_url}}/{{unit_name}}")
	want := "Jamie/jamie@example.com/hunter2/https://example.com/login/Troop 47"
	if got != want {
		t.Errorf("welcomeEmailReplacer substitution = %q, want %q", got, want)
	}
}

// TestWelcomeEmailReplacer_EscapesValuesForHTML is the rule that makes an
// HTML welcome email safe to send: the leader's template is theirs to
// write markup in, but the values dropped into it are not markup and must
// never be treated as such.
//
// The password is the case that matters most. It's generated, a leader
// reads it off the credentials page, and if a "<" in it silently ate the
// rest of the email nobody would know why the family never got their
// login.
func TestWelcomeEmailReplacer_EscapesValuesForHTML(t *testing.T) {
	r := welcomeEmailReplacer(`Ben & Jo <b>`, "a@b.com", `p<a>ss&"`, "https://example.com/login?a=1&b=2", "Troop & Pack", true)
	got := r.Replace("{{name}}|{{password}}|{{login_url}}|{{unit_name}}")

	for _, raw := range []string{"<b>", `p<a>ss`, "& b=2"} {
		if strings.Contains(got, raw) {
			t.Errorf("substituted value %q reached the HTML body unescaped:\n%s", raw, got)
		}
	}
	if !strings.Contains(got, "Ben &amp; Jo &lt;b&gt;") {
		t.Errorf("expected the name to be escaped, got:\n%s", got)
	}
}

// TestTextToHTML_KeepsPlainTemplatesReadable covers the compatibility
// half: every template written before HTML was allowed has no markup, and
// must not collapse into one paragraph now that the mail is sent as HTML.
func TestTextToHTML_KeepsPlainTemplatesReadable(t *testing.T) {
	got := textToHTML("Hi Jamie,\n\nEmail: a@b.com\nPassword: hunter2\n\nSee you soon.")

	if n := strings.Count(got, "<p>"); n != 3 {
		t.Errorf("expected 3 paragraphs from 3 blank-line-separated blocks, got %d:\n%s", n, got)
	}
	if !strings.Contains(got, "Email: a@b.com<br>") {
		t.Errorf("a single newline inside a paragraph should become a break, got:\n%s", got)
	}
	if strings.Contains(textToHTML("5 < 6 & 7"), "5 < 6") {
		t.Error("plain text must be escaped on its way into HTML")
	}
}

// TestLooksLikeHTML pins the rule that decides which of the two paths a
// stored template takes.
func TestLooksLikeHTML(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{defaultWelcomeEmailBody, false},
		{"Hi {{name}},\n\nWelcome!", false},
		{"<p>Hi {{name}},</p>", true},
		{"<a href=\"{{login_url}}\">Log in</a>", true},
	} {
		if got := looksLikeHTML(tc.body); got != tc.want {
			t.Errorf("looksLikeHTML(%q) = %v, want %v", tc.body, got, tc.want)
		}
	}
}
