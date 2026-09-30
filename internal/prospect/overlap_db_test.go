package prospect

import (
	"context"
	"testing"
)

// The email counts are per family, not per child, so a family with two
// children sits under two statuses at once as soon as one of them
// moves. That is correct — one letter goes to one inbox — but with
// nothing saying so it reads as a status change that failed to save,
// which is exactly how it was reported.
//
// AddressesAlsoElsewhere is what the composer says it with.

func TestAddressesAlsoElsewhere(t *testing.T) {
	e := newEnv(t, "Overlap Family")
	ctx := context.Background()

	// Two siblings at one address, and a one-child family for contrast.
	var siblings []string
	for _, child := range []string{"Elles", "Ellee"} {
		p, err := AddByLeader(ctx, e.pool, New{
			UnitID: e.unitID, ParentName: "Their Parent",
			ParentEmail: "siblings@example.com", ChildName: child,
		}, e.actor)
		if err != nil {
			t.Fatal(err)
		}
		siblings = append(siblings, p.ID)
	}
	solo, err := AddByLeader(ctx, e.pool, New{
		UnitID: e.unitID, ParentName: "Solo Parent",
		ParentEmail: "solo@example.com", ChildName: "Only Child",
	}, e.actor)
	if err != nil {
		t.Fatal(err)
	}

	also := func(statuses ...string) int {
		t.Helper()
		n, err := AddressesAlsoElsewhere(ctx, e.pool, e.unitID, statuses)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Everyone at the same status: nobody is anywhere else.
	if got := also(StatusNew); got != 0 {
		t.Errorf("with everyone at one status, overlap = %d, want 0", got)
	}

	// Move one sibling. Now that address has a child inside "new" and a
	// child outside it — the case the report was about.
	if _, err := UpdateStatus(ctx, e.pool, e.unitID, siblings[0], StatusVisited, "", "", e.actor); err != nil {
		t.Fatal(err)
	}
	if got := also(StatusNew); got != 1 {
		t.Errorf("after moving one sibling, overlap for New = %d, want 1", got)
	}
	if got := also(StatusVisited); got != 1 {
		t.Errorf("overlap for Visited = %d, want 1 — the other sibling is still at New", got)
	}

	// Ticking both statuses covers the whole family, so there is no
	// longer anyone outside the selection.
	if got := also(StatusNew, StatusVisited); got != 0 {
		t.Errorf("with both statuses ticked, overlap = %d, want 0", got)
	}

	// Moving the one-child family changes nothing: it has no sibling to
	// be split across statuses.
	if _, err := UpdateStatus(ctx, e.pool, e.unitID, solo.ID, StatusVisited, "", "", e.actor); err != nil {
		t.Fatal(err)
	}
	if got := also(StatusVisited); got != 1 {
		t.Errorf("overlap for Visited = %d, want 1 — the one-child family isn't an overlap", got)
	}

	// Once every child of the family has moved, the overlap is gone.
	if _, err := UpdateStatus(ctx, e.pool, e.unitID, siblings[1], StatusVisited, "", "", e.actor); err != nil {
		t.Fatal(err)
	}
	if got := also(StatusVisited); got != 0 {
		t.Errorf("with the whole family moved, overlap = %d, want 0", got)
	}
	if got := also(StatusNew); got != 0 {
		t.Errorf("nobody is at New any more, so overlap = %d, want 0", got)
	}
}

// An overlap is only ever reported for a family the campaign would
// actually write to. A family excluded by an opt-out is not a recipient
// and must not be counted as one here either, or the note would claim
// an audience the send does not have.
func TestOverlapIgnoresOptOuts(t *testing.T) {
	e := newEnv(t, "Opt-out Overlap Family")
	ctx := context.Background()

	var ids []string
	for _, child := range []string{"First", "Second"} {
		p, err := AddByLeader(ctx, e.pool, New{
			UnitID: e.unitID, ParentName: "Their Parent",
			ParentEmail: "optout@example.com", ChildName: child,
		}, e.actor)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	if _, err := UpdateStatus(ctx, e.pool, e.unitID, ids[0], StatusVisited, "", "", e.actor); err != nil {
		t.Fatal(err)
	}

	n, err := AddressesAlsoElsewhere(ctx, e.pool, e.unitID, []string{StatusNew})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("overlap before the opt-out = %d, want 1", n)
	}

	// One child's record opting out takes the whole address out of
	// every campaign — the rule RecipientsForStatuses already applies.
	if _, err := SetEmailOptOut(ctx, e.pool, e.unitID, ids[1], true, e.actor); err != nil {
		t.Fatal(err)
	}
	if n, err = AddressesAlsoElsewhere(ctx, e.pool, e.unitID, []string{StatusNew}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("an opted-out family was reported as an overlap: %d", n)
	}

	// And the recipients agree, which is the property that matters:
	// the note must never describe families the send won't reach.
	got, err := RecipientsForStatuses(ctx, e.pool, e.unitID, []string{StatusNew})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("the opted-out family is still a recipient: %v", got)
	}
}

// Another unit's families are not this unit's overlap.
func TestOverlapIsScopedToItsUnit(t *testing.T) {
	e := newEnv(t, "Scoped Overlap Family")
	ctx := context.Background()
	other := newUnit(t, e.pool)

	var ids []string
	for _, child := range []string{"One", "Two"} {
		p, err := AddByLeader(ctx, e.pool, New{
			UnitID: e.unitID, ParentName: "Parent",
			ParentEmail: "shared@example.com", ChildName: child,
		}, e.actor)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	if _, err := UpdateStatus(ctx, e.pool, e.unitID, ids[0], StatusVisited, "", "", e.actor); err != nil {
		t.Fatal(err)
	}

	n, err := AddressesAlsoElsewhere(ctx, e.pool, other, []string{StatusNew})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("the other unit sees %d overlaps from this unit's families, want 0", n)
	}
}
