package mailer

import (
	"strings"
	"testing"
)

// A Fastmail account that owns a domain lists it as a "*@domain"
// catch-all identity, and can genuinely send as any address on it. The
// lookup accepts that rather than demanding a named identity per
// address — but only for the exact domain.
func TestWildcardIdentityCovers(t *testing.T) {
	cases := []struct {
		name, identity, from string
		want                 bool
	}{
		{"the catch-all covers its own domain", "*@example.com", "pack47@example.com", true},
		{"case doesn't matter", "*@Example.COM", "Pack47@example.com", true},
		{"a subdomain is a different domain", "*@example.com", "pack47@mail.example.com", false},
		{"the parent domain is too", "*@mail.example.com", "pack47@example.com", false},
		{"another domain entirely", "*@example.com", "pack47@example.org", false},

		// Only the "*@" shape is a wildcard. An ordinary identity is
		// matched exactly by the caller, never by this.
		{"an ordinary identity isn't a wildcard", "admin@example.com", "pack47@example.com", false},
		{"a partial wildcard isn't one either", "pack*@example.com", "pack47@example.com", false},
		{"a bare star matches nothing", "*", "pack47@example.com", false},
		{"a star with no domain matches nothing", "*@", "pack47@example.com", false},

		// A From with no domain can't be covered by anything.
		{"no at-sign in the address", "*@example.com", "pack47", false},
		{"empty address", "*@example.com", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := wildcardIdentityCovers(c.identity, c.from); got != c.want {
				t.Errorf("wildcardIdentityCovers(%q, %q) = %v, want %v", c.identity, c.from, got, c.want)
			}
		})
	}
}

// The end-to-end half of the wildcard rule, through a fake Fastmail:
// the address the site sends as need not be listed by name when the
// account holds the whole domain.
func TestSendViaFastmailJMAP_AcceptsAWildcardIdentity(t *testing.T) {
	srv, lastBody := jmapTestServer(t, "someone@elsewhere.example", "*@example.com")
	defer srv.Close()

	origSession := fastmailSessionURL
	fastmailSessionURL = srv.URL + "/session"
	defer func() { fastmailSessionURL = origSession }()

	cfg := Config{
		Provider: ProviderFastmailJMAP,
		APIToken: "test-token",
		From:     "Pack 47 <pack47@example.com>",
	}
	if err := New(cfg, nil).sendViaFastmailJMAP(t.Context(), cfg, "family@example.com", "Subject", "Hello", "text/plain"); err != nil {
		t.Fatalf("a catch-all identity should have covered this address: %v", err)
	}
	if body := string(*lastBody); !strings.Contains(body, `"ident2"`) {
		t.Errorf("the submission didn't use the wildcard identity: %s", body)
	}
}

// A named identity is the one to use when there is one, even where a
// wildcard would also have matched — the wildcard is the fallback, not
// a shortcut past the account's own configuration.
func TestSendViaFastmailJMAP_PrefersTheExactIdentity(t *testing.T) {
	// The wildcard is listed first, so picking it would be the easy
	// mistake: the loop must look at the whole list before deciding.
	srv, lastBody := jmapTestServer(t, "*@example.com", "pack47@example.com")
	defer srv.Close()

	origSession := fastmailSessionURL
	fastmailSessionURL = srv.URL + "/session"
	defer func() { fastmailSessionURL = origSession }()

	cfg := Config{
		Provider: ProviderFastmailJMAP,
		APIToken: "test-token",
		From:     "pack47@example.com",
	}
	if err := New(cfg, nil).sendViaFastmailJMAP(t.Context(), cfg, "family@example.com", "Subject", "Hello", "text/plain"); err != nil {
		t.Fatalf("sending as a named identity failed: %v", err)
	}
	if body := string(*lastBody); !strings.Contains(body, `"ident2"`) {
		t.Errorf("the wildcard was used instead of the address's own identity: %s", body)
	}
}

// A domain the account doesn't hold is still refused — the wildcard
// fallback widens what works, it doesn't remove the check.
func TestSendViaFastmailJMAP_WildcardDoesNotCoverAnotherDomain(t *testing.T) {
	srv, _ := jmapTestServer(t, "*@example.com")
	defer srv.Close()

	origSession := fastmailSessionURL
	fastmailSessionURL = srv.URL + "/session"
	defer func() { fastmailSessionURL = origSession }()

	cfg := Config{
		Provider: ProviderFastmailJMAP,
		APIToken: "test-token",
		From:     "pack47@somewhere-else.example",
	}
	err := New(cfg, nil).sendViaFastmailJMAP(t.Context(), cfg, "family@example.com", "Subject", "Hello", "text/plain")
	if err == nil || !strings.Contains(err.Error(), "no Fastmail identity matches") {
		t.Errorf("expected an identity-mismatch error, got %v", err)
	}
}
