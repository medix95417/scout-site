package calendar

import (
	"context"
	"testing"
	"time"
)

// The file library's "filter by event" control used to offer every event
// the unit had ever held. A leader picked a campout off that list, got an
// empty page, and had no way to tell whether the filter was broken or the
// campout simply had no photos. ListWithFilesForUnit is the fix, and it
// is entirely a SQL question — an EXISTS against event_files — so it can
// only be checked against Postgres.
//
// Uses the same fixture as the feed-conflict tests (see
// feedconflict_db_test.go) rather than a second one; it already makes a
// unit and an author, which is all an event needs.

// eventAt keeps the tests readable — the dates are irrelevant here, only
// which events have something attached.
func eventAt(days int) time.Time {
	return time.Now().Add(time.Duration(days) * 24 * time.Hour)
}

// withStatus inserts an event in a status other than published, which
// fixture.ourEvent doesn't offer.
func (f fixture) eventWithStatus(t *testing.T, title, status string) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(), `
		INSERT INTO events (unit_id, title, starts_at, ends_at, visibility, status, created_by)
		VALUES ($1, $2, $3, $4, 'members', $5, $6) RETURNING id::text
	`, f.unitID, title, eventAt(-2), eventAt(-2).Add(time.Hour), status, f.actor).Scan(&id); err != nil {
		t.Fatalf("inserting %q: %v", title, err)
	}
	return id
}

// attach puts a file in the library and links it to an event.
func (f fixture) attach(t *testing.T, eventID, name string) {
	t.Helper()
	ctx := context.Background()
	var fileID string
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO files (unit_id, filename, content_type, size_bytes, storage_key, category)
		VALUES ($1, $2, 'image/jpeg', 100, $3, 'event_photo') RETURNING id::text
	`, f.unitID, name, f.unitID+"/"+name).Scan(&fileID); err != nil {
		t.Fatalf("creating file %q: %v", name, err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO event_files (event_id, file_id) VALUES ($1, $2)`, eventID, fileID); err != nil {
		t.Fatalf("linking file %q: %v", name, err)
	}
}

func titles(events []Event) map[string]bool {
	out := map[string]bool{}
	for _, e := range events {
		out[e.Title] = true
	}
	return out
}

func TestListWithFilesOffersOnlyEventsThatHaveSome(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "cal-withfiles")

	withPhotos := f.ourEvent(t, "Summer Camp", eventAt(-30), 48)
	withDoc := f.ourEvent(t, "Court of Honor", eventAt(-10), 2)
	f.ourEvent(t, "Troop Meeting", eventAt(-3), 1)  // nothing attached
	f.ourEvent(t, "Planning Night", eventAt(-1), 1) // nothing attached

	f.attach(t, withPhotos, "camp1.jpg")
	f.attach(t, withPhotos, "camp2.jpg") // two files, still one event
	f.attach(t, withDoc, "programme.jpg")

	got, err := ListWithFilesForUnit(ctx, f.pool, f.unitID)
	if err != nil {
		t.Fatalf("ListWithFilesForUnit: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2 — an event with two files must not appear twice: %v", len(got), titles(got))
	}
	have := titles(got)
	for _, want := range []string{"Summer Camp", "Court of Honor"} {
		if !have[want] {
			t.Errorf("%q has files but is not offered as a filter", want)
		}
	}
	for _, notWant := range []string{"Troop Meeting", "Planning Night"} {
		if have[notWant] {
			t.Errorf("%q has nothing attached but is offered as a filter", notWant)
		}
	}

	// And the list the LINK controls use still offers everything, or a
	// file could never be attached to an event for the first time.
	all, err := ListAllForUnit(ctx, f.pool, f.unitID)
	if err != nil {
		t.Fatalf("ListAllForUnit: %v", err)
	}
	if len(all) != 4 {
		t.Errorf("the link list offers %d events, want all 4: %v", len(all), titles(all))
	}
}

// TestListWithFilesMatchesWhatThePageGroups. The filter has to agree with
// the groups the library actually renders, and those are built from the
// files without regard to event status — so filtering this list by status
// would create the mirror-image bug: a group visible on the page with no
// way to filter to it.
func TestListWithFilesMatchesWhatThePageGroups(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "cal-withfiles-status")

	pending := f.eventWithStatus(t, "Patrol Hike (awaiting approval)", "pending_approval")
	f.attach(t, pending, "hike.jpg")

	got, err := ListWithFilesForUnit(ctx, f.pool, f.unitID)
	if err != nil {
		t.Fatalf("ListWithFilesForUnit: %v", err)
	}
	if !titles(got)["Patrol Hike (awaiting approval)"] {
		t.Error("an unpublished event with photos is groupable on the page but not filterable")
	}
}

// TestListWithFilesIsScopedToTheUnit — the Troop's campout photos must
// not put the Troop's campout in the Pack's filter.
func TestListWithFilesIsScopedToTheUnit(t *testing.T) {
	ctx := context.Background()
	troop := newFixture(t, "cal-wf-troop")
	pack := newFixture(t, "cal-wf-pack")

	troopEvent := troop.ourEvent(t, "Troop Campout", eventAt(-5), 24)
	troop.attach(t, troopEvent, "troop.jpg")

	got, err := ListWithFilesForUnit(ctx, pack.pool, pack.unitID)
	if err != nil {
		t.Fatalf("ListWithFilesForUnit: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("the Pack was offered the Troop's events: %v", titles(got))
	}
}

// TestUnlinkingRemovesTheFilterOption closes the loop: the list is
// derived, not cached, so detaching the last file takes the event back
// out of the filter.
func TestUnlinkingRemovesTheFilterOption(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "cal-wf-unlink")
	ev := f.ourEvent(t, "Pinewood Derby", eventAt(-7), 4)
	f.attach(t, ev, "derby.jpg")

	if got, _ := ListWithFilesForUnit(ctx, f.pool, f.unitID); len(got) != 1 {
		t.Fatalf("setup: got %d events, want 1", len(got))
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM event_files WHERE event_id = $1`, ev); err != nil {
		t.Fatal(err)
	}
	got, err := ListWithFilesForUnit(ctx, f.pool, f.unitID)
	if err != nil {
		t.Fatalf("ListWithFilesForUnit: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("the event still offers itself after its last file was unlinked: %v", titles(got))
	}
}
