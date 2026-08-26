package web

import "testing"

func TestRandomHomeActivities(t *testing.T) {
	mk := func(n int) []homeActivity {
		out := make([]homeActivity, n)
		for i := range out {
			out[i] = homeActivity{Title: string(rune('a' + i))}
		}
		return out
	}

	t.Run("fewer than max returns all unchanged", func(t *testing.T) {
		all := mk(3)
		got := randomHomeActivities(all, 4)
		if len(got) != 3 {
			t.Fatalf("got %d, want 3", len(got))
		}
		for i := range all {
			if got[i].Title != all[i].Title {
				t.Fatalf("order changed at %d: got %+v, want %+v", i, got[i], all[i])
			}
		}
	})

	t.Run("exactly max returns all unchanged", func(t *testing.T) {
		all := mk(4)
		got := randomHomeActivities(all, 4)
		if len(got) != 4 {
			t.Fatalf("got %d, want 4", len(got))
		}
	})

	t.Run("more than max picks exactly max, preserving relative order", func(t *testing.T) {
		all := mk(10)
		got := randomHomeActivities(all, 4)
		if len(got) != 4 {
			t.Fatalf("got %d, want 4", len(got))
		}
		// every picked activity must appear in `all`, and in the same
		// relative order it had there (no duplicates).
		lastIdx := -1
		seen := map[string]bool{}
		for _, a := range got {
			if seen[a.Title] {
				t.Fatalf("duplicate activity %q in result", a.Title)
			}
			seen[a.Title] = true
			idx := -1
			for i, x := range all {
				if x.Title == a.Title {
					idx = i
					break
				}
			}
			if idx == -1 {
				t.Fatalf("picked activity %+v not found in source", a)
			}
			if idx <= lastIdx {
				t.Fatalf("relative order not preserved: idx %d after %d", idx, lastIdx)
			}
			lastIdx = idx
		}
	})

	t.Run("varies across calls (not always the same 4 of 10)", func(t *testing.T) {
		all := mk(10)
		first := randomHomeActivities(all, 4)
		sawDifferent := false
		for i := 0; i < 50; i++ {
			got := randomHomeActivities(all, 4)
			if got[0].Title != first[0].Title || got[1].Title != first[1].Title || got[2].Title != first[2].Title || got[3].Title != first[3].Title {
				sawDifferent = true
				break
			}
		}
		if !sawDifferent {
			t.Fatalf("got the exact same 4 activities in the same order 50 times in a row — randomization doesn't seem to be happening")
		}
	})
}
