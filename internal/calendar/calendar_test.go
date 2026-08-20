package calendar

import "testing"

func strPtr(s string) *string { return &s }

func TestFilterVisibleToViewer(t *testing.T) {
	unscoped := Event{ID: "unit-wide"}
	denA := Event{ID: "den-a-event", SubGroupID: strPtr("den-a")}
	denB := Event{ID: "den-b-event", SubGroupID: strPtr("den-b")}
	events := []Event{unscoped, denA, denB}

	t.Run("canSeeAll bypasses filtering entirely", func(t *testing.T) {
		got := FilterVisibleToViewer(events, nil, true)
		if len(got) != 3 {
			t.Fatalf("got %d events, want 3 (unfiltered)", len(got))
		}
	})

	t.Run("no sub-group membership sees only unscoped events", func(t *testing.T) {
		got := FilterVisibleToViewer(events, map[string]bool{}, false)
		if len(got) != 1 || got[0].ID != "unit-wide" {
			t.Fatalf("got %+v, want only the unscoped event", got)
		}
	})

	t.Run("member of den-a sees unscoped plus den-a, not den-b", func(t *testing.T) {
		got := FilterVisibleToViewer(events, map[string]bool{"den-a": true}, false)
		if len(got) != 2 {
			t.Fatalf("got %d events, want 2", len(got))
		}
		ids := map[string]bool{got[0].ID: true, got[1].ID: true}
		if !ids["unit-wide"] || !ids["den-a-event"] || ids["den-b-event"] {
			t.Fatalf("got %+v, want unit-wide + den-a-event only", got)
		}
	})
}
