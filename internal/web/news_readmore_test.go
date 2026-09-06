package web

import (
	"strings"
	"testing"
)

// A news card that shows the first 220 characters of a longer post, with
// nothing to say so, reads as the whole announcement. The reader has no
// reason to click, so they never see the rest — which is how it was
// reported. The cue has to appear when there IS more, and must not
// appear when there isn't, or it sends people to a page showing the same
// sentence they just read.

func TestExcerptReportsWhetherItCut(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		maxLen   int
		wantCut  bool
		wantText string
	}{
		{"shorter than the limit", "Pack meeting Tuesday.", 220, false, "Pack meeting Tuesday."},
		{"exactly the limit", strings.Repeat("a", 20), 20, false, strings.Repeat("a", 20)},
		{"one over the limit", strings.Repeat("a", 21), 20, true, strings.Repeat("a", 20) + "…"},
		{"empty", "", 220, false, ""},
		// Whitespace is collapsed first, so a post that is only long
		// because of blank lines is not falsely marked as truncated.
		{"long only because of blank lines", "Short.\n\n\n\n\n\n\n\n\n\n\n\n   \n\n  Done.", 30, false, "Short. Done."},
		// The trailing character is not the signal: an author may end a
		// short note with an ellipsis of their own.
		{"author's own ellipsis", "More to follow…", 220, false, "More to follow…"},
	}
	for _, c := range cases {
		got, cut := excerpt(c.body, c.maxLen)
		if cut != c.wantCut {
			t.Errorf("%s: truncated = %v, want %v (got %q)", c.name, cut, c.wantCut, got)
		}
		if got != c.wantText {
			t.Errorf("%s: excerpt = %q, want %q", c.name, got, c.wantText)
		}
	}
}

func newsPage(items []publicPostView) any {
	return struct {
		baseData
		Items []publicPostView
	}{testBase("News"), items}
}

func TestNewsInvitesTheReaderInWhenThereIsMore(t *testing.T) {
	long, cut := excerpt(strings.Repeat("Camp news. ", 60), 220)
	if !cut {
		t.Fatal("test setup: the long body was not truncated")
	}
	out := renderPage(t, "news.html", newsPage([]publicPostView{
		{ID: "p1", Title: "Summer Camp", PostedOn: "6 Sep 2026", Excerpt: long, Truncated: true},
	}))

	if !strings.Contains(out, "Read more") {
		t.Errorf("a truncated post gives the reader no reason to click:\n%s", out)
	}
	// And the card is a link, so the cue points somewhere.
	if !strings.Contains(out, `href="/news/p1"`) {
		t.Error("the card does not link to the post")
	}
}

// TestNewsDoesNotPromiseMoreWhenThereIsNone is the other half, and the
// one that a naive implementation gets wrong.
func TestNewsDoesNotPromiseMoreWhenThereIsNone(t *testing.T) {
	short, cut := excerpt("Pack meeting moved to Wednesday.", 220)
	if cut {
		t.Fatal("test setup: the short body was truncated")
	}
	out := renderPage(t, "news.html", newsPage([]publicPostView{
		{ID: "p1", Title: "Meeting", PostedOn: "6 Sep 2026", Excerpt: short, Truncated: false},
	}))

	if strings.Contains(out, "Read more") {
		t.Errorf("a post shown in full still says there is more:\n%s", out)
	}
	if !strings.Contains(out, "Pack meeting moved to Wednesday.") {
		t.Error("the announcement itself is missing")
	}
}

// TestOnlyTheTruncatedCardsAreMarked — the flag is per post, so a mixed
// list must not tar them all with one brush.
func TestOnlyTheTruncatedCardsAreMarked(t *testing.T) {
	out := renderPage(t, "news.html", newsPage([]publicPostView{
		{ID: "p1", Title: "Long one", PostedOn: "6 Sep", Excerpt: "starts here…", Truncated: true},
		{ID: "p2", Title: "Short one", PostedOn: "5 Sep", Excerpt: "all of it", Truncated: false},
	}))
	if got := strings.Count(out, "Read more"); got != 1 {
		t.Errorf(`"Read more" appears %d times, want 1 — only the truncated card`, got)
	}
}
