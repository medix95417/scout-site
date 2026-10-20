package web

import (
	"regexp"
	"strings"
	"testing"

	"github.com/47-yonkers/scout-site/internal/content"
	"github.com/47-yonkers/scout-site/internal/files"
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

// --- posters ---------------------------------------------------------------

// A video that has not been played is a dark box unless it has a picture to
// show. Pointing at one is the template's whole part in that; the test is that
// it does, only for videos that can have one, on both places a video appears.
func TestAVideoPointsAtItsPoster(t *testing.T) {
	out := renderGalleryDetailWith(t, []content.GalleryPhoto{
		{URL: "/files/lib/download", Caption: "Ours", IsVideo: true},
		{URL: "https://example.org/clip.mp4", Caption: "Somebody else's", IsVideo: true},
	})

	var ours, elsewhere []string
	for _, tag := range videoTag.FindAllString(out, -1) {
		if strings.Contains(tag, "/files/lib/download") {
			ours = append(ours, tag)
		} else {
			elsewhere = append(elsewhere, tag)
		}
	}
	if len(ours) != 2 || len(elsewhere) != 2 { // each video is in the slides and in the strip
		t.Fatalf("found %d of ours and %d of the other, want 2 and 2", len(ours), len(elsewhere))
	}
	for _, tag := range ours {
		if !strings.Contains(tag, `poster="/files/lib/poster"`) {
			t.Errorf("a library video doesn't point at its poster:\n%s", tag)
		}
	}
	for _, tag := range elsewhere {
		if strings.Contains(tag, "poster=") {
			t.Errorf("a video from elsewhere points at a poster this site can't have made:\n%s", tag)
		}
	}
}

func filesPageWithVideos(canManage bool, backlog []files.VideoPoster) fileLibraryData {
	return fileLibraryData{
		baseData:          testBase("Files"),
		SelectedEventIDs:  map[string]bool{},
		CanManage:         canManage,
		StorageConfigured: true,
		PosterBacklog:     backlog,
		EventGroups: []eventFileGroupView{{
			EventTitle: "Not linked to an event",
			Files: []fileRow{
				{File: files.File{ID: "has-one", Filename: "a.mp4", ContentType: "video/mp4", Category: files.CategoryEventPhoto},
					SizeDisplay: "1 MB", LinkedEventIDs: map[string]bool{}, HasPoster: true},
				{File: files.File{ID: "has-none", Filename: "b.mp4", ContentType: "video/mp4", Category: files.CategoryEventPhoto},
					SizeDisplay: "1 MB", LinkedEventIDs: map[string]bool{}},
			},
		}},
	}
}

func TestTheFilesPageShowsAPosterOnlyWhereThereIsOne(t *testing.T) {
	out := renderPage(t, "files.html", filesPageWithVideos(true, nil))
	by := map[string]string{}
	for _, tag := range videoTag.FindAllString(out, -1) {
		switch {
		case strings.Contains(tag, `data-file-video="has-one"`):
			by["has-one"] = tag
		case strings.Contains(tag, `data-file-video="has-none"`):
			by["has-none"] = tag
		}
	}
	if !strings.Contains(by["has-one"], `poster="/files/has-one/poster"`) {
		t.Errorf("a video with a poster doesn't show it:\n%s", by["has-one"])
	}
	if by["has-none"] == "" || strings.Contains(by["has-none"], "poster=") {
		t.Errorf("a video without one points at a poster that is not there:\n%s", by["has-none"])
	}
}

// The panel that makes the missing posters is for a leader, and only while
// there is something for it to do. It is hidden until its script runs, so it
// is never a button that does nothing.
func TestThePosterPanelIsForALeaderWithVideosWaiting(t *testing.T) {
	waiting := []files.VideoPoster{{ID: "has-none", Label: "b.mp4"}, {ID: "also-none", Label: "c.mp4"}}

	out := renderPage(t, "files.html", filesPageWithVideos(true, waiting))
	if !strings.Contains(out, "data-poster-backfill") {
		t.Fatal("a leader with videos waiting is not offered the panel")
	}
	for _, want := range []string{
		`data-ids="has-none,also-none"`, // the list the script works through
		`data-csrf=`,                    // its POSTs need the token
		`hidden data-poster-backfill`,   // hidden until the script runs
		"2</span> of your videos don",   // the count, in the plural
		"window.ScoutPoster",            // the capture script is on the page
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the panel or its script is missing %q", want)
		}
	}

	for name, data := range map[string]fileLibraryData{
		"a leader with nothing waiting":     filesPageWithVideos(true, nil),
		"a member who cannot edit the site": filesPageWithVideos(false, waiting),
	} {
		if strings.Contains(renderPage(t, "files.html", data), "data-poster-backfill") {
			t.Errorf("%s is shown the panel", name)
		}
	}
}

func TestTheAlbumEditorLoadsTheCaptureScriptBeforeTheUploader(t *testing.T) {
	src, err := readSource("templates/admin-content-form.html")
	if err != nil {
		t.Fatal(err)
	}
	poster := strings.Index(src, `template "videoPosterScript"`)
	uploader := strings.Index(src, `template "galleryUploaderScript"`)
	if poster < 0 || uploader < 0 || poster > uploader {
		t.Errorf("the capture script must be on the page before the uploader that calls it (poster at %d, uploader at %d)", poster, uploader)
	}
}

// The capture reads the video from this site's own address, never from a
// blob: URL of the file just chosen. The site's Content-Security-Policy
// (default-src 'self') refuses blob: as media, so a capture from one fails in
// every browser — silently, because a poster that can't be made is only ever a
// dark box — and nothing but a real browser notices. This is the cheap
// reminder of why the page does it the longer way.
func TestPosterCaptureNeverReadsABlobURL(t *testing.T) {
	for _, file := range []string{"templates/_video-poster.html", "templates/_gallery-upload.html", "templates/files.html"} {
		src, err := readSource(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(src, "createObjectURL") {
			t.Errorf("%s makes a blob: URL, which the site's CSP will not let a <video> load", file)
		}
	}
	src, _ := readSource("templates/_gallery-upload.html")
	if !strings.Contains(src, `'/files/' + encodeURIComponent(fileId) + '/download'`) {
		t.Error("the album uploader no longer captures from the uploaded copy at /files/{id}/download")
	}
}
