package newsletter

import (
	"strings"
	"testing"
)

func TestSanitize_StripsScriptTags(t *testing.T) {
	out := Sanitize(`<p>Hi <strong>families</strong>!</p><script>alert(1)</script>`)
	if strings.Contains(out, "<script") || strings.Contains(out, "alert(1)") {
		t.Errorf("script tag survived sanitization: %q", out)
	}
	if !strings.Contains(out, "<strong>families</strong>") {
		t.Errorf("legitimate formatting was stripped: %q", out)
	}
}

func TestSanitize_StripsEventHandlerAttributes(t *testing.T) {
	out := Sanitize(`<p onclick="alert(1)">click <a href="javascript:alert(1)">me</a></p>`)
	if strings.Contains(out, "onclick") {
		t.Errorf("onclick handler survived sanitization: %q", out)
	}
	if strings.Contains(out, "javascript:") {
		t.Errorf("javascript: URL survived sanitization: %q", out)
	}
}

func TestSanitize_StripsIframe(t *testing.T) {
	out := Sanitize(`<iframe src="https://evil.example.com"></iframe><p>after</p>`)
	if strings.Contains(out, "<iframe") {
		t.Errorf("iframe survived sanitization: %q", out)
	}
	if !strings.Contains(out, "<p>after</p>") {
		t.Errorf("sibling content after a stripped tag was lost: %q", out)
	}
}

func TestSanitize_PreservesQuillFormattingOutput(t *testing.T) {
	in := `<p style="color: rgb(230, 0, 0);" class="ql-align-center">Centered <em>red</em> text</p>`
	out := Sanitize(in)
	if !strings.Contains(out, `class="ql-align-center"`) || !strings.Contains(out, "color: rgb(230, 0, 0)") {
		t.Errorf("legitimate Quill style/class was stripped: %q", out)
	}
}

func TestSanitize_PreservesListsAndLinks(t *testing.T) {
	out := Sanitize(`<ul><li>one</li><li>two</li></ul><a href="https://example.com">link</a>`)
	if !strings.Contains(out, "<ul>") || !strings.Contains(out, "<li>one</li>") {
		t.Errorf("list markup was stripped: %q", out)
	}
	if !strings.Contains(out, `href="https://example.com"`) {
		t.Errorf("legitimate link was stripped: %q", out)
	}
}

func TestSanitize_StripsImgOnError(t *testing.T) {
	out := Sanitize(`<img src="https://example.com/x.png" onerror="alert(1)" alt="x">`)
	if strings.Contains(out, "onerror") {
		t.Errorf("onerror attribute survived sanitization: %q", out)
	}
	if !strings.Contains(out, `src="https://example.com/x.png"`) {
		t.Errorf("legitimate image src was stripped: %q", out)
	}
}

func TestSanitize_BlocksObfuscatedSchemes(t *testing.T) {
	// Each of these must NOT survive with a working javascript:/vbscript:
	// href — browsers strip embedded control chars from a scheme before
	// evaluating it, so a naive prefix check on the raw value misses them.
	dangerous := []string{
		`<a href="java` + "\t" + `script:alert(1)">x</a>`,
		`<a href="java` + "\n" + `script:alert(1)">x</a>`,
		`<a href="  javascript:alert(1)">x</a>`,
		`<a href="JaVaScRiPt:alert(1)">x</a>`,
		`<a href="vbscript:msgbox(1)">x</a>`,
		`<a href="data:text/html,<b>x</b>">x</a>`,
	}
	for _, in := range dangerous {
		out := Sanitize(in)
		flat := strings.ToLower(strings.Map(func(r rune) rune {
			if r <= 0x20 {
				return -1
			}
			return r
		}, out))
		if strings.Contains(flat, "javascript:") || strings.Contains(flat, "vbscript:") || strings.Contains(flat, "data:text/html") {
			t.Errorf("dangerous scheme survived:\n in: %q\nout: %q", in, out)
		}
	}
}

