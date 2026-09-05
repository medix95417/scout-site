package newsletter

// Moving an embedded image out of the body it arrived in.
//
// A design tool asked to export a standalone HTML file inlines every
// image as a base64 data: URI, because a standalone file has nowhere
// else to put them. That is a sensible default for a file on disk and a
// poor one for an email:
//
//   - Gmail and Outlook refuse to render data: URI images at all. The
//     message arrives with a hole where the logo was, and nothing in the
//     composer warns anyone, because it looked right in the preview.
//   - The image is carried per recipient. A 13 KB logo sent to 200
//     families is 13 KB copied 200 times, and base64 adds a third again.
//   - Gmail clips a message past roughly 102 KB behind a "View entire
//     message" link, and an inlined photo or two is enough to cross that
//     — so the end of the email, unsubscribe footer included, quietly
//     stops being visible.
//
// Pointing at a hosted copy fixes all three, and is what a mailing tool
// does for you. See internal/web/inline_images.go for the half that
// actually stores the bytes.

import (
	"encoding/base64"
	"strings"
)

// ImageStore is handed the decoded bytes of one image found embedded in
// a body, along with the media type the document declared for it, and
// returns the URL to point at instead.
//
// Returning "" means "leave this one where it is" — the sole failure
// signal, deliberately, because there is nothing useful a caller here
// could do with an error. Hosting an image is an improvement to a body
// that already works; a body that keeps its embedded copy still sends,
// still renders in most clients, and can be fixed by hand. Failing the
// leader's save instead would be trading a cosmetic problem in some mail
// clients for a lost draft in all of them.
//
// declaredType comes from the document and is therefore the author's
// claim, not a fact. An implementation that stores these bytes and later
// serves them back MUST re-derive the type from the bytes themselves —
// see internal/web/file_serving.go, which explains what goes wrong
// otherwise.
type ImageStore func(data []byte, declaredType string) string

// hosted returns the URL to emit for a src value: the stored one if this
// is an embedded image and the store took it, and the original value in
// every other case.
func (s sanitizer) hosted(val string) string {
	if s.store == nil {
		return val
	}

	// Recognising the image here rather than trusting the caller to have
	// done it is what keeps hosting from ever widening what the sanitizer
	// accepts: this runs imageDataURI, the same pattern safeImageDataURI
	// checks against, over a value cleaned the same way. An attribute
	// that pattern does not match is handed back untouched and never
	// reaches a store — so a src the sanitizer would reject cannot be
	// laundered into a hosted URL, whichever order the two run in.
	cleaned := stripURLNoise(val)
	m := imageDataURI.FindStringSubmatch(cleaned)
	if m == nil {
		return val
	}
	comma := strings.IndexByte(cleaned, ',')
	data, err := decodeImagePayload(cleaned[comma+1:])
	if err != nil {
		return val
	}

	url := s.store(data, "image/"+strings.ToLower(m[1]))
	if url == "" {
		return val
	}
	// The store is a callback, so its return value gets the same check
	// any other URL in a body would: a bug there must not be a way to
	// put a javascript: src into an email.
	if !safeURL(url) {
		return val
	}
	return url
}

// decodeImagePayload decodes the base64 half of a data: URI, tolerating
// the padding an exporter left off — a "==" tail is arithmetic that a
// decoder can redo, and rejecting the image over it would help nobody.
func decodeImagePayload(payload string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(payload)
	if err == nil {
		return data, nil
	}
	return base64.RawStdEncoding.DecodeString(payload)
}
