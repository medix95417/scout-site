package newsletter

// The other sanitizing strategy, for a template somebody designed
// elsewhere.
//
// Sanitize keeps an allowlist: a small set of tags and attributes, and
// everything else goes. That is right for the editor, where the tags are
// known in advance and an allowlist is the only approach that does not
// have to anticipate every way of hiding something dangerous.
//
// It is wrong for a real email template, which leans on things no
// allowlist built around a WYSIWYG toolbar would think to include:
//
//   - <!--[if mso]> … <![endif]--> blocks that only Outlook reads, and
//     the VML shapes inside them that give it background images
//   - <center> and <font>, which are obsolete on the web and still the
//     most reliable way to do those two things in Outlook
//   - per-element attributes a designer's export scatters everywhere —
//     background, hspace, valign, dir, id
//
// Stripped of those, a template still sends. It just no longer looks
// like the thing that was designed, which is the whole reason somebody
// used a design tool.
//
// So SanitizeFullHTML inverts the strategy: keep everything, remove the
// things that can execute. That is a deliberately weaker guarantee, and
// it is why this is opt-in per newsletter and limited to an Admin (see
// internal/web's requireFullHTMLAuthor). A blocklist can be wrong in a
// way an allowlist cannot — it has to know about the danger to remove
// it — and the honest statement of what this gives you is: nothing in
// the body can run script in a browser we control, and the rest is the
// author's responsibility.
//
// Where the output ends up, which is what decides how much that matters:
//
//   - A mail client, which runs its own far stricter sanitizer over
//     anything we send. No mail client executes script in a message.
//   - The sent-newsletter view, in an <iframe sandbox="" srcdoc> — a
//     separate origin with scripting off, so markup this misses cannot
//     reach a leader's session from there.
//   - The COMPOSER, and this one is not sandboxed: reopening a draft
//     assigns the body to quill.root.innerHTML, same-origin, in the
//     page. innerHTML does not run <script>, but it DOES fire an
//     inline handler on an element it inserts — an <img onerror> would
//     execute as the leader who opened the draft.
//
// That third case is why the removals here are load-bearing rather than
// belt and braces, and why the "on" prefix check covers every handler
// rather than a list of the ones in fashion. Anything that can execute
// has to be gone before a body is stored, because storing it is what
// arms it.
//
// The author being an Admin is the last line, not the first: somebody
// who can already change site settings and grant themselves roles is not
// gaining a privilege here, only a way to shoot their own foot. It is
// not a reason to be careless about the markup.

