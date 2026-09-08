package newsletter

import (
	"strings"
	"testing"
)

// SanitizeFullHTML inverts the strategy: keep everything, remove what can
// execute. A blocklist can be wrong in a way an allowlist cannot, so the
// boundary is what these tests are about — what it keeps is a feature,
// what it lets through would be a hole.

// --- what a designed template needs, and the strict sanitizer drops ---

func TestFullHTMLKeepsWhatEmailTemplatesActuallyUse(t *testing.T) {
	cases := map[string]string{
		// The single most important one: Outlook's conditional blocks
		// ARE comments, and the strict sanitizer drops every comment.
		"Outlook conditional": `<!--[if mso]><table role="presentation"><tr><td><![endif]-->`,
		"VML background":      `<v:rect xmlns:v="urn:schemas-microsoft-com:vml" fill="true"><v:fill src="bg.png"/></v:rect>`,
		"center":              `<center>Pack 47</center>`,
		"font":                `<font face="Georgia" size="4" color="#003F87">Remembrance</font>`,
		"table attributes":    `<table><tr><td background="hero.png" valign="top" height="220" nowrap="nowrap">x</td></tr></table>`,
		"style block":         `<style>@media only screen and (max-width:600px){.col{width:100%!important}}</style>`,
		"id and dir":          `<div id="preheader" dir="ltr" lang="en">x</div>`,
		"anchor target":       `<a href="https://pack47.example.org" target="_blank" rel="noopener">Visit</a>`,
	}
	for name, in := range cases {
		out := SanitizeFullHTML(in)
		if strings.TrimSpace(out) == "" {
			t.Errorf("%s: dropped entirely\n in: %s", name, in)
			continue
		}
		// Spot-check the distinguishing token survived, not just "something".
		for _, token := range distinguishing(name) {
			if !strings.Contains(out, token) {
				t.Errorf("%s: lost %q\n in:  %s\n out: %s", name, token, in, out)
			}
		}
	}
}

func distinguishing(name string) []string {
	switch name {
	case "Outlook conditional":
		return []string{"[if mso]", "<![endif]"}
	case "VML background":
		return []string{"v:rect", "v:fill"}
	case "center":
		return []string{"<center>"}
	case "font":
		return []string{"<font", `face="Georgia"`}
	case "table attributes":
		return []string{`background="hero.png"`, `nowrap="nowrap"`}
	case "style block":
		return []string{"@media", "max-width:600px"}
	case "id and dir":
		return []string{`id="preheader"`, `dir="ltr"`}
	case "anchor target":
		return []string{`target="_blank"`}
	}
	return nil
}

// TestTheStrictSanitizerReallyDoesDropThese is the other half of the
// justification: if the strict one kept them, this whole mode would be
// unnecessary risk.
func TestTheStrictSanitizerReallyDoesDropThese(t *testing.T) {
	for _, in := range []string{
		`<!--[if mso]><table><![endif]-->`,
		`<center>Pack 47</center>`,
		`<font face="Georgia">x</font>`,
	} {
		if out := Sanitize(in); strings.Contains(out, "mso") || strings.Contains(out, "<center") || strings.Contains(out, "<font") {
			t.Errorf("the strict sanitizer kept %q as %q — full-HTML mode would not be needed for it", in, out)
		}
	}
}

// --- the boundary ---

func TestFullHTMLStillRemovesEverythingThatCanExecute(t *testing.T) {
	cases := map[string]struct{ in, mustNotContain string }{
		"script":           {`<p>hi</p><script>alert(1)</script>`, "<script"},
		"script with src":  {`<script src="https://evil.example/x.js"></script>`, "<script"},
		"iframe":           {`<iframe src="https://evil.example"></iframe>`, "<iframe"},
		"object":           {`<object data="x.swf"></object>`, "<object"},
		"embed":            {`<embed src="x.swf">`, "<embed"},
		"form":             {`<form action="https://evil.example"><input name="p"></form>`, "<form"},
		"bare input":       {`<input name="password" type="password">`, "<input"},
		"base":             {`<base href="https://evil.example/">`, "<base"},
		"link stylesheet":  {`<link rel="stylesheet" href="https://evil.example/x.css">`, "<link"},
		"onclick":          {`<div onclick="steal()">x</div>`, "onclick"},
		"onerror on img":   {`<img src="x.png" onerror="steal()">`, "onerror"},
		"onload on body":   {`<body onload="steal()">x</body>`, "onload"},
		"future handler":   {`<div onbeforetoggle="steal()">x</div>`, "onbeforetoggle"},
		"javascript href":  {`<a href="javascript:alert(1)">x</a>`, "javascript:"},
		"vbscript href":    {`<a href="vbscript:msgbox(1)">x</a>`, "vbscript:"},
		"split scheme":     {"<a href=\"java\tscript:alert(1)\">x</a>", "script:"},
		"data html href":   {`<a href="data:text/html;base64,PHNjcmlwdD4=">x</a>`, "data:text/html"},
		"srcdoc":           {`<iframe srcdoc="&lt;script&gt;alert(1)&lt;/script&gt;"></iframe>`, "srcdoc"},
		"css expression":   {`<div style="width:expression(alert(1))">x</div>`, "expression("},
		"css javascript":   {`<div style="background:url(javascript:alert(1))">x</div>`, "javascript:"},
		"moz binding":      {`<div style="-moz-binding:url(evil.xml#x)">x</div>`, "-moz-binding"},
		"style import":     {`<style>@import url("https://evil.example/x.css");</style>`, "@import"},
		"style expression": {`<style>.x{width:expression(alert(1))}</style>`, "expression("},
		"formaction":       {`<button formaction="javascript:alert(1)">x</button>`, "formaction"},
		"svg script":       {`<svg><script>alert(1)</script></svg>`, "<script"},
	}
	for name, c := range cases {
		out := strings.ToLower(SanitizeFullHTML(c.in))
		flat := strings.Map(func(r rune) rune {
			if r <= 0x20 {
				return -1
			}
			return r
		}, out)
		if strings.Contains(flat, strings.ToLower(c.mustNotContain)) {
			t.Errorf("%s: %q survived\n in:  %s\n out: %s", name, c.mustNotContain, c.in, out)
		}
	}
}

