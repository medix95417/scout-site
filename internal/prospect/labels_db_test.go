package prospect

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A unit's own status and category lists, against a real database.
// The rules worth holding are the ones that quietly corrupt data if
// they slip: that a rename doesn't move anybody, that a value is never
// reused, and that one unit's lists are invisible to the other.

func TestCreateLabel(t *testing.T) {
	e := newEnv(t, "Label Family")
	ctx := context.Background()

	l, err := CreateLabel(ctx, e.pool, e.unitID, KindStatus, "Left voicemail", false, e.actor)
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	if l.Value != "left-voicemail" {
		t.Errorf("value = %q, want a slug of the name", l.Value)
	}
	if l.SortOrder <= 5 {
		t.Errorf("a new status should land after the five defaults, got sort_order %d", l.SortOrder)
	}

	// The same name twice would give a leader two identical-looking
	// options and no way to tell which one a prospect is on.
	if _, err := CreateLabel(ctx, e.pool, e.unitID, KindStatus, "left VOICEMAIL", false, e.actor); !errors.Is(err, ErrLabelExists) {
		t.Errorf("a duplicate name (differing only in case) was accepted: %v", err)
	}

	// The two lists are separate, so the same name in each is fine.
	if _, err := CreateLabel(ctx, e.pool, e.unitID, KindCategory, "Left voicemail", false, e.actor); err != nil {
		t.Errorf("the same name in the other list was refused: %v", err)
	}

	t.Run("names that cannot be slugs are refused", func(t *testing.T) {
		for _, bad := range []string{"", "   ", "!!!", "—"} {
			if _, err := CreateLabel(ctx, e.pool, e.unitID, KindCategory, bad, false, e.actor); !errors.Is(err, ErrInvalid) {
				t.Errorf("CreateLabel(%q) = %v, want ErrInvalid", bad, err)
			}
		}
		if _, err := CreateLabel(ctx, e.pool, e.unitID, KindCategory, strings.Repeat("x", 61), false, e.actor); !errors.Is(err, ErrInvalid) {
			t.Error("an over-long name was accepted")
		}
	})

	t.Run("a category is never closed", func(t *testing.T) {
		c, err := CreateLabel(ctx, e.pool, e.unitID, KindCategory, "Wants Cubs", true, e.actor)
		if err != nil {
			t.Fatal(err)
		}
		if c.Closed {
			t.Error("a category was stored as closed — that is a status's idea, and nothing reads it here")
		}
		if _, err := SetLabelClosed(ctx, e.pool, e.unitID, c.ID, true, e.actor); !errors.Is(err, ErrInvalid) {
			t.Error("a category could be marked closed after the fact")
		}
	})
}

