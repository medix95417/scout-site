package web

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Nothing is emailed to a family a leader adds by hand.
//
// This is the promise the feature was asked for with, and it is the
// kind that erodes quietly: the send calls sit two functions away in
// the same package, a later change copies the shape of JoinSubmit
// because that is the other way a prospect gets created, and the first
// anyone knows is a list of people who never contacted the unit
// receiving "thanks for your enquiry".
//
// A source check rather than a behavioural one because the behaviour
// being asserted is an absence, and the only honest way to test an
// absence is to look at whether the call is there.
func TestAddingProspectsByHandSendsNothing(t *testing.T) {
	src, err := os.ReadFile("prospect_add.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	for _, forbidden := range []string{
		"sendProspectAutoReply", // the family's automatic reply
		"notifyProspect",        // the leaders' notification
		"h.Mailer",              // anything else reaching for the mailer
		"SendHTML",
		".Send(",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("prospect_add.go calls %s — adding a family by hand must not email anybody", forbidden)
		}
	}
}

// And the form's own path still does send, so the guard above is
// measuring a deliberate difference rather than a feature nobody
// wired up.
func TestTheJoinFormStillSends(t *testing.T) {
	src, err := os.ReadFile("prospects.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, want := range []string{"h.notifyProspect(", "h.sendProspectAutoReply("} {
		if !strings.Contains(body, want) {
			t.Errorf("the public join form no longer calls %s", want)
		}
	}
}

// The paste preview must not store anything: it exists so a leader can
// look at somebody's spreadsheet before it becomes rows.
func TestImportPreviewDoesNotSave(t *testing.T) {
	src, err := os.ReadFile("prospect_add.go")
	if err != nil {
		t.Fatal(err)
	}
	preview := htmlBetween(t, string(src),
		"func (h *Handlers) ProspectImportPreview(", "\n}\n")
	for _, forbidden := range []string{"SavePaste", "AddByLeader", "prospect.Create"} {
		if strings.Contains(preview, forbidden) {
			t.Errorf("the preview calls %s — it is supposed to store nothing", forbidden)
		}
	}
	if !strings.Contains(preview, "ParsePaste") {
		t.Error("the preview doesn't parse anything")
	}
}

// The confirmation step re-reads the pasted text rather than trusting
// a list of rows posted back to it. A browser could have edited those,
// and re-parsing also means the duplicate check runs against the
// database as it is at save time, not as it was when the preview was
// drawn.
func TestImportSaveReparses(t *testing.T) {
	src, err := os.ReadFile("prospect_add.go")
	if err != nil {
		t.Fatal(err)
	}
	save := htmlBetween(t, string(src),
		"func (h *Handlers) ProspectImportSave(", "\n}\n")
	if !strings.Contains(save, "ParsePaste") {
		t.Error("the save doesn't re-parse — it is trusting rows that came back from a browser")
	}
}

// Both new routes are behind the same gate as the rest of the page.
func TestAddingProspectsNeedsContentEditor(t *testing.T) {
	src, err := os.ReadFile("prospect_add.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	handlers := regexp.MustCompile(`func \(h \*Handlers\) (ProspectAdd|ProspectImportPreview|ProspectImportSave)\(`).
		FindAllStringSubmatch(body, -1)
	if len(handlers) != 3 {
		t.Fatalf("found %d of the three handlers", len(handlers))
	}
	if got := strings.Count(body, "h.requireContentEditor("); got != 3 {
		t.Errorf("%d of the three handlers check requireContentEditor", got)
	}
}
