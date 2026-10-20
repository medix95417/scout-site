package web

import (
	"regexp"
	"strings"
	"testing"

	"github.com/47-yonkers/scout-site/internal/content"
)

// What a photo page costs the server is decided in its markup, and a revert
// of any of this is one deleted attribute that no other test would notice.
//
// Every photo in a carousel is a request to this server, every video another,
// and /gallery shows every album's carousel at once. On a 1 vCPU box that was
// the most expensive page on the site: 112 thumbnails and 4 video files for 14
// albums of 8 photos, measured in a browser, and growing with every photo ever
// uploaded.

func mixedPhotos() []content.GalleryPhoto {
	return []content.GalleryPhoto{
		{URL: "/files/a/download", Caption: "A photo"},
		{URL: "/files/b/download", Caption: "Another photo"},
		{URL: "/files/c/download", Caption: "A video", IsVideo: true},
	}
}

func renderGalleryDetailWith(t *testing.T, photos []content.GalleryPhoto) string {
	t.Helper()
	return renderPage(t, "gallery-detail.html", galleryDetailData{
		baseData: testBase("Summer Camp"),
		Title:    "Summer Camp",
		PostedOn: "15 Jul 2026",
		Photos:   photos,
	})
}

var (
	imgTag   = regexp.MustCompile(`<img [^>]*>`)
	videoTag = regexp.MustCompile(`<video [^>]*>`)
)

func TestCarouselPhotosLoadLazily(t *testing.T) {
	out := renderGalleryDetailWith(t, mixedPhotos())

	// The slides, and the thumbnail strip under them: both are one request
	// per photo.
	var imgs []string
	for _, slide := range carouselSlides(out) {
		imgs = append(imgs, imgTag.FindAllString(slide, -1)...)
	}
	strip := out[strings.Index(out, "data-carousel-thumb"):]
	imgs = append(imgs, imgTag.FindAllString(strip, -1)...)

	if len(imgs) < 4 { // two photos in the slides, two in the strip
		t.Fatalf("found only %d images to check", len(imgs))
	}
	for _, tag := range imgs {
		if strings.Contains(tag, "data-lightbox-image") {
			continue // the lightbox's own image has no source until it is opened
		}
		if !strings.Contains(tag, `loading="lazy"`) {
			t.Errorf("a photo loads as soon as the page does, whether or not it is ever shown:\n%s", tag)
		}
	}
}

func TestCarouselVideosFetchNothingUntilPlayed(t *testing.T) {
	out := renderGalleryDetailWith(t, mixedPhotos())

	tags := videoTag.FindAllString(out, -1)
	if len(tags) != 2 { // one in the slides, one in the strip
		t.Fatalf("found %d video elements, want 2", len(tags))
	}
	for _, tag := range tags {
		if !strings.Contains(tag, `preload="none"`) {
			t.Errorf("a video asks this server for part of its file on every page view:\n%s", tag)
		}
		if strings.Contains(tag, `preload="metadata"`) || strings.Contains(tag, `preload="auto"`) {
			t.Errorf("a video preloads:\n%s", tag)
		}
	}
}

// A video that has not loaded anything is an empty box; it needs a backdrop
// or it reads as a page that failed to load.
func TestAVideoThatHasNotLoadedHasABackdrop(t *testing.T) {
	for _, tag := range videoTag.FindAllString(renderGalleryDetailWith(t, mixedPhotos()), -1) {
		if !strings.Contains(tag, "bg-black") && !strings.Contains(tag, "bg-gray-900") {
			t.Errorf("an unloaded video has no backdrop:\n%s", tag)
		}
	}
}

// The script is what makes lazy loading safe in a scroller: without it the
// slide a step lands on is blank for a moment, because a lazy image in an
// overflow container is not loaded until it is itself visible.
func TestTheCarouselScriptWarmsNeighboursAndStopsOffScreen(t *testing.T) {
	out := renderGalleryDetailWith(t, mixedPhotos())
	for _, want := range []string{
		`img.loading = 'eager'`,           // neighbouring slides are told to load ahead of being shown
		`new IntersectionObserver`,        // a carousel knows whether it is on screen
		`if (onScreen) advance()`,         // and only turns over while it is
		`addEventListener('scroll', warm`, // warming follows the visitor, not a timer
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the carousel script no longer contains %q", want)
		}
	}
}

// --- /gallery's pager ----------------------------------------------------

type galleryListData struct {
	baseData
	Items []publicPostView
	Pager pager
}

func renderGalleryList(t *testing.T, total int, page string) string {
	t.Helper()
	return renderPage(t, "gallery.html", galleryListData{
		baseData: testBase("Photos"),
		Items:    []publicPostView{{ID: "g1", Title: "Camp", PostedOn: "15 Jul 2026", Photos: galleryPhotos()}},
		Pager:    newPager(total, galleryPageSize, page, "/gallery"),
	})
}

func TestAGalleryThatFitsOnOnePageShowsNoPager(t *testing.T) {
	out := renderGalleryList(t, galleryPageSize, "")
	if strings.Contains(out, "Pages of photo albums") || strings.Contains(out, "Older albums") {
		t.Error("a single page of albums shows a pager")
	}
}

func TestTheFirstPageLinksOnlyForward(t *testing.T) {
	out := renderGalleryList(t, galleryPageSize*3, "")
	for _, want := range []string{"Page 1 of 3", `href="/gallery?page=2"`, `rel="next"`, "Older albums"} {
		if !strings.Contains(out, want) {
			t.Errorf("first page is missing %q", want)
		}
	}
	if strings.Contains(out, "Newer albums") || strings.Contains(out, `rel="prev"`) {
		t.Error("the first page links back to a page before it")
	}
}

func TestAMiddlePageLinksBothWays(t *testing.T) {
	out := renderGalleryList(t, galleryPageSize*3, "2")
	for _, want := range []string{"Page 2 of 3", `href="/gallery"`, `rel="prev"`, `href="/gallery?page=3"`, `rel="next"`} {
		if !strings.Contains(out, want) {
			t.Errorf("middle page is missing %q", want)
		}
	}
}

func TestTheLastPageLinksOnlyBack(t *testing.T) {
	out := renderGalleryList(t, galleryPageSize*3, "3")
	if !strings.Contains(out, "Page 3 of 3") || !strings.Contains(out, "Newer albums") {
		t.Error("last page is missing its position or the link back")
	}
	if strings.Contains(out, "Older albums") || strings.Contains(out, `rel="next"`) {
		t.Error("the last page links forward to nothing")
	}
}
