package web

import (
	"strings"
	"testing"
)

// Links in a news post body.
//
// Absolute URLs already became links. These cover the two things added
// alongside them: a path into this site's own file library, and a
// labelled [Words](target) link so a file reads as its name rather than
// as a uuid.
//
// The refusals matter most. renderPostBody is the one place in the
// codebase that hands user-typed content to a template as
// template.HTML, so whatever reaches an href here reaches the page —
// html/template is not a backstop behind it.

func TestFileLinksInAPostBody(t *testing.T) {
	cases := []struct {
		name, in string
		wantHref string // "" means: no link at all
		wantText string
	}{
		{
			"a bare file path becomes a link",
			"The slip is at /files/abc-123/download",
			"/files/abc-123/download", "/files/abc-123/download",
		},
		{
			"a labelled file link reads as its label",
			"[Permission slip](/files/abc-123/download) is due Friday",
			"/files/abc-123/download", "Permission slip",
		},
		{
			"a labelled external link",
			"[Our flyer](https://example.org/f.pdf)",
			"https://example.org/f.pdf", "Our flyer",
		},
		{
			"the thumb and banner variants are links too",
			"/files/abc-123/thumb",
			"/files/abc-123/thumb", "/files/abc-123/thumb",
		},

		// Refusals: left exactly as typed rather than half-rendered.
		{"javascript: is not a link target", "[Tap here](javascript:alert(1))", "", ""},
		{"data: is not a link target", "[Tap here](data:text/html,<script>x</script>)", "", ""},
		{
			// The one that reads like a path and isn't.
			"a protocol-relative URL is not a path",
			"[Tap here](//evil.example.org/x)", "", "",
		},
		{"an arbitrary path is not a link", "We met at /the/car/park", "", ""},
		{"a file path outside this site's shape", "/files/../../etc/passwd", "", ""},
		{"an unknown file action", "/files/abc-123/delete", "", ""},
		{"a relative path with no leading slash", "files/abc-123/download", "", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := string(renderPostBody(c.in))
			if c.wantHref == "" {
				if strings.Contains(got, "<a ") {
					t.Errorf("rendered a link from %q: %s", c.in, got)
				}
				return
			}
			if !strings.Contains(got, `href="`+c.wantHref+`"`) {
				t.Errorf("href missing for %q: %s", c.in, got)
			}
			if !strings.Contains(got, ">"+c.wantText+"</a>") {
				t.Errorf("link text for %q isn't %q: %s", c.in, c.wantText, got)
			}
		})
	}
}

// A refused target must come back as the leader's own text, not
// disappear and not be partly rendered.
func TestARefusedLinkKeepsItsText(t *testing.T) {
	got := string(renderPostBody("[Tap here](javascript:alert(1))"))
	if !strings.Contains(got, "[Tap here](javascript:alert(1))") {
		t.Errorf("a refused link lost its text: %s", got)
	}
	// And it is escaped as text, so nothing in it can act.
	if strings.Contains(got, "<script") {
		t.Errorf("unescaped markup survived: %s", got)
	}
}

// A label is attacker-influenced text like any other, and goes through
// the same escaping.
func TestALabelIsEscaped(t *testing.T) {
	got := string(renderPostBody(`[<script>alert(1)</script>](/files/abc-123/download)`))
	if strings.Contains(got, "<script>") {
		t.Fatalf("a script tag in a label reached the page: %s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("the label wasn't escaped: %s", got)
	}
	if !strings.Contains(got, `href="/files/abc-123/download"`) {
		t.Errorf("the link itself went missing: %s", got)
	}
	// A quote in a label must not be able to reach out of the attribute
	// it does not appear in, nor break the one it does.
	quoted := string(renderPostBody(`[a" onmouseover="alert(1)](/files/abc-123/download)`))
	if strings.Contains(quoted, `onmouseover="alert`) {
		t.Errorf("a quote in a label escaped its text node: %s", quoted)
	}
}

// A file path inside an absolute URL to this same site must be linked
// once, as the whole URL — not twice, and not with the path half
// nested inside the URL half.
func TestAFilePathInsideAURLIsLinkedOnce(t *testing.T) {
	got := string(renderPostBody("See https://pack.47-yonkers.org/files/abc-123/download now"))
	if n := strings.Count(got, "<a "); n != 1 {
		t.Errorf("got %d links, want 1: %s", n, got)
	}
	if !strings.Contains(got, `href="https://pack.47-yonkers.org/files/abc-123/download"`) {
		t.Errorf("the whole URL isn't the href: %s", got)
	}
}

// Several links on one line, of both kinds, in order.
func TestSeveralLinksOnALine(t *testing.T) {
	got := string(renderPostBody(
		"Bring [the form](/files/aaa-1/download) and read https://example.org/info first"))
	if n := strings.Count(got, "<a "); n != 2 {
		t.Fatalf("got %d links, want 2: %s", n, got)
	}
	if strings.Index(got, "/files/aaa-1/download") > strings.Index(got, "example.org/info") {
		t.Error("the links came out in the wrong order")
	}
	for _, want := range []string{"Bring ", " and read ", " first"} {
		if !strings.Contains(got, want) {
			t.Errorf("the surrounding text lost %q: %s", want, got)
		}
	}
}

// What the picker writes has to be what the renderer reads. These are
// two halves of one feature and nothing else holds them together.
func TestThePickerWritesWhatTheRendererUnderstands(t *testing.T) {
	inserted := fileLinkMarkdown("Permission slip.pdf", "abc-123")
	got := string(renderPostBody("Sign this: " + inserted))

	if !strings.Contains(got, `href="/files/abc-123/download"`) {
		t.Errorf("the renderer didn't understand %q: %s", inserted, got)
	}
	if !strings.Contains(got, ">Permission slip.pdf</a>") {
		t.Errorf("the file's name isn't the link text: %s", got)
	}
}

func TestFileLinkMarkdown(t *testing.T) {
	cases := []struct{ name, label, want string }{
		{"the ordinary case", "Permission slip.pdf", "[Permission slip.pdf](/files/x1/download)"},
		// Brackets in a name would end the label early and leave the
		// rest of it loose beside the link.
		{"brackets in the name are replaced", "Form [2026]", "[Form (2026)](/files/x1/download)"},
		{"newlines and runs of space collapse", "Form\n  2026 ", "[Form 2026](/files/x1/download)"},
		{"a nameless file still reads as something", "", "[Attachment](/files/x1/download)"},
		{"a name of nothing but spaces", "   ", "[Attachment](/files/x1/download)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := fileLinkMarkdown(c.label, "x1"); got != c.want {
				t.Errorf("fileLinkMarkdown(%q) = %q, want %q", c.label, got, c.want)
			}
		})
	}
}

// The news editor offers the picker; the gallery editor doesn't, since
// its body is a list of photo URLs and a document link in it would
// render as a broken image.
func TestOnlyTheNewsEditorOffersTheFilePicker(t *testing.T) {
	if !newsKind.HasFilePicker {
		t.Error("the news editor doesn't offer the file picker")
	}
	if galleryKind.HasFilePicker {
		t.Error("the gallery editor offers a file picker, whose links its body format can't hold")
	}
}
