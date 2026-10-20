package files

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPosterKeyIsAPureFunctionOfTheFileNotItsStorageKey(t *testing.T) {
	const unit, id = "unit-1", "0b6c1f6e-8f43-4a0d-9d5e-2f2a1f3c9b10"
	want := "unit-1/posters/" + id + ".jpg"
	if got := PosterKey(unit, id); got != want {
		t.Errorf("PosterKey = %q, want %q", got, want)
	}

	// A poster can only be remade by a leader with a browser, so unlike a
	// thumbnail it must not follow a file's storage key around: the rekey pass
	// rewrites that key, and a poster stranded under the old one would be lost.
	// Nothing about where the file's bytes live may appear in the key.
	for _, storageKey := range []string{
		NewStorageKey(unit, DocumentsFolder, "camp.mp4"),
		StorageKeyFor(unit, "summer-camp-2026-07-15", id, "camp.mp4"),
	} {
		if strings.Contains(PosterKey(unit, id), storageKey) {
			t.Errorf("the poster key contains the file's storage key %q", storageKey)
		}
	}

	// And it can never be the key of an upload, which always carries a name
	// after the id ("<id>-<name>.<ext>").
	if PosterKey(unit, id) == StorageKeyFor(unit, "posters", id, "x.jpg") {
		t.Error("a poster key can equal an upload's key, so a poster could overwrite a file")
	}
}

func TestVideoPostersListsOnlyThisUnitsVideosWithTheirState(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	mine := testUnit(t, pool, "poster-mine")
	theirs := testUnit(t, pool, "poster-theirs")

	done := makeFile(t, pool, mine, "done.mp4", "video/mp4", CategoryEventPhoto)
	waiting := makeFile(t, pool, mine, "waiting.webm", "video/webm", CategoryEventPhoto)
	makeFile(t, pool, mine, "photo.jpg", "image/jpeg", CategoryEventPhoto)
	makeFile(t, pool, mine, "doc.pdf", "application/pdf", CategoryGeneral)
	makeFile(t, pool, theirs, "elsewhere.mp4", "video/mp4", CategoryEventPhoto)

	if err := SetPoster(ctx, pool, done.ID, mine, true); err != nil {
		t.Fatalf("SetPoster: %v", err)
	}

	got, err := VideoPosters(ctx, pool, mine)
	if err != nil {
		t.Fatalf("VideoPosters: %v", err)
	}
	state := map[string]bool{}
	for _, v := range got {
		state[v.ID] = v.Has
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want only the unit's two videos: %+v", len(got), got)
	}
	if !state[done.ID] || state[waiting.ID] {
		t.Errorf("poster state wrong: done=%v waiting=%v", state[done.ID], state[waiting.ID])
	}
	for _, v := range got {
		if v.Label == "" {
			t.Errorf("a video has no label: %+v", v)
		}
	}
}

func TestSetPosterIsScopedToTheUnit(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	mine := testUnit(t, pool, "poster-scope-mine")
	theirs := testUnit(t, pool, "poster-scope-theirs")
	video := makeFile(t, pool, mine, "v.mp4", "video/mp4", CategoryEventPhoto)

	// The other unit names this unit's file by id: nothing changes, and it says so.
	if err := SetPoster(ctx, pool, video.ID, theirs, true); !errors.Is(err, ErrNoSuchFile) {
		t.Errorf("SetPoster from the other unit = %v, want ErrNoSuchFile", err)
	}
	if has, _ := HasPoster(ctx, pool, video.ID, mine); has {
		t.Error("the other unit's request marked this unit's video as having a poster")
	}
	if has, _ := HasPoster(ctx, pool, video.ID, theirs); has {
		t.Error("HasPoster answered true for a file that is not in the unit asking")
	}

	if err := SetPoster(ctx, pool, video.ID, mine, true); err != nil {
		t.Fatalf("SetPoster: %v", err)
	}
	if has, err := HasPoster(ctx, pool, video.ID, mine); err != nil || !has {
		t.Errorf("HasPoster = %v, %v after SetPoster", has, err)
	}
	if err := SetPoster(ctx, pool, video.ID, mine, false); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	if has, _ := HasPoster(ctx, pool, video.ID, mine); has {
		t.Error("the poster flag didn't clear")
	}
}

func TestANewFileHasNoPoster(t *testing.T) {
	pool := testPool(t)
	unit := testUnit(t, pool, "poster-new")
	v := makeFile(t, pool, unit, "new.mp4", "video/mp4", CategoryEventPhoto)
	if has, err := HasPoster(context.Background(), pool, v.ID, unit); err != nil || has {
		t.Errorf("a file just uploaded: HasPoster = %v, %v, want false", has, err)
	}
}
