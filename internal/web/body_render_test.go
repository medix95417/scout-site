package web

import (
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// renderPostBody is the one place user-typed text becomes template.HTML,
// so these tests are mostly about what it must never do, and then about
// the two things it does.

func TestRenderPostBody_EscapesEverythingItDidNotBuild(t *testing.T) {
	cases := []struct{ in, mustContain, mustNotContain string }{
		{`<script>alert(1)</script>`, `&lt;script&gt;alert(1)&lt;/script&gt;`, `<script>`},
		{`<img src=x onerror=alert(1)>`, `&lt;img src=x onerror=alert(1)&gt;`, `<img`},
		{`Tom & Jerry "quotes" 'apos'`, `Tom &amp; Jerry &#34;quotes&#34; &#39;apos&#39;`, `"quotes"`},
		{`javascript:alert(1)`, `javascript:alert(1)`, `href=`},
		{`<a href="https://example.org">x</a>`, `&lt;a href=&#34;`, `<a href="https://example.org">x</a>`},
		// A URL cannot smuggle a closing quote or tag into the attribute:
		// the pattern stops at any of those characters.
		{`https://example.org/"onmouseover="alert(1)`, `href="https://example.org/"`, `onmouseover="alert`},
		{`https://example.org/<b>bold</b>`, `href="https://example.org/"`, `<b>`},
		// An ampersand inside a URL is escaped in the attribute.
		{`https://example.org/?a=1&b=2`, `href="https://example.org/?a=1&amp;b=2"`, `&b=2"`},
	}
	for _, c := range cases {
		got := string(renderPostBody(c.in))
		if !strings.Contains(got, c.mustContain) {
			t.Errorf("renderPostBody(%q)\n  = %q\n  missing %q", c.in, got, c.mustContain)
		}
		if c.mustNotContain != "" && strings.Contains(got, c.mustNotContain) {
			t.Errorf("renderPostBody(%q)\n  = %q\n  must not contain %q", c.in, got, c.mustNotContain)
		}
	}
	if got := renderPostBody(""); got != "" {
		t.Errorf("empty body should render empty, got %q", got)
	}
}

func TestRenderPostBody_LinksURLs(t *testing.T) {
	got := string(renderPostBody("Sign up at https://example.org/form, then tell Sam."))
	want := `Sign up at <a href="https://example.org/form" target="_blank" rel="noopener noreferrer"`
	if !strings.Contains(got, want) {
		t.Errorf("missing link: %q", got)
	}
	if !strings.Contains(got, `>https://example.org/form</a>, then tell Sam.`) {
		t.Errorf("trailing comma should stay outside the link: %q", got)
	}
	for _, in := range []string{"http://example.org", "HTTPS://EXAMPLE.ORG/x"} {
		if !strings.Contains(string(renderPostBody(in)), `<a href="`) {
			t.Errorf("%q should be linked", in)
		}
	}
	for _, in := range []string{"example.org", "ftp://example.org/x", "mailto:x@example.org", "www.example.org"} {
		if strings.Contains(string(renderPostBody(in)), `<a href="`) {
			t.Errorf("%q must not be linked — only http(s)", in)
		}
	}
	// Newlines survive for whitespace-pre-line.
	if got := string(renderPostBody("one\ntwo")); got != "one\ntwo" {
		t.Errorf("plain lines should pass through with their newline, got %q", got)
	}
}

func TestTrimTrailingPunctuation(t *testing.T) {
	cases := map[string][2]string{
		"https://example.org/x.":              {"https://example.org/x", "."},
		"https://example.org/x?!":             {"https://example.org/x", "?!"},
		"https://example.org/x)":              {"https://example.org/x", ")"},
		"https://en.wikipedia.org/wiki/A_(b)": {"https://en.wikipedia.org/wiki/A_(b)", ""},
		"https://example.org/x":               {"https://example.org/x", ""},
	}
	for in, want := range cases {
		c, tr := trimTrailingPunctuation(in)
		if c != want[0] || tr != want[1] {
			t.Errorf("trimTrailingPunctuation(%q) = %q, %q; want %q, %q", in, c, tr, want[0], want[1])
		}
	}
}

func TestYouTubeVideoID(t *testing.T) {
	const id = "dQw4w9WgXcQ"
	yes := map[string]int{
		"https://www.youtube.com/watch?v=" + id:            0,
		"https://youtube.com/watch?v=" + id + "&t=90s":     90,
		"https://m.youtube.com/watch?feature=x&v=" + id:    0,
		"https://youtu.be/" + id:                           0,
		"https://youtu.be/" + id + "?t=1m30s":              90,
		"https://youtu.be/" + id + "?t=1h2m3s":             3723,
		"https://www.youtube.com/shorts/" + id:             0,
		"https://www.youtube.com/embed/" + id + "?start=5": 5,
		"https://www.youtube.com/live/" + id:               0,
		"https://www.youtube-nocookie.com/embed/" + id:     0,
		"HTTPS://WWW.YOUTUBE.COM/watch?v=" + id + ".":      0, // trailing period, upper-case host
	}
	for in, wantStart := range yes {
		got, start, ok := youtubeVideoID(in)
		if !ok || got != id || start != wantStart {
			t.Errorf("youtubeVideoID(%q) = %q, %d, %v; want %q, %d, true", in, got, start, ok, id, wantStart)
		}
	}
	no := []string{
		"https://www.youtube.com/",
		"https://www.youtube.com/@somechannel",
		"https://www.youtube.com/playlist?list=PL123",
		"https://www.youtube.com/results?search_query=scouts",
		"https://www.youtube.com/watch?v=tooshort",
		"https://www.youtube.com/watch?v=" + id + "X",    // twelve chars
		"https://www.youtube.com/watch?v=dQw4w9Wg<Xc",    // markup in the id
		"https://notyoutube.com/watch?v=" + id,           // wrong host
		"https://youtube.com.evil.example/watch?v=" + id, // host suffix trick
		"https://evil.example/?u=youtube.com/watch?v=" + id,
		"ftp://youtube.com/watch?v=" + id,
		"https://vimeo.com/123456",
	}
	for _, in := range no {
		if _, _, ok := youtubeVideoID(in); ok {
			t.Errorf("youtubeVideoID(%q) must not be recognised", in)
		}
	}
}

func TestRenderPostBody_EmbedsAYouTubeLinkOnItsOwnLine(t *testing.T) {
	const id = "dQw4w9WgXcQ"
	body := "Here's the campout video:\nhttps://youtu.be/" + id + "?t=42\nEnjoy!"
	got := string(renderPostBody(body))

	if !strings.Contains(got, `<iframe class="h-full w-full" src="https://www.youtube-nocookie.com/embed/`+id+`?start=42"`) {
		t.Errorf("expected an embed on the privacy host with the start offset, got %q", got)
	}
	if !strings.Contains(got, `sandbox="allow-scripts allow-same-origin`) || !strings.Contains(got, `loading="lazy"`) {
		t.Errorf("embed is missing its sandbox or lazy loading: %q", got)
	}
	if !strings.Contains(got, `>Watch on YouTube</a>`) {
		t.Errorf("embed should carry a fallback link: %q", got)
	}
	if strings.Contains(got, "youtube.com/embed") && !strings.Contains(got, "youtube-nocookie.com/embed") {
		t.Errorf("must embed from youtube-nocookie.com, not youtube.com: %q", got)
	}
	if !strings.HasPrefix(got, "Here&#39;s the campout video:\n") || !strings.HasSuffix(got, "\nEnjoy!") {
		t.Errorf("text around the embed should be escaped and kept: %q", got)
	}

	// The same link inside a sentence is a link, not a player.
	inline := string(renderPostBody("Watch https://youtu.be/" + id + " when you can."))
	if strings.Contains(inline, "<iframe") {
		t.Errorf("an inline YouTube link must not become an embed: %q", inline)
	}
	if !strings.Contains(inline, `<a href="https://youtu.be/`+id+`"`) {
		t.Errorf("an inline YouTube link should still be a link: %q", inline)
	}

	// A YouTube URL followed by words on the same line is not "a line of
	// its own": it links, and the words stay. The query form matters:
	// url.Parse accepts a space, and with "?t=42 see you there" the id
	// segment is still exactly the id, so youtubeVideoID alone would say
	// yes — isBareURL is what says no.
	trailingWords := string(renderPostBody("https://youtu.be/" + id + "?t=42 see you there"))
	if strings.Contains(trailingWords, "<iframe") {
		t.Errorf("a YouTube link followed by words must not embed: %q", trailingWords)
	}
	if !strings.Contains(trailingWords, `</a> see you there`) {
		t.Errorf("the words after the link should remain: %q", trailingWords)
	}

	// A line that is a URL but not a YouTube video is a link, not a player.
	other := string(renderPostBody("https://vimeo.com/123456"))
	if strings.Contains(other, "<iframe") {
		t.Errorf("only YouTube embeds: %q", other)
	}

	// Surrounding whitespace does not stop a bare link from embedding.
	spaced := string(renderPostBody("   https://youtu.be/" + id + "   "))
	if !strings.Contains(spaced, "<iframe") {
		t.Errorf("a bare link with surrounding spaces should still embed: %q", spaced)
	}
}

// renderPostBody must stay the only place in this package that turns
// user-typed content into template.HTML. It is the single function
// whose escaping is reviewed line by line; a second such cast elsewhere
// would be one nobody reviewed. template.JS casts of server constants
// (newsletter/campaign starter templates) are a different thing and are
// not counted.
func TestOnlyTheBodyRendererMakesTemplateHTML(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "body_render.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "template.HTML(") {
			t.Errorf("%s constructs template.HTML — route user content through renderPostBody instead, and review it there", f)
		}
	}
}

// The detail page must hand the rendered body through unescaped — a
// field typed string would make html/template escape the markup the
// renderer built, and the video would come out as literal angle
// brackets.
func TestNewsDetailRendersTheBodyAsHTML(t *testing.T) {
	const id = "dQw4w9WgXcQ"
	out := renderPage(t, "news-detail.html", struct {
		baseData
		Title    string
		Body     template.HTML
		PostedOn string
	}{
		baseData: testBase("Campout"),
		Title:    "Campout <recap>",
		Body:     renderPostBody("Photos & video:\nhttps://youtu.be/" + id + "\n<b>not bold</b>"),
		PostedOn: "1 Sep 2026",
	})
	if !strings.Contains(out, `<iframe class="h-full w-full" src="https://www.youtube-nocookie.com/embed/`+id+`"`) {
		t.Error("the embed did not reach the page intact")
	}
	if !strings.Contains(out, "Photos &amp; video:") || !strings.Contains(out, "&lt;b&gt;not bold&lt;/b&gt;") {
		t.Error("text around the embed must arrive escaped")
	}
	if !strings.Contains(out, "Campout &lt;recap&gt;") {
		t.Error("the title still goes through the contextual escaper")
	}
}
