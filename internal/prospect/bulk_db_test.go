package prospect

import (
	"context"
	"errors"
	"testing"
)

// Moving a batch of enquiries to one status.
//
// Every property here is SQL — which of the posted ids count as this
// unit's, which rows are skipped for already being there, and what gets
// written to the Activity Log. All of them fail quietly: the page says
// it moved some families and the wrong set moves, or the right set
// moves and the record of who did it is missing.

func TestUpdateStatusManyMovesTheSelection(t *testing.T) {
	e := newEnv(t, "Bulk Family")
	ctx := context.Background()

	a := e.prospect(t, "a@example.com", StatusNew)
	b := e.prospect(t, "b@example.com", StatusNew)
	untouched := e.prospect(t, "c@example.com", StatusNew)

	got, err := UpdateStatusMany(ctx, e.pool, e.unitID, []string{a.ID, b.ID}, StatusVisited, e.actor)
	if err != nil {
		t.Fatalf("UpdateStatusMany: %v", err)
	}
	if got.Moved != 2 || got.Already != 0 {
		t.Errorf("moved %+v, want 2 moved and 0 already there", got)
	}

	for _, id := range []string{a.ID, b.ID} {
		after, err := Get(ctx, e.pool, e.unitID, id)
		if err != nil {
			t.Fatal(err)
		}
		if after.Status != StatusVisited {
			t.Errorf("%s is at %q, want %q", after.ParentEmail, after.Status, StatusVisited)
		}
	}
	// A row nobody ticked is a row nobody touched.
	still, err := Get(ctx, e.pool, e.unitID, untouched.ID)
	if err != nil {
		t.Fatal(err)
	}
	if still.Status != StatusNew {
		t.Errorf("an unticked enquiry moved to %q", still.Status)
	}
}

