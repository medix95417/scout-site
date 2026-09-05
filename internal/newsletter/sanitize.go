package newsletter

import (
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// allowedTags is the HTML subset a newsletter body may contain — enough to
// cover everything Quill's toolbar (see admin-newsletter-form.html) can
// produce, deliberately excluding anything that can execute script or embed
// a remote frame (script, iframe, object, embed, form, style, link, meta).
var allowedTags = map[string]bool{
	"p": true, "br": true, "strong": true, "b": true, "em": true, "i": true,
	"u": true, "s": true, "del": true, "blockquote": true, "span": true, "div": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"ul": true, "ol": true, "li": true, "a": true, "img": true,
	"table": true, "thead": true, "tbody": true, "tr": true, "td": true, "th": true,
	// <style> is allowed, with its CSS checked by sanitizeCSS and its
	// content emitted raw rather than HTML-escaped — see renderSanitized.
	//
	// It was excluded originally, alongside script/iframe/object/embed/
	// form/link/meta, and for that list that is still right. But a style
	// block is not in their class: it cannot execute anything once the
	// handful of legacy CSS escapes below are refused, and dropping it
	// silently gutted every real email template. A design tool's export
	// puts its typography, its colours and its whole responsive layout in
	// a <style> block; a leader pasting one saw the styling vanish with no
	// message saying why.
	"style": true,
}

// allowedAttrs is the attribute allowlist per tag, checked in addition to
// sanitizeAttrValue below — an attribute not listed here for its tag is
// dropped outright, regardless of value.
var allowedAttrs = map[string]map[string]bool{
	"a":   {"href": true},
	"img": {"src": true, "alt": true, "width": true, "height": true},

	// Table presentation. HTML email is still built out of tables with
	// these attributes — every template a tool like Mailchimp produces
	// leans on them, and a leader can now upload one of those (see
	// admin-newsletter-form.html's HTML-source mode), so dropping them
	// would silently reduce a carefully-built template to unstyled rows.
	//
	// Safe to allow because they are inert: each one takes a length, a
	// colour or a keyword, and none can carry a URL or script. The
	// attributes that CAN — href, src, style — are still checked by
	// value in sanitizeAttrValue below, and every script-capable tag
	// remains absent from allowedTags.
	"table": tablePresentationAttrs,
	"thead": tablePresentationAttrs,
	"tbody": tablePresentationAttrs,
	"tr":    tablePresentationAttrs,
	"td":    tablePresentationAttrs,
	"th":    tablePresentationAttrs,
}

// tablePresentationAttrs is shared by every table element above.
var tablePresentationAttrs = map[string]bool{
	"width": true, "height": true, "align": true, "valign": true,
	"bgcolor": true, "border": true, "cellpadding": true, "cellspacing": true,
	"colspan": true, "rowspan": true,
	// role="presentation" is how a layout table tells a screen reader not
	// to announce it as data — worth keeping for exactly that reason.
	"role": true,
}

// globalAttrs are allowed on every element — style/class are how Quill
// expresses color, alignment, and indent (e.g. class="ql-align-center"),
// not just cosmetic extras, so they're allowed broadly rather than per-tag.
var globalAttrs = map[string]bool{"style": true, "class": true}

// Sanitize strips a WYSIWYG-authored HTML fragment down to allowedTags/
// allowedAttrs before it's stored or emailed — defense in depth against a
// leader's editor session (or a bug in the CDN-hosted editor itself)
// smuggling in a <script> tag or an onerror= handler that would otherwise
// run in a fellow leader's browser the next time they open the draft to
// edit it, or in a recipient's mail client. Unknown tags are dropped
// entirely (with their subtree, for the genuinely dangerous ones like
// <script>) rather than unwrapped — simpler, and Quill's own output never
// relies on a tag outside allowedTags carrying meaningful content.
func Sanitize(rawHTML string) string {
	nodes, err := html.ParseFragment(strings.NewReader(rawHTML), &html.Node{
		Type:     html.ElementNode,
		Data:     "body",
		DataAtom: atom.Body,
	})
	if err != nil {
		return ""
	}

	var b strings.Builder
	for _, n := range nodes {
		renderSanitized(&b, n)
	}
	return b.String()
}

func renderSanitized(b *strings.Builder, n *html.Node) {
	switch n.Type {
	case html.TextNode:
		b.WriteString(html.EscapeString(n.Data))
		return
	case html.ElementNode:
		if !allowedTags[n.Data] {
			return // drop disallowed tags (and their subtree) entirely
		}
	default:
		return // comments, doctypes, etc. — no legitimate reason for a newsletter body to carry one
	}

	tag := n.Data

	// <style> is the one element whose children are NOT markup. Its text
	// is CSS, and HTML-escaping it would corrupt every child selector
	// ("a > b" becoming "a &gt; b") and every "&" in a media query. So it
	// is emitted raw, which is exactly why its content has to be checked
	// as CSS first — see sanitizeCSS. Attributes are dropped: nothing an
	// email needs lives on the tag itself.
	if tag == "style" {
		css, ok := sanitizeCSS(textOf(n))
		if !ok || strings.TrimSpace(css) == "" {
			return
		}
		b.WriteString("<style>")
		b.WriteString(css)
		b.WriteString("</style>")
		return
	}

	b.WriteString("<")
	b.WriteString(tag)
	for _, a := range n.Attr {
		if !globalAttrs[a.Key] && !allowedAttrs[tag][a.Key] {
			continue
		}
		if !sanitizeAttrValue(a.Key, a.Val) {
			continue
		}
		b.WriteString(" ")
		b.WriteString(a.Key)
		b.WriteString(`="`)
		b.WriteString(html.EscapeString(a.Val))
		b.WriteString(`"`)
	}
	b.WriteString(">")

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		renderSanitized(b, c)
	}

	if tag != "br" && tag != "img" {
		b.WriteString("</")
		b.WriteString(tag)
		b.WriteString(">")
	}
}