func TestSanitize_KeepsLegitimateURLs(t *testing.T) {
	safe := []struct {
		in     string
		expect string
	}{
		{`<a href="https://example.com/x?a=b#c">x</a>`, "https://example.com/x?a=b#c"},
		{`<a href="http://example.com">x</a>`, "http://example.com"},
		{`<a href="mailto:leader@example.com">x</a>`, "mailto:leader@example.com"},
		{`<a href="tel:+15555551234">x</a>`, "tel:+15555551234"},
		{`<a href="/calendar">x</a>`, "/calendar"},
		{`<a href="#section">x</a>`, "#section"},
		{`<img src="https://example.com/p.png">`, "https://example.com/p.png"},
	}
	for _, c := range safe {
		out := Sanitize(c.in)
		if !strings.Contains(out, c.expect) {
			t.Errorf("legitimate URL was stripped:\n in: %q\nout: %q (wanted to contain %q)", c.in, out, c.expect)
		}
	}
}

// TestSanitize_KeepsEmailTableMarkup covers what an uploaded HTML email
// template actually depends on. These attributes are inert — a length, a
// colour or a keyword — and every table-based email in existence uses
// them, so stripping them would quietly flatten a template a leader had
// carefully built elsewhere.
func TestSanitize_KeepsEmailTableMarkup(t *testing.T) {
	in := `<table role="presentation" width="600" cellpadding="0" cellspacing="0" border="0" bgcolor="#f4f4f4">` +
		`<tr><td align="center" valign="top" colspan="2" style="padding:24px;font-family:Arial">Hi</td></tr></table>`
	out := Sanitize(in)

	for _, want := range []string{
		`role="presentation"`, `width="600"`, `cellpadding="0"`, `cellspacing="0"`,
		`border="0"`, `bgcolor="#f4f4f4"`, `align="center"`, `valign="top"`,
		`colspan="2"`, `padding:24px`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("email table markup %s was stripped:\n%s", want, out)
		}
	}
}

// TestSanitize_StillStripsTheDangerousParts is the other half of the
// widening above: allowing presentational attributes must not have
// loosened anything that can execute. Checked together in one test so a
// future widening has to keep passing it.
func TestSanitize_StillStripsTheDangerousParts(t *testing.T) {
	in := `<!DOCTYPE html><html><head><style>body{x:y}</style></head><body>` +
		`<table cellpadding="4" onmouseover="steal()"><tr><td background="javascript:alert(1)">` +
		`<script>alert(1)</script><iframe src="//evil"></iframe>` +
		`<img src="x" onerror="alert(1)"><a href="javascript:alert(1)">go</a>` +
		`<td style="background:url(javascript:alert(1))">hi</td></tr></table></body></html>`
	out := Sanitize(in)

	// "<style" is deliberately absent from this list now: a style block
	// with safe CSS is allowed (see TestSanitizeKeepsStyleBlocks), because
	// dropping it gutted every real email template. The CSS inside one is
	// checked separately, and TestSanitizeRejectsDangerousCSS covers that
	// — this input's style block is harmless and may survive. Everything
	// else here must still be impossible.
	for _, forbidden := range []string{
		"<script", "<iframe", "onerror", "onmouseover", "javascript:", "DOCTYPE", "background=",
	} {
		if strings.Contains(strings.ToLower(out), strings.ToLower(forbidden)) {
			t.Errorf("%q survived sanitization — this must never be reachable:\n%s", forbidden, out)
		}
	}
	// The safe parts of the same input should still come through.
	if !strings.Contains(out, `cellpadding="4"`) {
		t.Errorf("a safe attribute alongside dangerous ones was dropped:\n%s", out)
	}
	if !strings.Contains(out, "hi") {
		t.Errorf("text content was lost:\n%s", out)
	}
}

