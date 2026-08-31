package web

import "testing"

func TestProspectNotifyRecipients_SplitsAndTrims(t *testing.T) {
	got := prospectNotifyRecipients("cubmaster@example.com, membership@example.com\n\n  scoutmaster@example.com ;\n")
	want := []string{"cubmaster@example.com", "membership@example.com", "scoutmaster@example.com"}
	if len(got) != len(want) {
		t.Fatalf("got %d recipients %v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("recipient %d = %q, want %q", i, got[i], want[i])
		}
	}
	if n := len(prospectNotifyRecipients("   \n , ; \n")); n != 0 {
		t.Errorf("a list of only separators should yield no recipients, got %d", n)
	}
}

func TestHeaderSafe_StripsNewlines(t *testing.T) {
	got := headerSafe("Jamie\r\nBcc: attacker@example.com")
	if got != "Jamie  Bcc: attacker@example.com" {
		t.Errorf("CR/LF must not survive into a header, got %q", got)
	}
}
