package prospect

import (
	"context"
	"errors"
	"testing"
)

// Correcting an enquiry after it is entered, sorting by where each
// family has got to, and an age below what Scouting takes.

func TestUpdateDetails(t *testing.T) {
	e := newEnv(t, "Edit Family")
	ctx := context.Background()

	p, err := Create(ctx, e.pool, validSubmission(e.unitID))
	if err != nil {
		t.Fatal(err)
	}
	// Somewhere along the workflow, with notes — none of which this
	// should disturb.
	if _, err := UpdateStatus(ctx, e.pool, e.unitID, p.ID, StatusContacted, "", "Rang Tuesday", e.actor); err != nil {
		t.Fatal(err)
	}

	age := 7
	after, err := UpdateDetails(ctx, e.pool, e.unitID, p.ID, New{
		ParentName:  "Jamie Rivera-Okafor",
		ParentEmail: "jamie.new@example.com",
		ParentPhone: "555-0199",
		ChildName:   "Samuel Rivera",
		ChildAge:    &age,
		ChildGrade:  "2nd",
		ChildSchool: "Hillside Elementary",
		Message:     "Met at the school fair.",
	}, e.actor)
	if err != nil {
		t.Fatalf("UpdateDetails: %v", err)
	}

	if after.ParentEmail != "jamie.new@example.com" || after.ChildName != "Samuel Rivera" {
		t.Errorf("the correction didn't stick: %+v", after)
	}
	if after.ChildAge == nil || *after.ChildAge != 7 {
		t.Errorf("age = %v, want 7", after.ChildAge)
	}

	// The workflow half is UpdateStatus's, and editing details must not
	// reach into it — a leader fixing a misheard email has not decided
	// anything about where the family has got to.
	if after.Status != StatusContacted {
		t.Errorf("editing details moved the status to %q", after.Status)
	}
	if after.Notes != "Rang Tuesday" {
		t.Errorf("editing details lost the notes: %q", after.Notes)
	}
	if after.Source != p.Source {
		t.Errorf("editing details changed the source to %q", after.Source)
	}
}

// A record cannot be edited into a state the form would have refused.
func TestUpdateDetailsValidates(t *testing.T) {
	e := newEnv(t, "Invalid Edit Family")
	ctx := context.Background()

	p, err := Create(ctx, e.pool, validSubmission(e.unitID))
	if err != nil {
		t.Fatal(err)
	}
	tooOld := 99
	for _, c := range []struct {
		name string
		in   New
	}{
		{"no parent name", New{ParentEmail: "a@example.com", ChildName: "Kid"}},
		{"no email", New{ParentName: "A Parent", ChildName: "Kid"}},
		{"not an email", New{ParentName: "A Parent", ParentEmail: "nope", ChildName: "Kid"}},
		{"no child name", New{ParentName: "A Parent", ParentEmail: "a@example.com"}},
		{"an impossible age", New{ParentName: "A Parent", ParentEmail: "a@example.com", ChildName: "Kid", ChildAge: &tooOld}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := UpdateDetails(ctx, e.pool, e.unitID, p.ID, c.in, e.actor); !errors.Is(err, ErrInvalid) {
				t.Errorf("UpdateDetails accepted %s: %v", c.name, err)
			}
		})
	}

	// And nothing was written along the way.
	unchanged, err := Get(ctx, e.pool, e.unitID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.ParentEmail != p.ParentEmail {
		t.Errorf("a refused edit changed the record to %q", unchanged.ParentEmail)
	}
}

// One unit cannot correct another's records.
func TestUpdateDetailsIsScopedToItsUnit(t *testing.T) {
	e := newEnv(t, "Scoped Edit Family")
	ctx := context.Background()
	other := newUnit(t, e.pool)

	p, err := Create(ctx, e.pool, validSubmission(e.unitID))
	if err != nil {
		t.Fatal(err)
	}
	_, err = UpdateDetails(ctx, e.pool, other, p.ID, New{
		ParentName: "Someone Else", ParentEmail: "else@example.com", ChildName: "Kid",
	}, e.actor)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("another unit edited this unit's enquiry: %v", err)
	}
}

