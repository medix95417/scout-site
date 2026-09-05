package newsletter

import (
	"encoding/base64"
	"strings"
	"testing"
)

// onePixelGIF is a real, decodable GIF, so a test asserting "these are
// the bytes the store was handed" is asserting something meaningful.
var onePixelGIF = []byte{
	0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00, 0x01, 0x00, 0x80, 0x00,
	0x00, 0x00, 0x00, 0x00, 0xff, 0xff, 0xff, 0x21, 0xf9, 0x04, 0x01, 0x00,
	0x00, 0x00, 0x00, 0x2c, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00,
	0x00, 0x02, 0x01, 0x44, 0x00, 0x3b,
}

func gifDataURI() string {
	return "data:image/gif;base64," + base64.StdEncoding.EncodeToString(onePixelGIF)
}

// TestHostingReplacesTheEmbeddedImage is the whole point: the body comes
// out pointing at a URL, and carrying none of the image.
func TestHostingReplacesTheEmbeddedImage(t *testing.T) {
	var gotData []byte
	var gotType string
	out := SanitizeHostingImages(`<p>hi</p><img src="`+gifDataURI()+`" alt="Pack 47">`,
		func(data []byte, declaredType string) string {
			gotData, gotType = data, declaredType
			return "https://pack47.example.org/files/abc/download"
		})

	if !strings.Contains(out, `src="https://pack47.example.org/files/abc/download"`) {
		t.Errorf("body does not point at the hosted URL:\n%s", out)
	}
	if strings.Contains(out, "data:image") {
		t.Errorf("the embedded copy is still in the body:\n%s", out)
	}
	if !strings.Contains(out, `alt="Pack 47"`) || !strings.Contains(out, "<p>hi</p>") {
		t.Errorf("hosting disturbed the rest of the body:\n%s", out)
	}
	if string(gotData) != string(onePixelGIF) {
		t.Errorf("store got %d bytes, want the %d decoded ones", len(gotData), len(onePixelGIF))
	}
	if gotType != "image/gif" {
		t.Errorf("declared type = %q, want image/gif", gotType)
	}
}

// TestDecliningLeavesTheImageEmbedded is the failure mode that must stay
// harmless. Storage being down is not a reason to lose the picture.
func TestDecliningLeavesTheImageEmbedded(t *testing.T) {
	uri := gifDataURI()
	out := SanitizeHostingImages(`<img src="`+uri+`">`, func([]byte, string) string { return "" })
	if !strings.Contains(out, uri) {
		t.Errorf("a declined image was not left embedded:\n%s", out)
	}
}

// TestSanitizeAloneHostsNothing keeps the two entry points distinct — a
// caller that did not ask for hosting must not get it.
func TestSanitizeAloneHostsNothing(t *testing.T) {
	uri := gifDataURI()
	if out := Sanitize(`<img src="` + uri + `">`); !strings.Contains(out, uri) {
		t.Errorf("plain Sanitize altered an embedded image:\n%s", out)
	}
}

// TestHostingOnlySeesImagesTheSanitizerAccepted is the ordering that
// keeps this from becoming a hole: the store is offered a src only after
// that src has passed the same checks it would have to pass anyway. An
// SVG or a text/html payload never reaches storage.
func TestHostingOnlySeesImagesTheSanitizerAccepted(t *testing.T) {
	rejected := []string{
		`<img src="data:image/svg+xml;base64,PHN2Zz48c2NyaXB0PmFsZXJ0KDEpPC9zY3JpcHQ+PC9zdmc+">`,
		`<img src="data:text/html;base64,PGI+eDwvYj4=">`,
		`<img src="data:application/javascript;base64,YWxlcnQoMSk=">`,
		`<img src="data:image/png,<script>alert(1)</script>">`,
		// href is not src, and must never be handed over.
		`<a href="data:image/gif;base64,` + base64.StdEncoding.EncodeToString(onePixelGIF) + `">x</a>`,
	}
	for _, in := range rejected {
		called := false
		SanitizeHostingImages(in, func([]byte, string) string {
			called = true
			return "https://pack47.example.org/files/abc/download"
		})
		if called {
			t.Errorf("the store was offered something the sanitizer rejects: %q", in)
		}
	}
}

// TestHostedURLIsCheckedLikeAnyOther guards the callback boundary. The
// store is code, and code has bugs; a URL coming back from one must not
// be a way around the checks every other URL in a body goes through.
func TestHostedURLIsCheckedLikeAnyOther(t *testing.T) {
	uri := gifDataURI()
	for _, bad := range []string{"javascript:alert(1)", "vbscript:msgbox(1)", "data:text/html,<b>x</b>"} {
		out := SanitizeHostingImages(`<img src="`+uri+`">`, func([]byte, string) string { return bad })
		if strings.Contains(strings.ToLower(out), strings.ToLower(bad)) {
			t.Errorf("an unsafe URL from the store was emitted: %q\n%s", bad, out)
		}
		if !strings.Contains(out, uri) {
			t.Errorf("rejecting the store's URL should leave the image embedded, got:\n%s", out)
		}
	}
}

// TestHostingHandlesExporterFormatting covers the two shapes a real
// exported file uses that a strict decoder would refuse: base64 wrapped
// across lines, and base64 with its "=" padding left off.
func TestHostingHandlesExporterFormatting(t *testing.T) {
	padded := base64.StdEncoding.EncodeToString(onePixelGIF)
	unpadded := strings.TrimRight(padded, "=")
	wrapped := padded[:20] + "\n        " + padded[20:]

	for name, payload := range map[string]string{"unpadded": unpadded, "wrapped": wrapped} {
		var got []byte
		SanitizeHostingImages(`<img src="data:image/gif;base64,`+payload+`">`,
			func(data []byte, _ string) string {
				got = data
				return "https://pack47.example.org/files/abc/download"
			})
		if string(got) != string(onePixelGIF) {
			t.Errorf("%s: store got %d bytes, want %d", name, len(got), len(onePixelGIF))
		}
	}
}

// TestEveryEmbeddedImageIsOffered — a template with a logo, a divider
// and a footer badge must not have only its first image hosted.
func TestEveryEmbeddedImageIsOffered(t *testing.T) {
	uri := gifDataURI()
	body := `<img src="` + uri + `"><table><tr><td><img src="` + uri + `"></td></tr></table><img src="` + uri + `">`

	calls := 0
	out := SanitizeHostingImages(body, func([]byte, string) string {
		calls++
		return "https://pack47.example.org/files/abc/download"
	})
	if calls != 3 {
		t.Errorf("store called %d times, want 3 (one per embedded image, nested ones included)", calls)
	}
	if strings.Contains(out, "data:image") {
		t.Errorf("an embedded image survived:\n%s", out)
	}
}
