package newsletter

import (
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
	case "href", "src":
		return safeURL(val)
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
