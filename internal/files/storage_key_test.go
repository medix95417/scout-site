package files

import (
	"strings"
	"testing"
	"time"
)

// Object keys are paths. Two of their three segments come from outside —
// an event title a leader typed, and a filename whoever is uploading
// chose — so the tests that matter here are the ones that prove neither
// can add a path segment of its own.

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestEventFolderReadsAsTheEvent(t *testing.T) {
	cases := map[string]string{
		"Summer Camp":              "summer-camp-2026-07-15",
		"Court of Honor":           "court-of-honor-2026-07-15",
		"Pinewood Derby 2026":      "pinewood-derby-2026-2026-07-15",
		"Scouting for Food — Drop": "scouting-for-food-drop-2026-07-15",
		"  Leading & trailing  ":   "leading-trailing-2026-07-15",
		"Multiple   spaces":        "multiple-spaces-2026-07-15",
	}
	for title, want := range cases {
		if got := EventFolder(title, day("2026-07-15")); got != want {
			t.Errorf("EventFolder(%q) = %q, want %q", title, got, want)
		}
	}
}

// TestEventFolderCannotEscapeItsFolder is the guard. A title is free text
// a leader types; it must never be able to add a path segment, climb out
// of the unit's namespace, or produce an empty one.
func TestEventFolderCannotEscapeItsFolder(t *testing.T) {
	hostile := []string{
		"../../etc/passwd",
		"..",
		"../",
		"a/b/c",
		"/absolute",
		"trailing/",
		`back\slash`,
		"nul\x00byte",
		"..%2f..%2f",
		"....//....//",
		"🏕️🔥", // no ASCII at all
		"...", // dots only
		"   ", // whitespace only
		strings.Repeat("x", 500),
	}
	for _, title := range hostile {
		got := EventFolder(title, day("2026-07-15"))
		if strings.Contains(got, "/") {
			t.Errorf("EventFolder(%q) = %q — added a path segment", title, got)
		}
		if strings.Contains(got, "..") {
			t.Errorf("EventFolder(%q) = %q — contains a parent reference", title, got)
		}
		if got == "" || strings.HasPrefix(got, "-") {
			t.Errorf("EventFolder(%q) = %q — not a usable folder name", title, got)
		}
		if len(got) > maxKeySegment+len("-2026-07-15") {
			t.Errorf("EventFolder(%q) is %d chars, over the segment bound", title, len(got))
		}
		for _, r := range got {
			if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
				t.Errorf("EventFolder(%q) = %q — contains %q", title, got, r)
				break
			}
		}
	}
}

// TestEventFolderNeverCollidesWithTheFixedFolders — the date suffix is
// what makes this true, so an event may be called anything at all.
func TestEventFolderNeverCollidesWithTheFixedFolders(t *testing.T) {
	for _, title := range []string{"Documents", "documents", "Photos", "photos", "email-images"} {
		got := EventFolder(title, day("2026-07-15"))
		for _, fixed := range []string{DocumentsFolder, PhotosFolder, "email-images"} {
			if got == fixed {
				t.Errorf("an event called %q takes over the %q folder", title, fixed)
			}
		}
	}
}

// TestEventFolderSeparatesYears — last year's Summer Camp and this
// year's are different campouts and must not share a folder.
func TestEventFolderSeparatesYears(t *testing.T) {
	a := EventFolder("Summer Camp", day("2025-07-14"))
	b := EventFolder("Summer Camp", day("2026-07-15"))
	if a == b {
		t.Errorf("both years landed in %q", a)
	}
}

func TestNewStorageKeyIsFiledUnderTheUnitAndFolder(t *testing.T) {
	key := NewStorageKey("unit-1", DocumentsFolder, "Bylaws 2026.pdf")

	if !strings.HasPrefix(key, "unit-1/documents/") {
		t.Errorf("key %q is not under the unit's documents folder", key)
	}
	if !strings.HasSuffix(key, "-bylaws-2026.pdf") {
		t.Errorf("key %q does not keep a recognizable filename", key)
	}
	if got := strings.Count(key, "/"); got != 2 {
		t.Errorf("key %q has %d separators, want exactly 2 (unit/folder/object)", key, got)
	}
	// Two uploads of the same file must not overwrite each other.
	if NewStorageKey("unit-1", DocumentsFolder, "Bylaws 2026.pdf") == key {
		t.Error("two uploads of the same filename produced the same key")
	}
}

// TestUploadedFilenameCannotAddPathSegments. fh.Filename comes from the
// multipart body and is chosen by the uploader, not the browser.
func TestUploadedFilenameCannotAddPathSegments(t *testing.T) {
	hostile := []string{
		"../../../etc/passwd",
		"a/b/c.jpg",
		"..\\..\\win.jpg",
		"photo.jpg/../../escape",
		"/etc/shadow",
		"....//x.png",
		"\x00.jpg",
		"",
		".",
		"..",
		strings.Repeat("y", 400) + ".jpeg",
	}
	for _, name := range hostile {
		key := NewStorageKey("unit-1", PhotosFolder, name)
		if !strings.HasPrefix(key, "unit-1/photos/") {
			t.Errorf("filename %q produced %q — escaped its folder", name, key)
		}
		if got := strings.Count(key, "/"); got != 2 {
			t.Errorf("filename %q produced %q — %d separators, want 2", name, key, got)
		}
		if strings.Contains(key, "..") {
			t.Errorf("filename %q produced %q — contains a parent reference", name, key)
		}
		object := key[strings.LastIndexByte(key, '/')+1:]
		if object == "" {
			t.Errorf("filename %q produced an empty object name", name)
		}
	}
}

// TestExtensionSurvives keeps keys recognizable in a bucket listing,
// which is the only reason the filename is in the key at all.
func TestExtensionSurvives(t *testing.T) {
	for name, wantSuffix := range map[string]string{
		"camp.JPG":       ".jpg",
		"notes.pdf":      ".pdf",
		"sheet.xlsx":     ".xlsx",
		"archive.tar.gz": ".gz",
		"no-extension":   "",
		"weird.j p g":    ".j-p-g",
		"trailing.dot.":  "",
	} {
		key := NewStorageKey("unit-1", DocumentsFolder, name)
		if !strings.HasSuffix(key, wantSuffix) {
			t.Errorf("NewStorageKey(%q) = %q, want it to end %q", name, key, wantSuffix)
		}
	}
}
