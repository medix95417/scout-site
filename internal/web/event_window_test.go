package web

import (
	"testing"
	"time"

	"github.com/47-yonkers/scout-site/internal/calendar"
	"github.com/47-yonkers/scout-site/internal/files"
)

func TestClassifyEventWindow(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour

	cases := []struct {
		name     string
		startsAt time.Time
		want     string
	}{
		{"this morning", now.Add(-4 * time.Hour), eventWindowRecent},
		{"yesterday", now.Add(-day), eventWindowRecent},
		{"29 days ago", now.Add(-29 * day), eventWindowRecent},
		{"31 days ago", now.Add(-31 * day), eventWindowOther},
		{"last summer", now.Add(-300 * day), eventWindowOther},
		{"tonight", now.Add(6 * time.Hour), eventWindowUpcoming},
		{"next week", now.Add(7 * day), eventWindowUpcoming},
		{"29 days out", now.Add(29 * day), eventWindowUpcoming},
		{"31 days out", now.Add(31 * day), eventWindowOther},
		{"next year's camporee", now.Add(300 * day), eventWindowOther},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyEventWindow(c.startsAt, now); got != c.want {
				t.Errorf("classifyEventWindow(%s) = %q, want %q", c.name, got, c.want)
			}
		})
	}
}

// The two windows must never both claim the same event: an event in two
// buckets is a checkbox rendered twice, which posts the same event id
// twice when the leader saves.
func TestTheTwoWindowsNeverOverlap(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	for offset := -40 * 24 * time.Hour; offset <= 40*24*time.Hour; offset += time.Hour {
		got := classifyEventWindow(now.Add(offset), now)
		if got != eventWindowRecent && got != eventWindowUpcoming && got != eventWindowOther {
			t.Fatalf("offset %s classified as %q, which is not one of the three buckets", offset, got)
		}
	}
}

func TestPhotosLookBackwardsAndDocumentsLookForwards(t *testing.T) {
	if got := windowForCategory(files.CategoryEventPhoto); got != eventWindowRecent {
		t.Errorf("a photo/video upload should open on recent events, got %q", got)
	}
	if got := windowForCategory(files.CategoryGeneral); got != eventWindowUpcoming {
		t.Errorf("a document upload should open on upcoming events, got %q", got)
	}
	if got := windowForCategory("something-new"); got != eventWindowUpcoming {
		t.Errorf("an unknown category should be treated as a document, got %q", got)
	}
}

func testEvents(now time.Time) []calendar.Event {
	day := 24 * time.Hour
	return []calendar.Event{
		{ID: "last-weekend", Title: "Last weekend's hike", StartsAt: now.Add(-3 * day)},
		{ID: "next-weekend", Title: "Next weekend's campout", StartsAt: now.Add(4 * day)},
		{ID: "old", Title: "Summer camp, last year", StartsAt: now.Add(-300 * day)},
		{ID: "far-off", Title: "Next year's camporee", StartsAt: now.Add(200 * day)},
	}
}

func windowIDs(choices []eventChoice) []string {
	out := make([]string, 0, len(choices))
	for _, c := range choices {
		out = append(out, c.ID)
	}
	return out
}

func sameIDs(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestSplitEventChoicesByCategory(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	choices := eventChoices(testEvents(now), now)

	t.Run("a photo offers the last 30 days", func(t *testing.T) {
		shown, rest := splitEventChoices(choices, files.CategoryEventPhoto, nil)
		if !sameIDs(windowIDs(shown), "last-weekend") {
			t.Errorf("shown = %v, want just last-weekend", windowIDs(shown))
		}
		if !sameIDs(windowIDs(rest), "next-weekend", "old", "far-off") {
			t.Errorf("rest = %v, want everything else", windowIDs(rest))
		}
	})

	t.Run("a document offers the next 30 days", func(t *testing.T) {
		shown, rest := splitEventChoices(choices, files.CategoryGeneral, nil)
		if !sameIDs(windowIDs(shown), "next-weekend") {
			t.Errorf("shown = %v, want just next-weekend", windowIDs(shown))
		}
		if !sameIDs(windowIDs(rest), "last-weekend", "old", "far-off") {
			t.Errorf("rest = %v, want everything else", windowIDs(rest))
		}
	})

	// The link form posts back exactly the boxes it rendered, so an
	// existing link that fell outside the window has to be rendered
	// where the leader can see it.
	t.Run("an event this file is already linked to is always shown", func(t *testing.T) {
		linked := map[string]bool{"old": true}
		shown, rest := splitEventChoices(choices, files.CategoryEventPhoto, linked)
		if !sameIDs(windowIDs(shown), "last-weekend", "old") {
			t.Errorf("shown = %v, want the window plus the existing link", windowIDs(shown))
		}
		for _, c := range rest {
			if c.ID == "old" {
				t.Error("the already-linked event was also offered in the hidden half — it would render twice")
			}
		}
	})

	t.Run("nothing is ever dropped", func(t *testing.T) {
		for _, category := range []string{files.CategoryEventPhoto, files.CategoryGeneral} {
			shown, rest := splitEventChoices(choices, category, nil)
			if len(shown)+len(rest) != len(choices) {
				t.Errorf("category %q: %d shown + %d hidden != %d events", category, len(shown), len(rest), len(choices))
			}
		}
	})
}
