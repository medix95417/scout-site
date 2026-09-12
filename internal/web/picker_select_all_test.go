package web

import (
	"strings"
	"testing"

	"github.com/47-yonkers/scout-site/internal/content"
	"github.com/47-yonkers/scout-site/internal/files"
	"github.com/47-yonkers/scout-site/internal/leaders"
)

// Building an album out of an event's photos usually means all of them.
// The "add all" control only makes sense where the picker appends to a
// list; in single-select mode there is one field and "all of them" is
// not something it can hold.

func pickerGroups() []files.EventFileGroup {
	return []files.EventFileGroup{{
		EventID: "e-camp", EventTitle: "Summer Camp",
		Files: []files.File{
			{ID: "p1", Filename: "one.jpg", ContentType: "image/jpeg", Public: true},
			{ID: "p2", Filename: "two.jpg", ContentType: "image/jpeg", Public: true},
			{ID: "p3", Filename: "three.mp4", ContentType: "video/mp4", Public: true},
		},
	}}
}

// albumForm mirrors the anonymous struct AdminGalleryNew/Edit renders
// admin-content-form.html with.
func albumForm(eventPhotos []files.EventFileGroup) any {
	return struct {
		baseData
		Kind                 contentKind
		IsEdit               bool
		Post                 content.Post
		PhotoDateInput       string
		PublicMediaGroups    []files.EventFileGroup
		PublicMediaUngrouped []files.File
		EventPhotoGroups     []files.EventFileGroup
	}{
		baseData: testBase("Photo Album"), Kind: galleryKind, IsEdit: true,
		Post:              content.Post{ID: "g1", Title: "Summer Camp", Status: "draft", Visibility: "members"},
		PublicMediaGroups: eventPhotos,
		EventPhotoGroups:  eventPhotos,
	}
}

// leaderForm mirrors AdminLeaderForm's own render data — a single-select
// picker, for contrast.
func leaderForm(groups []files.EventFileGroup) any {
	return struct {
		baseData
		IsEdit                bool
		Leader                leaders.Leader
		PublicImageGroups     []files.EventFileGroup
		PublicImagesUngrouped []files.File
	}{
		baseData: testBase("Our Leaders"), IsEdit: true,
		Leader:            leaders.Leader{ID: "l1", Name: "Sam Kowalski"},
		PublicImageGroups: groups,
	}
}

func TestAlbumPickerOffersAddAllPerEvent(t *testing.T) {
	out := renderPage(t, "admin-content-form.html", albumForm(pickerGroups()))

	if !strings.Contains(out, "<button type=\"button\" class=\"underline\" style=\"color: var(--unit-color);\" data-picker-select-all>") {
		t.Fatal("no add-all button in the album's event-photo picker")
	}
	if !strings.Contains(out, "Add all 3") {
		t.Error("the add-all control should name how many it will add")
	}
	if !strings.Contains(out, "data-picker-clear-all") {
		t.Error("there is no way to take a whole group back out again")
	}
	// "All" is scoped to the group root, so it reaches the photos still
	// folded away behind that group's nested "Show more" pages.
	if !strings.Contains(out, "data-picker-group") {
		t.Error("the group root isn't marked, so add-all has nothing to scope to")
	}
}

func TestSinglePhotoPickerHasNoAddAll(t *testing.T) {
	out := renderPage(t, "admin-leaders-form.html", leaderForm(pickerGroups()))

	// The shared script mentions the attribute by name in a selector, so
	// the button's own label is what distinguishes "this page has the
	// control" from "this page ships the code for it".
	if strings.Contains(out, "Add all ") {
		t.Error("a single-select picker offers add-all, which it cannot honour")
	}
	if strings.Contains(out, "data-picker-group-count></span>") {
		t.Error("a single-select picker renders the append-mode group counter")
	}
	// It still has to be a working picker.
	if !strings.Contains(out, "data-image-picker-option") {
		t.Error("the single-select picker lost its thumbnails")
	}
}

// Clicking a thumbnail that is already in the list takes it out again —
// that is what makes "add all, then drop the blurry ones" possible. The
// behaviour lives in the shared script, so this checks the page that
// needs it actually ships with it.
func TestAlbumPickerScriptTogglesAndDeduplicates(t *testing.T) {
	out := renderPage(t, "admin-content-form.html", albumForm(pickerGroups()))

	for _, needle := range []string{"var hasURL", "var removeURL", "var addURL", "data-picker-select-all"} {
		if !strings.Contains(out, needle) {
			t.Errorf("the picker script is missing %s, so a second click can't remove a photo", needle)
		}
	}
}
