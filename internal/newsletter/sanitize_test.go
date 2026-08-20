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
