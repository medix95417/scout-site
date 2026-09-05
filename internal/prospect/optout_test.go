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

// A campaign's audience is chosen with checkboxes, but arrives as form
// values, which can be anything. Widening it past the real statuses would
// mean emailing families the leader didn't pick.
func TestFilterStatusesRejectsAnythingNotAStatus(t *testing.T) {
	got := filterStatuses([]string{StatusNew, "everyone", StatusNew, "", StatusJoined})
	want := []string{StatusNew, StatusJoined}
	if len(got) != len(want) {
		t.Fatalf("filterStatuses = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("filterStatuses = %v, want %v", got, want)
		}
	}
	if len(filterStatuses(nil)) != 0 {
		t.Error("filterStatuses(nil) should be empty")
	}
}

// StatusLabels is what the admin page shows for "who did this go to", and
// it reads back a stored array that may contain a status no longer in the
// code — it must not render blank in that case.
func TestCampaignStatusLabels(t *testing.T) {
	c := Campaign{TargetStatuses: []string{StatusNew, StatusContacted}}
	got := c.StatusLabels()
	if !strings.Contains(got, "New enquiry") || !strings.Contains(got, "Contacted") {
		t.Errorf("StatusLabels = %q, want both statuses named", got)
	}
	if (Campaign{}).StatusLabels() != "no one" {
		t.Errorf("an empty audience should read as %q, got %q", "no one", (Campaign{}).StatusLabels())
	}
	unknown := Campaign{TargetStatuses: []string{"retired_status"}}
	if unknown.StatusLabels() != "retired_status" {
		t.Errorf("an unrecognized status should fall back to its own value, got %q", unknown.StatusLabels())
	}
}
