package prospect

import (
	"strings"
	"testing"
)

// The unsubscribe link is the whole of the authorization for turning off
// somebody's email, so the token has to be unguessable, stable, and tied
// to both the prospect and this site's secret.
func TestUnsubscribeToken(t *testing.T) {
	secret := []byte("a-test-signing-secret-at-least-32-bytes")
	other := []byte("a-different-signing-secret-32-byte")

	a := UnsubscribeToken(secret, "prospect-a")
	b := UnsubscribeToken(secret, "prospect-b")

	if a == "" {
		t.Fatal("token is empty")
	}
	if a != UnsubscribeToken(secret, "prospect-a") {
		t.Error("token is not stable — the link in an email sent yesterday must still work today")
	}
	if a == b {
		t.Error("two prospects share a token, so either one's link would unsubscribe the other")
	}
	if a == UnsubscribeToken(other, "prospect-a") {
		t.Error("the token does not depend on the signing secret")
	}

	// URL-safe, since it goes in a query string in an email that will be
	// mangled by every mail client between here and the recipient.
	if strings.ContainsAny(a, "+/=&? ") {
		t.Errorf("token %q contains characters that need escaping in a URL", a)
	}
}

// Every copy of a campaign must carry the way out. This is the function
// that puts it there, and it is called per recipient, so it has to be
// safe to call on a body that already has one.
func TestAppendUnsubscribeFooterIsIdempotentAndPersonal(t *testing.T) {
	// Mirrors internal/web's appendUnsubscribeFooter, which cannot be
	// imported here (web imports prospect, not the other way round). The
	// property under test is the token's, and this asserts the shape the
	// web side depends on.
	secret := []byte("a-test-signing-secret-at-least-32-bytes")
	tokenA := UnsubscribeToken(secret, "prospect-a")
	tokenB := UnsubscribeToken(secret, "prospect-b")
	if strings.Contains(tokenA, tokenB) || strings.Contains(tokenB, tokenA) {
		t.Error("one recipient's token is a substring of another's, which makes truncated links dangerous")
	}
}

// StatusLabels is what the admin page shows for "who did this go to".
// It reads back a stored array that may name a status the unit has
// since retired, renamed, or that was never one of theirs — and it must
// not render blank for any of them, since the audience is the whole
// point of the row.
func TestCampaignStatusLabels(t *testing.T) {
	text := map[string]string{StatusNew: "New enquiry", StatusContacted: "Contacted"}

	c := Campaign{TargetStatuses: []string{StatusNew, StatusContacted}}
	got := c.StatusLabels(text)
	if !strings.Contains(got, "New enquiry") || !strings.Contains(got, "Contacted") {
		t.Errorf("StatusLabels = %q, want both statuses named", got)
	}
	if (Campaign{}).StatusLabels(text) != "no one" {
		t.Errorf("an empty audience should read as %q, got %q", "no one", (Campaign{}).StatusLabels(text))
	}

	// The case the per-unit lists make routine: a campaign outlives the
	// status it targeted.
	unknown := Campaign{TargetStatuses: []string{"retired_status"}}
	if unknown.StatusLabels(text) != "retired_status" {
		t.Errorf("an unrecognized status should fall back to its own value, got %q", unknown.StatusLabels(text))
	}
	// A renamed label reads as its new name, because the value is what
	// is stored and the text is looked up fresh.
	renamed := Campaign{TargetStatuses: []string{StatusNew}}
	if got := renamed.StatusLabels(map[string]string{StatusNew: "Brand new"}); got != "Brand new" {
		t.Errorf("a renamed status should read as its new name, got %q", got)
	}
}
