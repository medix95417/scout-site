package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSniffContentType_IgnoresAttackerDeclaredType is the regression test
// for the stored-XSS hole this file's logic closes: an upload used to be
// stored under whatever content type its multipart part header claimed,
// and that value was echoed back on download from this app's own origin.
// A file named ".png" declaring "text/html" therefore executed as script
// for whoever opened it.
func TestSniffContentType_IgnoresAttackerDeclaredType(t *testing.T) {
	pngHeader := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR")
	htmlBytes := []byte("<html><script>alert(document.domain)</script></html>")

	cases := []struct {
		name     string
		data     []byte
		declared string
		want     string
	}{
		{"real png claiming html", pngHeader, "text/html", "image/png"},
		{"real png claiming svg", pngHeader, "image/svg+xml", "image/png"},
		{"actual html claiming png", htmlBytes, "image/png", "text/html"},
		{"html with no declared type", htmlBytes, "", "text/html"},
		{"gif claiming javascript", []byte("GIF89a....."), "application/javascript", "image/gif"},
		{"pdf", []byte("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n"), "application/pdf", "application/pdf"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sniffContentType(c.data, c.declared); got != c.want {
				t.Errorf("sniffContentType(%q declared) = %q, want %q", c.declared, got, c.want)
			}
		})
	}
}

// TestSniffContentType_DeclaredTypeCannotUpgradeToScriptable covers the
// one place a declared type is still consulted — when the sniffer lands
// on a generic type it can't refine (a .docx is just a ZIP). That path
// must never accept a scriptable or inline-renderable claim, or it would
// reopen the same hole through the back door.
func TestSniffContentType_DeclaredTypeCannotUpgradeToScriptable(t *testing.T) {
	zipBytes := []byte("PK\x03\x04\x14\x00\x00\x00\x08\x00")

	if got := sniffContentType(zipBytes, "application/vnd.openxmlformats-officedocument.wordprocessingml.document"); got != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" {
		t.Errorf("an inert, specific claim should refine a generic sniff, got %q", got)
	}
	for _, declared := range []string{"text/html", "image/svg+xml", "text/javascript", "image/png", "application/pdf"} {
		got := sniffContentType(zipBytes, declared)
		if got == declared {
			t.Errorf("declared %q was accepted for generic bytes — it must not be", declared)
		}
	}
}

// TestWriteUserFileHeaders checks the serve-time half. This runs against
// the stored content type rather than the bytes, which is what protects
// rows written before sniffing existed and still carry whatever their
// uploader claimed.
func TestWriteUserFileHeaders(t *testing.T) {
	cases := []struct {
		name            string
		contentType     string
		filename        string
		wantType        string
		wantDisposition string
	}{
		{"photo renders inline", "image/jpeg", "campout.jpg", "image/jpeg", "inline"},
		{"pdf renders inline", "application/pdf", "permission-slip.pdf", "application/pdf", "inline"},
		{"stored html is forced to download", "text/html", "evil.png", "text/html", "attachment"},
		{"stored svg is forced to download", "image/svg+xml", "logo.svg", "image/svg+xml", "attachment"},
		{"charset parameter is stripped", "text/html; charset=utf-8", "x.html", "text/html", "attachment"},
		{"unknown type downloads", "application/zip", "roster.zip", "application/zip", "attachment"},
		{"empty type downloads", "", "mystery", "application/octet-stream", "attachment"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeUserFileHeaders(rec, c.contentType, c.filename)

			if got := rec.Header().Get("Content-Type"); got != c.wantType {
				t.Errorf("Content-Type = %q, want %q", got, c.wantType)
			}
			disp := rec.Header().Get("Content-Disposition")
			if !strings.HasPrefix(disp, c.wantDisposition) {
				t.Errorf("Content-Disposition = %q, want it to start with %q", disp, c.wantDisposition)
			}
			// Every response carrying user bytes is sandboxed, so even an
			// inline-allowlisted format that turns out to be a polyglot has
			// no script, no plugins and no same-origin access.
			if got := rec.Header().Get("Content-Security-Policy"); got != "sandbox" {
				t.Errorf("Content-Security-Policy = %q, want %q", got, "sandbox")
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
		})
	}
}

// TestSafeFilename_CannotEscapeTheHeader guards the Content-Disposition
// filename, which comes from the uploader.
func TestSafeFilename_CannotEscapeTheHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	writeUserFileHeaders(rec, "image/png", "evil\r\nX-Injected: yes\".png")

	// The residual text staying inside the quoted filename is fine — what
	// must not survive is anything that ends the header line or closes the
	// quoted string early.
	disp := rec.Header().Get("Content-Disposition")
	for _, bad := range []string{"\r", "\n"} {
		if strings.Contains(disp, bad) {
			t.Errorf("Content-Disposition %q still contains %q", disp, bad)
		}
	}
	if strings.Count(disp, `"`) != 2 {
		t.Errorf("Content-Disposition %q should have exactly one quoted filename", disp)
	}
	if rec.Header().Get("X-Injected") != "" {
		t.Error("a header was injected through the filename")
	}
}
