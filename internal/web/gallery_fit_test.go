package web

import (
	"strings"
	"testing"

	"github.com/47-yonkers/scout-site/internal/content"
)

// A photo page that crops is a bug you cannot see in a diff.
//
// The gallery carousel sizes every slide to a fixed height. With
// object-cover that height CROPS: a 900x1600 portrait photo in the old
// h-80 box showed 20% of itself, measured in a browser — a band across
// the middle, with nothing to tell the visitor there was more, short of
// clicking through to the lightbox. Which is exactly how it was
// reported: "displaying half of the photo... unless you click on the
// photo directly."
//
// The class names below are load-bearing, and swapping one back is a
// one-word edit that no other test would notice.

func galleryPhotos() []content.GalleryPhoto {
	return []content.GalleryPhoto{
		{URL: "/files/a/download", Caption: "Tall one"},
		{URL: "/files/b/download", Caption: "Wide one"},
	}
}

type galleryDetailData struct {
	baseData
	Title    string
	PostedOn string
	Photos   []content.GalleryPhoto
}

func renderGalleryDetail(t *testing.T) string {
	t.Helper()
	return renderPage(t, "gallery-detail.html", galleryDetailData{
		baseData: testBase("Summer Camp"),
		Title:    "Summer Camp",
		PostedOn: "15 Jul 2026",
		Photos:   galleryPhotos(),
	})
}

func TestGalleryDetailShowsWholePhotos(t *testing.T) {
	out := renderGalleryDetail(t)

	if !strings.Contains(out, "object-contain") {
		t.Error("the gallery carousel does not use object-contain, so photos are cropped to the box")
	}
	// The carousel SLIDES specifically — not the lightbox, which has
	// always used object-contain and is what made the bug survivable,
	// and not the thumbnail strip below, where cropping to uniform
	// squares is right.
	slides := carouselSlides(out)
	if len(slides) != 2 {
		t.Fatalf("found %d carousel slides, want 2", len(slides))
	}
	for _, slide := range slides {
		if !strings.Contains(slide, "object-contain") {
			t.Errorf("a carousel slide still crops:\n%s", slide)
		}
		if strings.Contains(slide, "object-cover") {
			t.Errorf("a carousel slide still has object-cover:\n%s", slide)
		}
	}
}

// carouselSlides pulls out the full-width figures the carousel scrolls
// between, which is the surface the complaint was about.
func carouselSlides(html string) []string {
	var out []string
	const marker = `<figure class="snap-start`
	for rest := html; ; {
		i := strings.Index(rest, marker)
		if i == -1 {
			return out
		}
		rest = rest[i:]
		end := strings.Index(rest, "</figure>")
		if end == -1 {
			return append(out, rest)
		}
		out = append(out, rest[:end])
		rest = rest[end:]
	}
}

// TestGalleryDetailGivesPhotosRoom. Showing a portrait photo whole in a
// 320px-tall box renders it about 180px wide — technically uncropped and
// not worth looking at. The height has to grow with the fit.
func TestGalleryDetailGivesPhotosRoom(t *testing.T) {
	slides := carouselSlides(renderGalleryDetail(t))
	if len(slides) == 0 {
		t.Fatal("no carousel slides rendered")
	}
	slide := slides[0]

	if !strings.Contains(slide, "vh]") {
		t.Errorf("the carousel height does not scale with the viewport:\n%s", slide)
	}
	if !strings.Contains(slide, "min-h-80") {
		t.Errorf("no minimum height, so a short window collapses the photo:\n%s", slide)
	}
}

// TestPreviewCarouselsStillCrop. Cropping is right for a grid of cards,
// where uniform tiles are the point — so the fix must be opt-in per
// caller rather than a change to the shared partial's behaviour.
func TestPreviewCarouselsStillCrop(t *testing.T) {
	for _, page := range []struct {
		name string
		data any
	}{
		{"gallery.html", struct {
			baseData
			Items []publicPostView
		}{testBase("Photos"), []publicPostView{{ID: "g1", Title: "Camp", PostedOn: "15 Jul 2026", Photos: galleryPhotos()}}}},
	} {
		out := renderPage(t, page.name, page.data)
		if !strings.Contains(out, "object-cover") {
			t.Errorf("%s: the card grid stopped cropping; uniform tiles are deliberate there", page.name)
		}
	}
}

// TestTheLightboxStillShowsTheOriginal — the carousel shows a thumbnail
// for bandwidth; clicking must still reach the full file.
func TestTheLightboxStillShowsTheOriginal(t *testing.T) {
	out := renderGalleryDetail(t)
	if !strings.Contains(out, `href="/files/a/download" data-lightbox-open`) {
		t.Error("the photo no longer links to its original file")
	}
	// Checked on the SLIDE, not the page: the thumbnail strip below also
	// uses thumbURL, so a page-wide search for "/thumb" passes even if
	// the big carousel image switched to full-size originals — which
	// would make opening one album download every photo at full size.
	for _, slide := range carouselSlides(out) {
		if !strings.Contains(slide, "/thumb\"") {
			t.Errorf("a carousel slide is not using a thumbnail:\n%s", slide)
		}
	}
}
