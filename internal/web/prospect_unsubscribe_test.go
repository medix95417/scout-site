package web

import (
	"strings"
	"testing"

	"github.com/47-yonkers/scout-site/internal/prospect"
)

var testSecret = []byte("a-test-signing-secret-at-least-32-bytes")

// Every copy of a bulk email to members of the public has to carry a way
// out of it. This is where that happens, and it happens per recipient, so
// the link must be theirs and the footer must survive being applied to a
// body that already has one.
func TestAppendUnsubscribeFooter(t *testing.T) {
	rec := prospect.Recipient{ProspectID: "prospect-a", Name: "Robin", Email: "robin@example.com"}
	body := "<p>Come and visit us.</p>"

	out := appendUnsubscribeFooter(body, "https://pack47.example.org", rec, testSecret)

	if !strings.HasPrefix(out, body) {
		t.Error("the leader's own message should come first, unchanged")
	}
	if !strings.Contains(out, "unsubscribe") {
		t.Error("footer does not offer an unsubscribe")
	}
	want := "https://pack47.example.org/unsubscribe?p=prospect-a&t=" + prospect.UnsubscribeToken(testSecret, "prospect-a")
	if !strings.Contains(out, want) {
		t.Errorf("footer does not contain this recipient's own link\nwant: %s\ngot:  %s", want, out)
	}
	// Says why they are hearing from us. "Unsubscribe" alone doesn't
	// remind anyone why a Scouting unit is emailing them months later.
	if !strings.Contains(out, "asked us about joining") {
		t.Error("footer does not say why the recipient is receiving this")
	}

	// Applied twice — a re-personalized body, a retry — must not stack.
	twice := appendUnsubscribeFooter(out, "https://pack47.example.org", rec, testSecret)
	if strings.Count(twice, unsubscribeFooterMarker) != 1 {
		t.Errorf("footer applied twice, giving %d copies", strings.Count(twice, unsubscribeFooterMarker))
	}
}

// Two recipients must never receive the same link, or unsubscribing would
// take the wrong family off the list.
func TestUnsubscribeFooterIsPerRecipient(t *testing.T) {
	a := appendUnsubscribeFooter("<p>x</p>", "https://x.example", prospect.Recipient{ProspectID: "a"}, testSecret)
	b := appendUnsubscribeFooter("<p>x</p>", "https://x.example", prospect.Recipient{ProspectID: "b"}, testSecret)
	if a == b {
		t.Fatal("two recipients got an identical unsubscribe footer")
	}
}

// The prospect id and token go into a URL inside an HTML attribute. They
// are server-generated today, but this is the boundary where a stray
// quote would break out of the href, so it is escaped rather than
// trusted.
func TestUnsubscribeFooterEscapesItsURL(t *testing.T) {
	rec := prospect.Recipient{ProspectID: `a"onmouseover="alert(1)`}
	out := appendUnsubscribeFooter("<p>x</p>", "https://x.example", rec, testSecret)
	if strings.Contains(out, `onmouseover="alert(1)"`) {
		t.Errorf("an id containing a quote escaped the href attribute:\n%s", out)
	}
	if !strings.Contains(out, "&#34;") {
		t.Error("the quote in the id was not escaped")
	}
}
