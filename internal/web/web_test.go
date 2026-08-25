package web

import (
	"testing"

	"github.com/47-yonkers/scout-site/internal/files"
)

func TestChunkFiles(t *testing.T) {
	mk := func(n int) []files.File {
		out := make([]files.File, n)
		for i := range out {
			out[i] = files.File{ID: string(rune('a' + i))}
		}
		return out
	}

	t.Run("empty", func(t *testing.T) {
		if got := chunkFiles(nil, 25); got != nil {
			t.Fatalf("got %v, want nil", got)
		}
	})

	t.Run("fewer than one page", func(t *testing.T) {
		got := chunkFiles(mk(3), 25)
		if len(got) != 1 || len(got[0]) != 3 {
			t.Fatalf("got %v, want one page of 3", got)
		}
	})

	t.Run("exact multiple", func(t *testing.T) {
		got := chunkFiles(mk(50), 25)
		if len(got) != 2 || len(got[0]) != 25 || len(got[1]) != 25 {
			t.Fatalf("got %d pages (%v sizes), want 2 pages of 25", len(got), sizes(got))
		}
	})

	t.Run("remainder page", func(t *testing.T) {
		got := chunkFiles(mk(60), 25)
		if len(got) != 3 || len(got[0]) != 25 || len(got[1]) != 25 || len(got[2]) != 10 {
			t.Fatalf("got %d pages (%v sizes), want 25/25/10", len(got), sizes(got))
		}
	})

	t.Run("zero size", func(t *testing.T) {
		if got := chunkFiles(mk(5), 0); got != nil {
			t.Fatalf("got %v, want nil", got)
		}
	})
}

func sizes(pages [][]files.File) []int {
	out := make([]int, len(pages))
	for i, p := range pages {
		out[i] = len(p)
	}
	return out
}