// TestAScriptTagTakesItsContentsWithIt — dropping the tag and keeping the
// body would dump the code into the page as text, which reads as
// gibberish at best and, unescaped somewhere, executes.
func TestAScriptTagTakesItsContentsWithIt(t *testing.T) {
	out := SanitizeFullHTML(`<p>before</p><script>var secret = 41;</script><p>after</p>`)
	if strings.Contains(out, "secret") {
		t.Errorf("the script's contents leaked into the body:\n%s", out)
	}
	if !strings.Contains(out, "before") || !strings.Contains(out, "after") {
		t.Errorf("dropping the script took its neighbours with it:\n%s", out)
	}
}

// TestMarkupAfterAnEarlyCommentEndIsStillHandled. Comments are kept —
// that is the point, for Outlook — so the question is what happens to
// whatever a malformed one leaves behind. The parser ends the comment at
// the first "-->" and the script becomes a real element, which has to be
// dropped like any other. (The "-->" escaping in renderFull is belt and
// braces for a tree not built by the parser; no input reaches it.)
func TestMarkupAfterAnEarlyCommentEndIsStillHandled(t *testing.T) {
	out := SanitizeFullHTML(`<!--[if mso]> --><script>alert(1)</script><!-- <![endif]-->`)
	if strings.Contains(out, "<script") {
		t.Errorf("a comment was broken out of:\n%s", out)
	}
}

// TestDocumentWrappersAreUnwrappedNotDropped — an email body is a
// fragment, but the content inside <html>/<body> is the whole message.
func TestDocumentWrappersAreUnwrappedNotDropped(t *testing.T) {
	out := SanitizeFullHTML(`<html><head><title>T</title><meta charset="utf-8"></head><body><p>Kept</p></body></html>`)
	if !strings.Contains(out, "<p>Kept</p>") {
		t.Errorf("the message was lost with its wrapper:\n%s", out)
	}
	for _, gone := range []string{"<html", "<body", "<head", "<title", "<meta"} {
		if strings.Contains(out, gone) {
			t.Errorf("%s survived into an email body fragment:\n%s", gone, out)
		}
	}
}

// TestVoidElementsAreNotGivenClosingTags — <img></img> is markup a parser
// has to guess about, and mail clients guess differently.
func TestVoidElementsAreNotGivenClosingTags(t *testing.T) {
	out := SanitizeFullHTML(`<p>a<br>b<img src="https://x.example/i.png"><hr></p>`)
	for _, bad := range []string{"</br>", "</img>", "</hr>"} {
		if strings.Contains(out, bad) {
			t.Errorf("%s emitted:\n%s", bad, out)
		}
	}
}

// TestSanitizeForPicksTheRightOne — the switch every caller goes through.
func TestSanitizeForPicksTheRightOne(t *testing.T) {
	in := `<center>Pack 47</center><script>alert(1)</script>`

	strict := SanitizeFor(in, false)
	if strings.Contains(strict, "<center") {
		t.Errorf("strict mode kept <center>: %s", strict)
	}
	full := SanitizeFor(in, true)
	if !strings.Contains(full, "<center") {
		t.Errorf("full mode dropped <center>: %s", full)
	}
	// Neither mode ever keeps a script.
	for name, out := range map[string]string{"strict": strict, "full": full} {
		if strings.Contains(out, "<script") {
			t.Errorf("%s mode kept a script: %s", name, out)
		}
	}
}