// Renaming changes what people read and nothing else. If it moved the
// value, every prospect and campaign already using it would be
// orphaned — silently, and only visible later as a blank audience.
func TestRenameLabelDoesNotMoveAnybody(t *testing.T) {
	e := newEnv(t, "Rename Family")
	ctx := context.Background()

	p, err := Create(ctx, e.pool, validSubmission(e.unitID))
	if err != nil {
		t.Fatal(err)
	}
	contacted := labelByValue(t, e.pool, e.unitID, KindStatus, StatusContacted)
	if _, err := UpdateStatus(ctx, e.pool, e.unitID, p.ID, StatusContacted, "", "", e.actor); err != nil {
		t.Fatal(err)
	}

	renamed, err := RenameLabel(ctx, e.pool, e.unitID, contacted.ID, "Spoke to them", e.actor)
	if err != nil {
		t.Fatalf("RenameLabel: %v", err)
	}
	if renamed.Value != StatusContacted {
		t.Errorf("the stored value changed to %q — everything filed under the old one is now orphaned", renamed.Value)
	}

	after, err := Get(ctx, e.pool, e.unitID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != StatusContacted {
		t.Errorf("the prospect moved to %q", after.Status)
	}
	text, err := LabelText(ctx, e.pool, e.unitID, KindStatus)
	if err != nil {
		t.Fatal(err)
	}
	if got := LabelIn(text, after.Status); got != "Spoke to them" {
		t.Errorf("the prospect reads as %q, want the new name", got)
	}
}

// Retiring withdraws a label without losing what it meant.
func TestRetireLabel(t *testing.T) {
	e := newEnv(t, "Retire Family")
	ctx := context.Background()

	p, err := Create(ctx, e.pool, validSubmission(e.unitID))
	if err != nil {
		t.Fatal(err)
	}
	visited := labelByValue(t, e.pool, e.unitID, KindStatus, StatusVisited)
	if _, err := UpdateStatus(ctx, e.pool, e.unitID, p.ID, StatusVisited, "", "", e.actor); err != nil {
		t.Fatal(err)
	}
	if _, err := SetLabelRetired(ctx, e.pool, e.unitID, visited.ID, true, e.actor); err != nil {
		t.Fatalf("SetLabelRetired: %v", err)
	}

	// Gone from the pickers...
	live, err := ListLabels(ctx, e.pool, e.unitID, KindStatus, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range live {
		if l.Value == StatusVisited {
			t.Error("a retired status is still offered")
		}
	}
	if ok, _ := LabelExists(ctx, e.pool, e.unitID, KindStatus, StatusVisited); ok {
		t.Error("a prospect can still be moved to a retired status")
	}

	// ...but the prospect already on it keeps it, and it still reads
	// as itself rather than as a slug.
	after, err := Get(ctx, e.pool, e.unitID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != StatusVisited {
		t.Errorf("retiring a status moved the prospect off it, to %q", after.Status)
	}
	text, err := LabelText(ctx, e.pool, e.unitID, KindStatus)
	if err != nil {
		t.Fatal(err)
	}
	if got := LabelIn(text, StatusVisited); got != "Visited a meeting" {
		t.Errorf("a retired status reads as %q, want its own name", got)
	}

	// And it can come back.
	if _, err := SetLabelRetired(ctx, e.pool, e.unitID, visited.ID, false, e.actor); err != nil {
		t.Fatal(err)
	}
	if ok, _ := LabelExists(ctx, e.pool, e.unitID, KindStatus, StatusVisited); !ok {
		t.Error("restoring a retired status didn't put it back")
	}
}

// Two statuses cannot be retired: the one new enquiries land on, and
// the last one left. Either would leave the unit unable to file an
// enquiry at all.
func TestRetireRefusesToBreakTheWorkflow(t *testing.T) {
	e := newEnv(t, "Guard Family")
	ctx := context.Background()

	newStatus := labelByValue(t, e.pool, e.unitID, KindStatus, StatusNewValue)
	if _, err := SetLabelRetired(ctx, e.pool, e.unitID, newStatus.ID, true, e.actor); !errors.Is(err, ErrProtectedLabel) {
		t.Errorf("the status new enquiries arrive at could be retired: %v", err)
	}

	// Retire everything else, then check the last one holds.
	all, err := ListLabels(ctx, e.pool, e.unitID, KindStatus, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range all {
		if l.Value == StatusNewValue {
			continue
		}
		if _, err := SetLabelRetired(ctx, e.pool, e.unitID, l.ID, true, e.actor); err != nil {
			t.Fatalf("retiring %q: %v", l.Value, err)
		}
	}
	live, err := ListLabels(ctx, e.pool, e.unitID, KindStatus, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 {
		t.Fatalf("expected one status left, got %d", len(live))
	}
}

// A closed status is what drops an enquiry out of the default list, and
// which statuses those are is now the unit's own choice.
func TestClosedStatusDecidesTheOpenList(t *testing.T) {
	e := newEnv(t, "Closed Family")
	ctx := context.Background()

	p, err := Create(ctx, e.pool, validSubmission(e.unitID))
	if err != nil {
		t.Fatal(err)
	}
	if !inOpenList(t, ctx, e.pool, e.unitID, p.ID) {
		t.Fatal("a new enquiry should start open")
	}

	// Mark the status it is on as dealt with, and it drops out —
	// without the prospect changing at all.
	newStatus := labelByValue(t, e.pool, e.unitID, KindStatus, StatusNewValue)
	if _, err := SetLabelClosed(ctx, e.pool, e.unitID, newStatus.ID, true, e.actor); err != nil {
		t.Fatal(err)
	}
	if inOpenList(t, ctx, e.pool, e.unitID, p.ID) {
		t.Error("marking its status closed didn't drop the enquiry out of the open list")
	}
	n, err := CountOpenForUnit(ctx, e.pool, e.unitID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("open count = %d, want 0", n)
	}

	// And a unit that decides "joined" still wants attention gets that.
	joined := labelByValue(t, e.pool, e.unitID, KindStatus, StatusJoined)
	if _, err := SetLabelClosed(ctx, e.pool, e.unitID, joined.ID, false, e.actor); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateStatus(ctx, e.pool, e.unitID, p.ID, StatusJoined, "", "", e.actor); err != nil {
		t.Fatal(err)
	}
	if !inOpenList(t, ctx, e.pool, e.unitID, p.ID) {
		t.Error("a status the unit reopened still drops the enquiry out")
	}
}

// One unit's lists are not the other's — the whole reason these moved
// out of a global enum.
func TestLabelsAreScopedToTheirUnit(t *testing.T) {
	e := newEnv(t, "Scope Family")
	ctx := context.Background()
	other := newUnit(t, e.pool)

	mine, err := CreateLabel(ctx, e.pool, e.unitID, KindCategory, "Sibling of a current Scout", false, e.actor)
	if err != nil {
		t.Fatal(err)
	}

	theirs, err := ListLabels(ctx, e.pool, other, KindCategory, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(theirs) != 0 {
		t.Errorf("the other unit can see %d of this unit's categories", len(theirs))
	}
	if ok, _ := LabelExists(ctx, e.pool, other, KindCategory, mine.Value); ok {
		t.Error("the other unit could file a prospect under this unit's category")
	}
	// And it cannot reach in to change it.
	if _, err := RenameLabel(ctx, e.pool, other, mine.ID, "Renamed by someone else", e.actor); !errors.Is(err, ErrLabelNotFound) {
		t.Errorf("another unit renamed this unit's label: %v", err)
	}
	if _, err := SetLabelRetired(ctx, e.pool, other, mine.ID, true, e.actor); !errors.Is(err, ErrLabelNotFound) {
		t.Errorf("another unit retired this unit's label: %v", err)
	}
}

// A category is optional, and "" is always a valid answer — it is what
// uncategorised means and the only way back to it.
func TestCategoryIsOptional(t *testing.T) {
	e := newEnv(t, "Category Family")
	ctx := context.Background()

	p, err := Create(ctx, e.pool, validSubmission(e.unitID))
	if err != nil {
		t.Fatal(err)
	}
	if p.Category != "" {
		t.Errorf("a new enquiry arrived pre-categorised as %q", p.Category)
	}

	c, err := CreateLabel(ctx, e.pool, e.unitID, KindCategory, "School fair", false, e.actor)
	if err != nil {
		t.Fatal(err)
	}
	after, err := UpdateStatus(ctx, e.pool, e.unitID, p.ID, StatusNew, c.Value, "", e.actor)
	if err != nil {
		t.Fatalf("setting a category: %v", err)
	}
	if after.Category != c.Value {
		t.Errorf("category = %q, want %q", after.Category, c.Value)
	}

	cleared, err := UpdateStatus(ctx, e.pool, e.unitID, p.ID, StatusNew, "", "", e.actor)
	if err != nil {
		t.Fatalf("clearing a category: %v", err)
	}
	if cleared.Category != "" {
		t.Errorf("a category could not be cleared, still %q", cleared.Category)
	}

	if _, err := UpdateStatus(ctx, e.pool, e.unitID, p.ID, StatusNew, "not-a-category", "", e.actor); !errors.Is(err, ErrInvalid) {
		t.Error("an unknown category was accepted")
	}
}

// Moving swaps a label with its neighbour and stops at the ends.
func TestMoveLabel(t *testing.T) {
	e := newEnv(t, "Move Family")
	ctx := context.Background()

	order := func() []string {
		all, err := ListLabels(ctx, e.pool, e.unitID, KindStatus, true)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(all))
		for _, l := range all {
			out = append(out, l.Value)
		}
		return out
	}
	before := order()
	second := labelByValue(t, e.pool, e.unitID, KindStatus, before[1])

	if err := MoveLabel(ctx, e.pool, e.unitID, second.ID, true, e.actor); err != nil {
		t.Fatalf("MoveLabel up: %v", err)
	}
	after := order()
	if after[0] != before[1] || after[1] != before[0] {
		t.Errorf("order after moving up = %v, want the first two swapped from %v", after, before)
	}

	// At the top, moving up again is a no-op rather than an error —
	// the buttons are always there, and pressing one at the end of the
	// list shouldn't produce a failure page.
	first := labelByValue(t, e.pool, e.unitID, KindStatus, after[0])
	if err := MoveLabel(ctx, e.pool, e.unitID, first.ID, true, e.actor); err != nil {
		t.Errorf("moving the first item up returned an error: %v", err)
	}
	if got := order(); got[0] != after[0] {
		t.Errorf("moving the first item up changed the order to %v", got)
	}
}
