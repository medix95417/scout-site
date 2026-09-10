package web

import "testing"

func TestCSVCell(t *testing.T) {
	cases := map[string]string{
		"Jamie Rivera":          "Jamie Rivera",
		"":                      "",
		"=HYPERLINK(\"x\")":     "'=HYPERLINK(\"x\")",
		"+1 555 0100":           "'+1 555 0100",
		"-3":                    "'-3",
		"@SUM(A1)":              "'@SUM(A1)",
		"\tcmd":                 "'\tcmd",
		"O'Brien":               "O'Brien",
		"reset_family_password": "reset_family_password",
	}
	for in, want := range cases {
		if got := csvCell(in); got != want {
			t.Errorf("csvCell(%q) = %q, want %q", in, got, want)
		}
	}
}