// Sorting by status uses the unit's own workflow order, not the
// alphabet — a leader working through "everyone still at New enquiry"
// wants New before Contacted, which is what the statuses page means.
func TestListForUnitOrderByStatus(t *testing.T) {
	e := newEnv(t, "Sort Family")
	ctx := context.Background()

	// Created in an order that is neither the workflow order nor its
	// reverse, so passing cannot be an accident of insertion.
	for _, c := range []struct{ email, status string }{
		{"c@example.com", StatusJoined},
		{"a@example.com", StatusNew},
		{"d@example.com", StatusContacted},
		{"b@example.com", StatusDeclined},
		{"e@example.com", StatusVisited},
	} {
		in := validSubmission(e.unitID)
		in.ParentEmail = c.email
		p, err := Create(ctx, e.pool, in)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := UpdateStatus(ctx, e.pool, e.unitID, p.ID, c.status, "", "", e.actor); err != nil {
			t.Fatal(err)
		}
	}

	byStatus, err := ListForUnit(ctx, e.pool, e.unitID, false, OrderStatus)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{StatusNew, StatusContacted, StatusVisited, StatusJoined, StatusDeclined}
	if len(byStatus) != len(want) {
		t.Fatalf("got %d rows, want %d", len(byStatus), len(want))
	}
	for i, status := range want {
		if byStatus[i].Status != status {
			t.Errorf("position %d is %q, want %q (full order: %v)", i, byStatus[i].Status, status, statuses(byStatus))
		}
	}

	// A status the unit reorders sorts the new way, since the order
	// comes from the label rather than from this package.
	visited := labelByValue(t, e.pool, e.unitID, KindStatus, StatusVisited)
	if err := MoveLabel(ctx, e.pool, e.unitID, visited.ID, true, e.actor); err != nil {
		t.Fatal(err)
	}
	if err := MoveLabel(ctx, e.pool, e.unitID, visited.ID, true, e.actor); err != nil {
		t.Fatal(err)
	}
	reordered, err := ListForUnit(ctx, e.pool, e.unitID, false, OrderStatus)
	if err != nil {
		t.Fatal(err)
	}
	if reordered[0].Status != StatusVisited {
		t.Errorf("moving a status up the list didn't change the sort: %v", statuses(reordered))
	}

	// The default is still newest first, and says nothing about status.
	newest, err := ListForUnit(ctx, e.pool, e.unitID, false, OrderNewest)
	if err != nil {
		t.Fatal(err)
	}
	if newest[0].ParentEmail != "e@example.com" {
		t.Errorf("newest-first starts with %q, want the last one created", newest[0].ParentEmail)
	}
}

func statuses(list []Prospect) []string {
	out := make([]string, 0, len(list))
	for _, p := range list {
		out = append(out, p.Status)
	}
	return out
}

// An age below what any program takes is allowed, so a unit can keep
// in touch with a younger sibling until they are old enough.
func TestAYoungerSiblingCanBeRecorded(t *testing.T) {
	e := newEnv(t, "Young Family")
	ctx := context.Background()

	for _, age := range []int{0, 1, 2, 3} {
		in := validSubmission(e.unitID)
		in.ParentEmail = "young@example.com"
		in.ChildName = "Sibling aged " + string(rune('0'+age))
		in.ChildAge = &age
		p, err := Create(ctx, e.pool, in)
		if err != nil {
			t.Fatalf("age %d was refused: %v", age, err)
		}
		if p.ChildAge == nil || *p.ChildAge != age {
			t.Errorf("age %d didn't round-trip: %v", age, p.ChildAge)
		}
	}

	// Still bounded, so a slipped digit is caught.
	for _, bad := range []int{-1, 22, 90} {
		in := validSubmission(e.unitID)
		in.ChildAge = &bad
		if _, err := Create(ctx, e.pool, in); !errors.Is(err, ErrInvalid) {
			t.Errorf("age %d was accepted: %v", bad, err)
		}
	}
}
