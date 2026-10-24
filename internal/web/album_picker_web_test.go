package web

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// An album holds photos; each viewer sees the ones they may. These run the
// whole story — ten photos in the library, six of them marked public — through
// the album editor's picker and the public album page, because the bug was in
// the seam between them: the picker offered only the public six, so the other
// four could never be put in an album, whatever the album page would have done
// with them.

type albumLibrary struct {
	public, private []string // file ids
}

func (s *emailSite) mediaLibrary(public, private int) albumLibrary {
	s.t.Helper()
	var lib albumLibrary
	add := func(n int, isPublic bool, tag string) []string {
		var ids []string
		for i := 0; i < n; i++ {
			ids = append(ids, s.one(`
				INSERT INTO files (unit_id, filename, display_name, content_type, size_bytes, storage_key, category, is_public)
				VALUES ($1, $2, $2, 'image/jpeg', 1000, $3, 'event_photo', $4) RETURNING id::text`,
				s.unitID, fmt.Sprintf("%s-%d.jpg", tag, i), fmt.Sprintf("test/%s/%s-%d", s.sfx, tag, i), isPublic))
		}
		return ids
	}
	lib.public = add(public, true, "public")
	lib.private = add(private, false, "private")
	return lib
}

func (l albumLibrary) all() []string { return append(append([]string{}, l.public...), l.private...) }

func TestTheAlbumPickerOffersEveryPhotoNotJustThePublicOnes(t *testing.T) {
	s := newEmailSite(t, false)
	editor := s.leader("Sam", "scoutmaster", nil)
	lib := s.mediaLibrary(6, 4)

	// Any album will do; a new one has the same picker.
	r := s.get(editor, "/admin/gallery/new")
	if r.status != http.StatusOK {
		t.Fatalf("the album form: %d", r.status)
	}
	for _, id := range lib.all() {
		if !strings.Contains(r.body, `data-image-picker-option="/files/`+id+`/download"`) {
			t.Errorf("file %s is missing from the picker — it can't be put in an album", id)
		}
	}
	// The private ones are marked, so a leader can see what signed-out visitors won't get.
	if got := strings.Count(r.body, ">Private</span>"); got != len(lib.private) {
		t.Errorf("%d tiles are badged Private, want %d (one per private photo)", got, len(lib.private))
	}
	if !strings.Contains(r.body, "Every photo is here, public or not") {
		t.Errorf("the page doesn't explain that private photos can go in an album")
	}
}

func TestAnAlbumOfPublicAndPrivatePhotosShowsEachViewerWhatTheyMaySee(t *testing.T) {
	s := newEmailSite(t, false)
	editor := s.leader("Sam", "scoutmaster", nil)
	lib := s.mediaLibrary(6, 4)

	var lines []string
	for _, id := range lib.all() {
		lines = append(lines, "/files/"+id+"/download")
	}
	// A public album: the page is open to visitors, and shows them only what is public.
	r := s.post(editor, "/admin/gallery", form("title", "Camp week", "body", strings.Join(lines, "\n"), "visibility", "public"))
	if r.status != http.StatusSeeOther {
		t.Fatalf("saving the album: %d %s", r.status, r.body)
	}
	albumID := s.one(`SELECT id::text FROM content_pages WHERE unit_id = $1 AND title = 'Camp week'`, s.unitID)
	if r := s.post(editor, "/admin/gallery/"+albumID+"/publish", form()); r.status != http.StatusSeeOther {
		t.Fatalf("publishing: %d", r.status)
	}
	// All ten were kept in the album itself, not dropped on the way in.
	if got := s.one(`SELECT body FROM content_pages WHERE id = $1`, albumID); strings.Count(got, "/files/") != 10 {
		t.Fatalf("the album stored %d photos, want all 10:\n%s", strings.Count(got, "/files/"), got)
	}

	seen := func(who person) map[string]bool {
		page := s.get(who, "/gallery/"+albumID)
		if page.status != http.StatusOK {
			t.Fatalf("the album page: %d", page.status)
		}
		got := map[string]bool{}
		for _, id := range lib.all() {
			got[id] = strings.Contains(page.body, id)
		}
		return got
	}

	visitor := seen(s.anonymous())
	for _, id := range lib.public {
		if !visitor[id] {
			t.Errorf("a visitor can't see public photo %s", id)
		}
	}
	for _, id := range lib.private {
		if visitor[id] {
			t.Errorf("a visitor was shown private photo %s", id)
		}
	}

	member := seen(editor)
	for _, id := range lib.all() {
		if !member[id] {
			t.Errorf("a signed-in member can't see photo %s — they should see all ten", id)
		}
	}
}
