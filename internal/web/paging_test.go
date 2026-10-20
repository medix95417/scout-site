package web

import (
	"strconv"
	"testing"
)

func TestNewPager(t *testing.T) {
	cases := []struct {
		name        string
		total, size int
		raw         string
		page, pages int
		start, end  int
		prev, next  string
	}{
		{"empty list is one empty page", 0, 12, "", 1, 1, 0, 0, "", ""},
		{"a list that fits is one page", 5, 12, "", 1, 1, 0, 5, "", ""},
		{"exactly one full page", 12, 12, "", 1, 1, 0, 12, "", ""},
		{"one over spills to a second page", 13, 12, "", 1, 2, 0, 12, "", "/gallery?page=2"},
		{"the second page of two", 13, 12, "2", 2, 2, 12, 13, "/gallery", ""},
		{"a middle page links both ways, and back to the bare path for page 1", 40, 12, "2", 2, 4, 12, 24, "/gallery", "/gallery?page=3"},
		{"the last page is short", 40, 12, "4", 4, 4, 36, 40, "/gallery?page=3", ""},
		{"past the end is the last page, not an error", 40, 12, "99", 4, 4, 36, 40, "/gallery?page=3", ""},
		{"zero is the first page", 40, 12, "0", 1, 4, 0, 12, "", "/gallery?page=2"},
		{"negative is the first page", 40, 12, "-3", 1, 4, 0, 12, "", "/gallery?page=2"},
		{"not a number is the first page", 40, 12, "abc", 1, 4, 0, 12, "", "/gallery?page=2"},
		{"a value too big for an int is the first page", 40, 12, "99999999999999999999", 1, 4, 0, 12, "", "/gallery?page=2"},
		{"a nonsense page size cannot divide by zero", 3, 0, "", 1, 3, 0, 1, "", "/gallery?page=2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := newPager(c.total, c.size, c.raw, "/gallery")
			want := pager{Page: c.page, Pages: c.pages, Start: c.start, End: c.end, PrevURL: c.prev, NextURL: c.next}
			if got != want {
				t.Errorf("newPager(%d, %d, %q) = %+v, want %+v", c.total, c.size, c.raw, got, want)
			}
		})
	}
}

// Every item appears on exactly one page: walking the links from the first
// page reaches all of them once, with none skipped at a boundary.
func TestWalkingThePagesVisitsEveryItemOnce(t *testing.T) {
	for _, total := range []int{0, 1, 11, 12, 13, 24, 25, 100} {
		seen := make([]int, total)
		p := newPager(total, 12, "", "/gallery")
		for {
			for i := p.Start; i < p.End; i++ {
				seen[i]++
			}
			if p.NextURL == "" {
				break
			}
			next := p.Page + 1
			p = newPager(total, 12, strconv.Itoa(next), "/gallery")
		}
		for i, n := range seen {
			if n != 1 {
				t.Errorf("total %d: item %d shown %d times", total, i, n)
			}
		}
	}
}
