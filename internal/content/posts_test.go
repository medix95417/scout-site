package content

import "testing"

func TestParseGalleryPhotos(t *testing.T) {
	body := "https://example.com/a.jpg\n" +
		"https://example.com/b.jpg | A caption\n" +
		"\n" +
		"/files/123/download | A clip | video\n" +
		"https://example.com/c.mp4 | pasted link\n" +
		"https://example.com/d.MOV\n"

	got := ParseGalleryPhotos(body)
	if len(got) != 5 {
		t.Fatalf("got %d photos, want 5: %+v", len(got), got)
	}

	want := []GalleryPhoto{
		{URL: "https://example.com/a.jpg", Caption: "", IsVideo: false},
		{URL: "https://example.com/b.jpg", Caption: "A caption", IsVideo: false},
		{URL: "/files/123/download", Caption: "A clip", IsVideo: true},
		{URL: "https://example.com/c.mp4", Caption: "pasted link", IsVideo: true},
		{URL: "https://example.com/d.MOV", Caption: "", IsVideo: true},
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("photo %d = %+v, want %+v", i, got[i], w)
		}
	}
}