// sanitizeAttrValue blocks the value-based attacks an allowlisted attribute
// can still carry: a dangerous URL scheme in href/src, or an old IE-only
// CSS expression() in style — the attribute name being allowed isn't
// enough on its own.
func sanitizeAttrValue(key, val string) bool {
	switch key {
	case "href":
		return safeURL(val)
	case "src":
		// An image may also be embedded in the document itself. See
		// safeImageDataURI for why that is allowed here and NOT on href.
		return safeURL(val) || safeImageDataURI(val)
	case "style":
		lower := strings.ToLower(stripURLNoise(val))
		if strings.Contains(lower, "expression(") || strings.Contains(lower, "javascript:") || strings.Contains(lower, "vbscript:") {
			return false
		}
	}
	return true
}

// safeURL reports whether a href/src value uses a scheme we're willing to
// emit. It uses a positive allowlist (http, https, mailto, tel, or a
// scheme-relative/relative/anchor URL) rather than a blocklist of bad
// schemes: a blocklist has to anticipate every dangerous scheme AND every
// way of obfuscating it, while an allowlist rejects anything it doesn't
// positively recognize. Crucially it strips the ASCII control characters
// and whitespace browsers themselves ignore inside a scheme BEFORE
// checking — otherwise "java\tscript:" or "java\nscript:" reads as a
// harmless unknown-scheme string here but still executes as javascript:
// once a browser (or mail client) parses it.
func safeURL(val string) bool {
	cleaned := strings.ToLower(stripURLNoise(val))

	// No scheme at all (relative path, "#anchor", "?query", or a bare
	// "//host" scheme-relative URL) — nothing to execute.
	colon := strings.IndexByte(cleaned, ':')
	if colon == -1 {
		return true
	}
	// A colon that only appears after a '/', '?', or '#' is part of the
	// path/query/fragment (e.g. "/a/b:c"), not a scheme separator.
	if slash := strings.IndexAny(cleaned, "/?#"); slash != -1 && slash < colon {
		return true
	}

	scheme := cleaned[:colon]
	switch scheme {
	case "http", "https", "mailto", "tel":
		return true
	default:
		return false
	}
}

