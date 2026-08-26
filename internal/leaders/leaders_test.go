package leaders

import "testing"

func TestNormalizePhotoFocus(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"top", "top"},
		{"bottom", "bottom"},
		{"center", "center"},
		{"", "center"},
		{"TOP", "center"}, // case-sensitive — anything not an exact match falls back
		{"left", "center"},
	}
	for _, c := range cases {
		if got := NormalizePhotoFocus(c.in); got != c.want {
			t.Errorf("NormalizePhotoFocus(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