// Notes and category are per-enquiry judgements and are not part of a
// bulk move. Carrying them along would mean one leader's note about one
// family being written across a dozen.
func TestUpdateStatusManyLeavesEverythingElseAlone(t *testing.T) {
	e := newEnv(t, "Bulk Keep Family")
	ctx := context.Background()

	p, err := AddByLeader(ctx, e.pool, New{
		UnitID: e.unitID, ParentName: "Their Parent",
		ParentEmail: "keep@example.com", ChildName: "Kept Child",
		ParentPhone: "555-0123", ChildGrade: "3rd", ChildSchool: "Hillside",
	}, e.actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateStatus(ctx, e.pool, e.unitID, p.ID, StatusContacted, "", "Rang Tuesday", e.actor); err != nil {
		t.Fatal(err)
	}

	if _, err := UpdateStatusMany(ctx, e.pool, e.unitID, []string{p.ID}, StatusVisited, e.actor); err != nil {
		t.Fatal(err)
	}

	after, err := Get(ctx, e.pool, e.unitID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != StatusVisited {
		t.Fatalf("status = %q, want %q", after.Status, StatusVisited)
	}
	if after.Notes != "Rang Tuesday" {
		t.Errorf("a bulk move overwrote the notes: %q", after.Notes)
	}
	if after.ParentEmail != "keep@example.com" || after.ChildName != "Kept Child" ||
		after.ParentPhone != "555-0123" || after.ChildSchool != "Hillside" {
		t.Errorf("a bulk move changed the contact details: %+v", after)
	}
	if after.Source != SourceLeader {
		t.Errorf("a bulk move changed the source to %q", after.Source)
	}
}

// Already-there rows are reported, not rewritten. The count is what the
// page tells the leader, and "moved 3" when they ticked 4 has to be
// explainable or it reads as a failure.
func TestUpdateStatusManyCountsTheOnesAlreadyThere(t *testing.T) {
	e := newEnv(t, "Bulk Already Family")
	ctx := context.Background()

	moving := e.prospect(t, "moving@example.com", StatusNew)
	there := e.prospect(t, "there@example.com", StatusVisited)

	got, err := UpdateStatusMany(ctx, e.pool, e.unitID, []string{moving.ID, there.ID}, StatusVisited, e.actor)
	if err != nil {
		t.Fatal(err)
	}
	if got.Moved != 1 || got.Already != 1 {
		t.Errorf("got %+v, want 1 moved and 1 already there", got)
	}

	// The whole selection already at the target is a no-op, not an error
	// and not a write.
	got, err = UpdateStatusMany(ctx, e.pool, e.unitID, []string{moving.ID, there.ID}, StatusVisited, e.actor)
	if err != nil {
		t.Fatal(err)
	}
	if got.Moved != 0 || got.Already != 2 {
		t.Errorf("re-running the same move gave %+v, want 0 moved and 2 already there", got)
	}

	// And an enquiry left alone keeps the updated_at it had, because
	// "when did this last change" is the question that column answers.
	before, err := Get(ctx, e.pool, e.unitID, there.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateStatusMany(ctx, e.pool, e.unitID, []string{there.ID}, StatusVisited, e.actor); err != nil {
		t.Fatal(err)
	}
	after, err := Get(ctx, e.pool, e.unitID, there.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("a no-op move touched updated_at: %v then %v", before.UpdatedAt, after.UpdatedAt)
	}
}

// One entry per enquiry, the same shape a single status change writes.
// One entry for the batch would be the wrong record: the question
// afterwards is always asked on one family's name.
func TestUpdateStatusManyAuditsEachEnquiry(t *testing.T) {
	e := newEnv(t, "Bulk Audit Family")
	ctx := context.Background()

	a := e.prospect(t, "audit-a@example.com", StatusNew)
	b := e.prospect(t, "audit-b@example.com", StatusNew)
	alreadyThere := e.prospect(t, "audit-c@example.com", StatusVisited)

	entries := func(id string) int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, `
			SELECT count(*) FROM audit_log
			WHERE entity_type = 'prospect' AND entity_id = $1 AND action = 'update'
		`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	was := map[string]int{a.ID: entries(a.ID), b.ID: entries(b.ID), alreadyThere.ID: entries(alreadyThere.ID)}

	if _, err := UpdateStatusMany(ctx, e.pool, e.unitID,
		[]string{a.ID, b.ID, alreadyThere.ID}, StatusVisited, e.actor); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{a.ID, b.ID} {
		if got := entries(id) - was[id]; got != 1 {
			t.Errorf("a moved enquiry got %d new log entries, want 1", got)
		}
	}
	// Nothing happened to it, so nothing is logged about it — a dozen
	// entries recording no change is how an Activity Log stops being
	// worth reading.
	if got := entries(alreadyThere.ID) - was[alreadyThere.ID]; got != 0 {
		t.Errorf("an enquiry already at that status got %d new log entries, want 0", got)
	}

	// The entry records where it came from, which is the only reason to
	// keep a before at all.
	var before string
	if err := e.pool.QueryRow(ctx, `
		SELECT before_state->>'Status' FROM audit_log
		WHERE entity_type = 'prospect' AND entity_id = $1 AND action = 'update'
		ORDER BY occurred_at DESC LIMIT 1
	`, a.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != StatusNew {
		t.Errorf("the log says it moved from %q, want %q", before, StatusNew)
	}
}

// The unit scoping is in the statement, so an id from the other unit
// matches nothing rather than being moved by a handler that forgot to
// check it.
func TestUpdateStatusManyIsScopedToItsUnit(t *testing.T) {
	e := newEnv(t, "Bulk Scope Family")
	ctx := context.Background()
	other := newUnit(t, e.pool)
	if err := EnsureDefaults(ctx, e.pool, other); err != nil {
		t.Fatal(err)
	}

	mine := e.prospect(t, "mine@example.com", StatusNew)

	got, err := UpdateStatusMany(ctx, e.pool, other, []string{mine.ID}, StatusVisited, e.actor)
	if err != nil {
		t.Fatalf("UpdateStatusMany from the other unit: %v", err)
	}
	if got.Moved != 0 || got.Already != 0 {
		t.Errorf("the other unit moved %+v of this unit's enquiries, want none", got)
	}
	after, err := Get(ctx, e.pool, e.unitID, mine.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != StatusNew {
		t.Errorf("the other unit moved this unit's enquiry to %q", after.Status)
	}

	// A mixed selection moves only the half that belongs to the caller.
	theirs, err := AddByLeader(ctx, e.pool, New{
		UnitID: other, ParentName: "Other Parent",
		ParentEmail: "theirs@example.com", ChildName: "Their Child",
	}, e.actor)
	if err != nil {
		t.Fatal(err)
	}
	got, err = UpdateStatusMany(ctx, e.pool, other, []string{mine.ID, theirs.ID}, StatusVisited, e.actor)
	if err != nil {
		t.Fatal(err)
	}
	if got.Moved != 1 {
		t.Errorf("a mixed selection moved %+v, want only the caller's own one", got)
	}
}

// A status has to be one this unit still offers, checked once for the
// whole batch — otherwise a bulk move is a way to land a dozen families
// somewhere the pickers no longer show.
func TestUpdateStatusManyRefusesAStatusThatIsNotOffered(t *testing.T) {
	e := newEnv(t, "Bulk Status Family")
	ctx := context.Background()

	p := e.prospect(t, "status@example.com", StatusNew)

	if _, err := UpdateStatusMany(ctx, e.pool, e.unitID, []string{p.ID}, "not-a-status", e.actor); !errors.Is(err, ErrInvalid) {
		t.Errorf("an unknown status gave %v, want ErrInvalid", err)
	}

	retired := labelByValue(t, e.pool, e.unitID, KindStatus, StatusVisited)
	if _, err := SetLabelRetired(ctx, e.pool, e.unitID, retired.ID, true, e.actor); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateStatusMany(ctx, e.pool, e.unitID, []string{p.ID}, StatusVisited, e.actor); !errors.Is(err, ErrInvalid) {
		t.Errorf("a retired status gave %v, want ErrInvalid", err)
	}

	// Nothing was written on the way to either refusal.
	after, err := Get(ctx, e.pool, e.unitID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != StatusNew {
		t.Errorf("a refused bulk move left the enquiry at %q", after.Status)
	}
}

// An empty selection is a mis-click, not an error: nothing to do and
// nothing to say about it.
func TestUpdateStatusManyWithNothingSelected(t *testing.T) {
	e := newEnv(t, "Bulk Empty Family")
	ctx := context.Background()

	got, err := UpdateStatusMany(ctx, e.pool, e.unitID, nil, StatusVisited, e.actor)
	if err != nil {
		t.Fatalf("an empty selection gave %v, want no error", err)
	}
	if got.Moved != 0 || got.Already != 0 {
		t.Errorf("an empty selection reported %+v", got)
	}
}