// stripURLNoise removes the leading/trailing spaces and the embedded ASCII
// control characters (including tab, newline, carriage return, form feed,
// and NUL) that browsers strip from a URL before resolving its scheme —
// see safeURL for why this matters.
func stripURLNoise(val string) string {
	return strings.Map(func(r rune) rune {
		if r <= 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, val)
}

// textOf concatenates an element's direct text content — for <style>,
// whose single child is the CSS.
func textOf(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		}
	}
	return b.String()
}

// cssBanned are the constructs that make CSS executable or fetching, and
// the one sequence that would end the block early.
//
// Each is a real escape rather than a precaution: expression() ran
// arbitrary JScript in old IE; -moz-binding pointed at an XBL document
// that could script; behavior: did the same through an HTC file;
// javascript:/vbscript: in a url() still execute in some clients; @import
// pulls in a stylesheet from anywhere, which is a tracking beacon at best
// and someone else's CSS at worst. "</style" is the breakout — without
// refusing it, CSS could close its own block and open a script tag.
var cssBanned = []string{
	"expression(",
	"javascript:",
	"vbscript:",
	"-moz-binding",
	"behavior:",
	"@import",
	"</style",
}

// sanitizeCSS decides whether a <style> block is safe to emit as-is.
//
// All-or-nothing on purpose. Surgically editing CSS means parsing it, and
// a half-understood stylesheet that gets partially rewritten is a worse
// outcome than one that is refused whole — the leader can see their
// styling is missing and ask why, where they would never notice one
// silently altered rule. The banned list is short and every entry is a
// known execution or fetch vector.
//
// Checked against a copy with the ASCII control characters and whitespace
// browsers themselves ignore stripped out, for the same reason safeURL
// does it: "java\tscript:" reads as harmless here and executes there.
func sanitizeCSS(css string) (string, bool) {
	probe := strings.ToLower(stripURLNoise(css))
	for _, bad := range cssBanned {
		if strings.Contains(probe, bad) {
			return "", false
		}
	}
	return css, true
}

// imageDataURI matches an embedded raster image: the form a design tool
// produces when it inlines a logo rather than hosting it.
//
// Deliberately an enumeration of raster types rather than "data:image/".
// SVG is an image by MIME type and a scripted document by capability — it
// can carry <script> and event handlers — so data:image/svg+xml is not on
// this list and must not be added to it.
//
// The payload is base64 only. A plain (non-base64) data: URI can hold
// arbitrary text, which is a harder thing to reason about for no benefit:
// every tool that inlines an image base64-encodes it.
var imageDataURI = regexp.MustCompile(`^(?i:data:image/(png|jpeg|jpg|gif|webp|bmp);base64,)[a-zA-Z0-9+/=]+$`)

// safeImageDataURI reports whether a src is an embedded image.
//
// Allowed on src, and NOT on href, and the distinction is the whole point.
// A data: URI in an <img> is decoded as image bytes and rendered — if the
// bytes are not a valid image, nothing happens. The same URI in an <a
// href> is a document the browser will NAVIGATE to, which for
// data:text/html means running attacker-authored markup on a page the
// reader believes is ours. Same scheme, completely different exposure.
//
// Checked against a copy with the whitespace and control characters
// browsers ignore stripped out — not as a defence (this is an allowlist,
// so stripping only ever widens what it accepts) but because exporters
// pad a long URI out from the quote and wrap its base64 across lines to
// keep the file readable. A browser ignores that whitespace when it
// decodes; rejecting the image over its formatting would be pure loss.
func safeImageDataURI(val string) bool {
	return imageDataURI.MatchString(stripURLNoise(val))
}
