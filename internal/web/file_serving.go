package web

// This file is the trust boundary around user-uploaded bytes.
//
// A file in the library was uploaded by a leader and is served back from
// this app's own origin, which makes it same-origin content as far as a
// browser is concerned. That means the Content-Type it's served under
// decides whether it's a photo or a script running as whoever opened it.
// Two rules follow, and both are applied here rather than at any call
// site, so a new route that serves stored bytes can't quietly skip them:
//
//  1. Never believe the uploader about what a file is. The multipart part
//     header a client sends is attacker-chosen — a ".png" can declare
//     "text/html" — so sniffContentType re-derives the type from the
//     actual bytes instead (see FileUpload).
//  2. Never render anything inline unless it's a format that can't carry
//     script. writeUserFileHeaders serves anything outside a small
//     allowlist as a download, and stamps every response with a sandbox
//     CSP so even an allowlisted format that turns out to be a polyglot
//     has nothing to execute with.
//
// Rule 2 is deliberately applied at serve time, not just at upload time:
// rows written before this existed still carry whatever content type
// their uploader claimed, and re-deriving on the way out is what makes
// those safe without a data migration.

import (
	"mime"
	"net/http"
	"strings"
)

// inlineRenderableTypes are the formats a browser may render in place.
// Everything here is a container that either can't hold script at all, or
// (for PDF) is handled by a viewer that doesn't grant it page context.
//
// image/svg+xml is deliberately absent: an SVG is XML that can carry
// <script>, and serving one inline from this origin would be the very
// hole this file exists to close. Sniffing wouldn't produce it anyway
// (http.DetectContentType reports SVG as text/xml), but leaving it out
// explicitly means adding it back has to be a deliberate act.
var inlineRenderableTypes = map[string]bool{
	"image/jpeg":      true,
	"image/png":       true,
	"image/gif":       true,
	"image/webp":      true,
	"image/bmp":       true,
	"image/avif":      true,
	"image/tiff":      true,
	"application/pdf": true,
	"video/mp4":       true,
	"video/webm":      true,
	"audio/mpeg":      true,
	"audio/ogg":       true,
	"audio/wav":       true,
}

// sniffContentType decides the content type to STORE for an upload, from
// the bytes themselves. declared is the uploader's claim, used only as a
// tiebreaker for formats Go's sniffer can't tell apart, and never when it
// would upgrade an inert type into a renderable one.
//
// http.DetectContentType reads at most the first 512 bytes and always
// returns something, falling back to application/octet-stream — so this
// never fails, it just gets less specific.
func sniffContentType(data []byte, declared string) string {
	sniffed := normalizeMediaType(http.DetectContentType(data))

	// The sniffer can't distinguish some real formats from their generic
	// container: .docx/.xlsx/.pptx all sniff as application/zip, .csv as
	// text/plain, and plenty of formats as raw octets. Where it lands on
	// one of those deliberately inert types, an uploader's more specific
	// claim is worth keeping so the library shows "Word document" rather
	// than "ZIP archive" — but only if that claim is itself inert, so
	// this can never be used to talk the server into serving text/html.
	switch sniffed {
	case "application/octet-stream", "text/plain", "application/zip":
		if d := normalizeMediaType(declared); d != "" && !inlineRenderableTypes[d] && !isScriptable(d) {
			return d
		}
	}
	return sniffed
}

// isScriptable reports whether a media type is one a browser would treat
// as executable/markup content in page context.
func isScriptable(mediaType string) bool {
	switch mediaType {
	case "text/html", "application/xhtml+xml", "image/svg+xml",
		"application/xml", "text/xml", "application/javascript", "text/javascript":
		return true
	}
	return false
}

// normalizeMediaType strips any parameters ("; charset=utf-8") and
// lowercases what's left, so comparisons against the allowlist are exact.
// Returns "" for anything unparseable.
func normalizeMediaType(ct string) string {
	ct = strings.TrimSpace(ct)
	if ct == "" {
		return ""
	}
	parsed, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed)
}

// writeUserFileHeaders sets every response header for serving stored,
// user-uploaded bytes. The single place that decides inline-vs-download,
// so both the download and thumbnail routes get the same treatment.
func writeUserFileHeaders(w http.ResponseWriter, contentType, filename string) {
	mediaType := normalizeMediaType(contentType)
	inline := inlineRenderableTypes[mediaType]
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}

	w.Header().Set("Content-Type", mediaType)

	disposition := "attachment"
	if inline {
		disposition = "inline"
	}
	// FormatMediaType quotes the filename and RFC 2231-encodes it if it
	// isn't plain ASCII, which is what keeps a crafted name from escaping
	// the header.
	w.Header().Set("Content-Disposition",
		mime.FormatMediaType(disposition, map[string]string{"filename": safeFilename(filename)}))

	// Overrides the global policy (see cmd/server.securityHeaders, which
	// runs before this handler) for this response only. A bare "sandbox"
	// puts the response in a unique opaque origin with scripts, plugins,
	// forms and same-origin access all off — so even if a file slipped
	// through as a renderable type, there's nothing for it to run or
	// reach. Nothing legitimate in the library needs any of that: these
	// are photos and PDFs.
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

// safeFilename strips anything that would break out of the quoted string
// in a Content-Disposition header, or inject a second header line.
func safeFilename(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' {
			return -1
		}
		return r
	}, name)
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return "download"
	}
	return cleaned
}