import (
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// executableTags are dropped with everything inside them. Each one either
// runs code, fetches and renders a document of its own, or collects
// input — none has any business in an email, and every one of them is a
// way to turn a preview into a page that acts.
//
// <base> is here for a subtler reason than the rest: it rewrites what
// every relative URL in the document resolves to, so one tag can silently
// repoint every link in a newsletter at somewhere else.
var executableTags = map[string]bool{
	"script": true, "iframe": true, "object": true, "embed": true,
	"applet": true, "form": true, "input": true, "button": true,
	"textarea": true, "select": true, "option": true,
	"base": true, "link": true, "frame": true, "frameset": true,
	"noscript": true, "portal": true,
}

// documentTags are unwrapped rather than dropped: they carry no risk, but
// an email body is a fragment, and a nested <html>/<body> is at best
// ignored and at worst confuses a mail client's own parser. Their
// children are kept.
var documentTags = map[string]bool{
	"html": true, "head": true, "body": true, "meta": true, "title": true,
}

// SanitizeFullHTML keeps a designed template intact and removes only what
// can execute. See this file's header for what that does and does not
// promise; use Sanitize unless a leader has explicitly asked for this.
func SanitizeFullHTML(rawHTML string) string {
	return sanitizeFull(rawHTML, nil)
}

func sanitizeFull(rawHTML string, store ImageStore) string {
	nodes, err := html.ParseFragment(strings.NewReader(rawHTML), &html.Node{
		Type:     html.ElementNode,
		Data:     "body",
		DataAtom: atom.Body,
	})
	if err != nil {
		return ""
	}

	s := sanitizer{store: store}
	var b strings.Builder
	for _, n := range nodes {
		s.renderFull(&b, n)
	}
	return b.String()
}

func (s sanitizer) renderFull(b *strings.Builder, n *html.Node) {
	switch n.Type {
	case html.TextNode:
		// <style> never reaches here — its whole subtree is handled in
		// the ElementNode case below, so its CSS is checked rather than
		// emitted raw. Everything else is text and gets escaped.
		b.WriteString(html.EscapeString(n.Data))
		return

	case html.CommentNode:
		// Kept, which the strict sanitizer does not do, and the single
		// most important difference for Outlook: <!--[if mso]> blocks
		// ARE comments.
		//
		// The "-->" replacement is belt and braces, not a live guard:
		// the parser ends a comment at the first "-->", so a CommentNode
		// coming out of html.ParseFragment cannot contain one (probed,
		// including bogus comments and "--!>"). It costs a string scan
		// and it means this function is still correct if it is ever
		// handed a tree built some other way.
		b.WriteString("<!--")
		b.WriteString(strings.ReplaceAll(n.Data, "-->", "--&gt;"))
		b.WriteString("-->")
		return

	case html.ElementNode:
		if executableTags[n.Data] {
			return // and its subtree with it
		}
		// A <style> block is CSS, and CSS is not inert: @import fetches a
		// stylesheet from anywhere, and expression()/-moz-binding/
		// behavior: were all ways to run code in browsers a mail client
		// may still be built on. Checked with the same sanitizeCSS the
		// strict path uses, and the block dropped whole if anything in
		// it is refused — a stylesheet half-applied is worse than none.
		if n.Data == "style" {
			css, ok := sanitizeCSS(textOf(n))
			if !ok || strings.TrimSpace(css) == "" {
				return
			}
			b.WriteString("<style>")
			b.WriteString(css)
			b.WriteString("</style>")
			return
		}
		if documentTags[n.Data] {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				s.renderFull(b, c)
			}
			return
		}

	default:
		return // doctype and anything else structural
	}

	b.WriteString("<")
	b.WriteString(n.Data)
	for _, a := range n.Attr {
		if !safeFullAttr(a.Key, a.Val) {
			continue
		}
		// Same hosting hook as the strict path, and checked for safety
		// first there too — see sanitizer.hosted.
		val := a.Val
		if a.Key == "src" {
			val = s.hosted(val)
		}
		b.WriteString(" ")
		b.WriteString(a.Key)
		b.WriteString(`="`)
		b.WriteString(html.EscapeString(val))
		b.WriteString(`"`)
	}
	b.WriteString(">")

	if voidElements[n.Data] {
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		s.renderFull(b, c)
	}
	b.WriteString("</")
	b.WriteString(n.Data)
	b.WriteString(">")
}

// voidElements have no closing tag. Emitting one produces markup a parser
// has to guess about, and guesses differ between mail clients.
var voidElements = map[string]bool{
	"area": true, "br": true, "col": true, "embed": true, "hr": true,
	"img": true, "input": true, "link": true, "meta": true, "param": true,
	"source": true, "track": true, "wbr": true,
}

// safeFullAttr is the blocklist half: an attribute survives unless it is
// one of the few that can run something.
func safeFullAttr(key, val string) bool {
	lower := strings.ToLower(key)

	// Every event handler, present and future. Checking the "on" prefix
	// rather than naming them means onbeforetoggle — and whatever is
	// invented next — is covered without an update here.
	if strings.HasPrefix(lower, "on") {
		return false
	}
	// srcdoc is a whole document inside an attribute, and would need this
	// same treatment recursively to be safe. Not worth it for something
	// no email uses.
	if lower == "srcdoc" {
		return false
	}

	switch lower {
	case "href", "src", "action", "formaction", "background", "poster", "cite", "longdesc", "usemap", "data":
		// Anything that resolves to a URL gets the strict sanitizer's own
		// scheme check, control characters and all — an allowlist is
		// exactly right for a scheme, since there are four worth having.
		return safeURL(val) || safeImageDataURI(val)
	case "style":
		return safeStyleAttr(val)
	}
	return true
}

// safeStyleAttr rejects the handful of CSS constructs that can execute or
// fetch. Shared vocabulary with sanitizeCSS, which does the same job for
// a whole <style> block.
func safeStyleAttr(val string) bool {
	lower := strings.ToLower(stripURLNoise(val))
	for _, banned := range cssBanned {
		if strings.Contains(lower, banned) {
			return false
		}
	}
	return true
}
