package web

import "testing"

// ?next= is followed the instant a password is accepted, so an off-site
// redirect here is a phishing hand-off, not a cosmetic bug: sign in on the
// real site, get bounced to a copy that asks you to confirm your password.
func TestSanitizeNextPathRefusesOffSiteDestinations(t *testing.T) {
	hostile := []struct{ in, why string }{
		{`//evil.com`, "protocol-relative URL"},
		{`///evil.com`, "three slashes still resolves off-site"},
		{`https://evil.com`, "absolute URL"},
		{`http://evil.com`, "absolute URL"},
		// Browsers normalise a backslash to a forward slash while
		// resolving, so these reach another origin despite starting "/".
		{`/\evil.com`, "backslash normalised to slash"},
		{`/\/evil.com`, "mixed slash and backslash"},
		{`/\\evil.com`, "double backslash"},
		{`\\evil.com`, "leading backslashes"},
		{"javascript:alert(1)", "scheme that is not http"},
		{"/path\nSet-Cookie: x=y", "control character"},
		{"/path\r\nLocation: //evil.com", "CRLF injection"},
		{"/path\tmore", "tab"},
	}
	for _, c := range hostile {
		if got := sanitizeNextPath(c.in); got != "/" {
			t.Errorf("sanitizeNextPath(%q) = %q, want \"/\" — %s lets an attacker "+
				"redirect a just-authenticated user off-site", c.in, got, c.why)
		}
	}
}

// The fix must not break the ordinary case, which is the whole reason
// ?next= exists: land back where you were headed.
func TestSanitizeNextPathKeepsRealDestinations(t *testing.T) {
	fine := []string{
		"/roster",
		"/calendar",
		"/settings/calendar",
		"/groups/6f1c2f7e-0000-0000-0000-000000000000",
		"/calendar?month=2026-10",
		"/news#latest",
		"/admin/roster/members/abc?add_member_error=already+has+a+login",
	}
	for _, in := range fine {
		if got := sanitizeNextPath(in); got != in {
			t.Errorf("sanitizeNextPath(%q) = %q — a legitimate destination was discarded", in, got)
		}
	}
	if got := sanitizeNextPath(""); got != "/" {
		t.Errorf("empty next should fall back to \"/\", got %q", got)
	}
}
