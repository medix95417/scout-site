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
// can still carry: a javascript:/data: URL in href/src, or an old
// IE-only CSS expression() in style — the attribute name being allowed
// isn't enough on its own.
func sanitizeAttrValue(key, val string) bool {
	lower := strings.ToLower(strings.TrimSpace(val))
	switch key {
	case "href", "src":
		if strings.HasPrefix(lower, "javascript:") || strings.HasPrefix(lower, "data:") {
			return false
		}
	case "style":
		if strings.Contains(lower, "expression(") || strings.Contains(lower, "javascript:") {
			return false
		}
	}
	return true
}