// A real email template keeps its styling.
//
// Every design tool puts typography, colour and the whole responsive
// layout in a <style> block. Dropping it silently — which is what used to
// happen — left a leader looking at an unstyled email with nothing saying
// why, and is the reason this was reported as "the HTML doesn't display
// correctly".
func TestSanitizeKeepsStyleBlocks(t *testing.T) {
	in := `<style>.hdr { color:#003F87 } @media (max-width:600px){ .hdr{font-size:20px} }</style>
<h1 class="hdr">Come and visit</h1>`
	out := Sanitize(in)

	for _, want := range []string{"<style>", ".hdr", "#003F87", "@media (max-width:600px)"} {
		if !strings.Contains(out, want) {
			t.Errorf("sanitized output lost %q:\n%s", want, out)
		}
	}
	// CSS is not markup: escaping it would corrupt child selectors and
	// media queries.
	if strings.Contains(out, "&gt;") || strings.Contains(out, "&amp;") {
		t.Errorf("CSS was HTML-escaped, which corrupts it:\n%s", out)
	}
}

func TestSanitizeKeepsChildSelectorsIntact(t *testing.T) {
	out := Sanitize(`<style>table > tbody > tr { padding:0 }</style>`)
	if !strings.Contains(out, "table > tbody > tr") {
		t.Errorf("child selectors were mangled:\n%s", out)
	}
}

// The block is refused whole rather than partly rewritten. Each of these
// is a real execution or fetch vector, not a precaution.
func TestSanitizeRejectsDangerousCSS(t *testing.T) {
	for name, css := range map[string]string{
		"IE expression":     `.a { width: expression(alert(1)) }`,
		"javascript url":    `.a { background: url(javascript:alert(1)) }`,
		"vbscript url":      `.a { background: url(vbscript:msgbox(1)) }`,
		"moz-binding":       `.a { -moz-binding: url(http://evil.example/x.xml#e) }`,
		"IE behavior":       `.a { behavior: url(evil.htc) }`,
		"remote import":     `@import url("http://evil.example/x.css");`,
		"obfuscated scheme": `.a { background: url(java` + "\t" + `script:alert(1)) }`,
	} {
		out := Sanitize("<style>" + css + "</style><p>body</p>")
		if strings.Contains(out, "<style") {
			t.Errorf("%s: dangerous CSS was emitted:\n%s", name, out)
		}
		// The rest of the message must survive — refusing the stylesheet
		// is not a reason to lose the letter.
		if !strings.Contains(out, "<p>body</p>") {
			t.Errorf("%s: refusing the style block also dropped the body:\n%s", name, out)
		}
	}
}

// The breakout: CSS that closes its own block and opens something else.
func TestSanitizeRejectsStyleBreakout(t *testing.T) {
	out := Sanitize(`<style>.a{}</style><script>alert(1)</script>`)
	if strings.Contains(strings.ToLower(out), "<script") {
		t.Fatalf("a script tag survived:\n%s", out)
	}

	// And the same attempted from inside the CSS text.
	out = Sanitize("<style>.a{} </style><script>alert(1)</script></style>")
	if strings.Contains(strings.ToLower(out), "<script") {
		t.Fatalf("a script tag survived a style breakout:\n%s", out)
	}
}

// Everything genuinely dangerous stays out.
func TestSanitizeStillDropsExecutableTags(t *testing.T) {
	for _, in := range []string{
		`<script>alert(1)</script>`,
		`<iframe src="http://evil.example"></iframe>`,
		`<object data="x"></object>`,
		`<embed src="x">`,
		`<form action="http://evil.example"><input name="p"></form>`,
		`<link rel="stylesheet" href="http://evil.example/x.css">`,
		`<img src=x onerror="alert(1)">`,
	} {
		out := Sanitize(in)
		for _, bad := range []string{"<script", "<iframe", "<object", "<embed", "<form", "<link", "onerror"} {
			if strings.Contains(strings.ToLower(out), bad) {
				t.Errorf("input %q produced %q in:\n%s", in, bad, out)
			}
		}
	}
}
