package web

import (
	"strings"
	"testing"

	"github.com/47-yonkers/scout-site/internal/thumbnail"
)

// The three heroes — the homepage's, a page banner, a den/patrol page's
// — used to point straight at the original uploaded file, so every
// visitor to the front page downloaded a full camera photo to fill a
// band a few hundred pixels tall. They now point at a resized variant.

func TestBannerURL(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			"one of our own files becomes its banner variant",
			"/files/abc123/download",
			"/files/abc123/banner",
		},
		{
			// An external URL a leader pasted in is somebody else's
			// file; there is nothing here to resize.
			"an external URL passes through",
			"https://example.com/photo.jpg",
			"https://example.com/photo.jpg",
		},
		{"a stock photo path passes through", "/static/stock/campfire.jpg", "/static/stock/campfire.jpg"},
		{"empty stays empty", "", ""},
		{"a files URL that isn't a download passes through", "/files/abc123/thumb", "/files/abc123/thumb"},
		{"no id", "/files//download", "/files//download"},
		{"an id with a slash in it is not an id", "/files/a/b/download", "/files/a/b/download"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := bannerURL(c.in); got != c.want {
				t.Errorf("bannerURL(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// The two variants must not collide: a hero asking for a banner and a
// picker asking for a thumbnail have to get different URLs, or one size
// would be served where the other was wanted.
func TestThumbAndBannerAreDifferentURLs(t *testing.T) {
	const src = "/files/abc123/download"
	thumb, banner := thumbURL(src), bannerURL(src)
	if thumb == banner {
		t.Fatalf("both variants resolve to %q", thumb)
	}
	if !strings.HasSuffix(thumb, "/thumb") || !strings.HasSuffix(banner, "/banner") {
		t.Errorf("unexpected variant URLs: thumb %q, banner %q", thumb, banner)
	}
}

// Both cached variants must live at different storage keys for the same
// reason, or generating one would overwrite the other.
func TestVariantCacheKeysDiffer(t *testing.T) {
	if thumbStorageSuffix == bannerStorageSuffix {
		t.Fatalf("both variants cache under %q — one would overwrite the other", thumbStorageSuffix)
	}
	for _, suffix := range []string{thumbStorageSuffix, bannerStorageSuffix} {
		if !strings.HasPrefix(suffix, ".") || !strings.HasSuffix(suffix, ".jpg") {
			t.Errorf("suffix %q doesn't look like a derived key for a JPEG", suffix)
		}
	}
}

func TestHomeHeroUsesTheResizedBanner(t *testing.T) {
	data := homePage()
	data.HeroImageURL = "/files/hero123/download"
	out := renderPage(t, "home.html", data)

	if !strings.Contains(out, "/files/hero123/banner") {
		t.Error("the homepage hero isn't pointed at the resized banner")
	}
	if strings.Contains(out, "/files/hero123/download") {
		t.Error("the homepage hero still serves the full-size original")
	}
}

// An external hero URL still has to work — a leader who pasted a link to
// a photo hosted elsewhere gets it rendered, unrewritten.
func TestExternalHeroStillRenders(t *testing.T) {
	data := homePage()
	data.HeroImageURL = "https://example.com/campfire.jpg"
	out := renderPage(t, "home.html", data)

	if !strings.Contains(out, "https://example.com/campfire.jpg") {
		t.Error("an externally hosted hero photo went missing")
	}
}

// The route has to exist, or every hero is a broken image.
func TestBannerRouteIsRegistered(t *testing.T) {
	src, err := readSource("web.go")
	if err != nil {
		t.Fatalf("reading web.go: %v", err)
	}
	if !strings.Contains(src, `mux.HandleFunc("GET /files/{id}/banner", h.FileBanner)`) {
		t.Error("no route serves the banner variant the templates now ask for")
	}
}

// The banner handler must run the same access check as the thumbnail and
// download ones — it serves the same file's bytes, just smaller. Both
// share one implementation precisely so this cannot drift; the guard is
// that they still do.
func TestBothVariantsShareOneImplementation(t *testing.T) {
	src, err := readSource("files.go")
	if err != nil {
		t.Fatalf("reading files.go: %v", err)
	}
	for _, fn := range []string{"FileThumbnail", "FileBanner"} {
		body, ok := functionBody(src, fn)
		if !ok {
			t.Fatalf("%s not found", fn)
		}
		if !strings.Contains(body, "h.serveImageVariant(") {
			t.Errorf("%s doesn't delegate to serveImageVariant, so its access check can drift from the other's", fn)
		}
	}
	shared, ok := functionBody(src, "serveImageVariant")
	if !ok {
		t.Fatal("serveImageVariant not found")
	}
	if !strings.Contains(shared, "requiresLoginToDownload(") {
		t.Error("serveImageVariant doesn't check whether this file needs a login")
	}
}

// The sizes the handlers ask for are the two the thumbnail package
// defines, rather than numbers written out again here.
func TestHandlersUseThePackagesSizes(t *testing.T) {
	src, err := readSource("files.go")
	if err != nil {
		t.Fatalf("reading files.go: %v", err)
	}
	if !strings.Contains(src, "thumbnail.BannerDimension") {
		t.Error("the banner handler doesn't use thumbnail.BannerDimension")
	}
	if !strings.Contains(src, "thumbnail.MaxDimension") {
		t.Error("the thumbnail handler doesn't use thumbnail.MaxDimension")
	}
	if thumbnail.BannerDimension <= thumbnail.MaxDimension {
		t.Error("the banner size isn't larger than the preview size")
	}
}
