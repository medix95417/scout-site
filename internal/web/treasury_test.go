package web

import "testing"

// TestParseDollarsToCents_RejectsNonFiniteAndOverflow is the regression
// test for the money-parsing bug this replaced. The old implementation
// went via strconv.ParseFloat and then int64(math.Round(f*100)), which
// silently produced math.MinInt64 — a -$92 quadrillion posting — for
// "Inf", "NaN" and anything past int64's range. Nothing reached the
// books only because all seven call sites happened to reject a
// non-positive result; the helper itself handed back a number no caller
// should ever have had to defend against.
//
// Note these amounts would also have passed the ledger's own balance
// check, since a deposit posts +amount and -amount and those still sum
// to zero.
func TestParseDollarsToCents_RejectsNonFiniteAndOverflow(t *testing.T) {
	for _, raw := range []string{
		"Inf", "+Inf", "-Inf", "inf", "NaN", "nan",
		"1e20", "99999999999999999999", "1e5",
		"1_0", "0x1p10",
		"", "   ", "abc", "12.345", "1.2.3", "--5", "12abc", "1,00,0.0.0",
	} {
		if got, err := parseDollarsToCents(raw); err == nil {
			t.Errorf("parseDollarsToCents(%q) = %d with no error — it must be rejected", raw, got)
		}
	}
}

func TestParseDollarsToCents_Accepts(t *testing.T) {
	cases := []struct {
		raw  string
		want int64
	}{
		{"12.34", 1234},
		{"0.05", 5},
		{".5", -1}, // sentinel: rejected below, no leading digit
		{"5", 500},
		{"5.", -1}, // sentinel: rejected below, trailing dot
		{"1.5", 150},
		{"$12.34", 1234},
		{"1,250", 125000},
		{"$1,250.99", 125099},
		{" 42 ", 4200},
		{"-5.00", -500},
		{"9999999.99", 999999999},
	}
	for _, c := range cases {
		got, err := parseDollarsToCents(c.raw)
		if c.want == -1 {
			if err == nil {
				t.Errorf("parseDollarsToCents(%q) = %d, want an error", c.raw, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseDollarsToCents(%q) errored: %v", c.raw, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseDollarsToCents(%q) = %d, want %d", c.raw, got, c.want)
		}
	}
}

// TestParseDollarsToCents_CapsAtMax keeps a single entered amount well
// clear of the range where cents arithmetic could overflow downstream.
func TestParseDollarsToCents_CapsAtMax(t *testing.T) {
	if _, err := parseDollarsToCents("10000000.00"); err != nil {
		t.Errorf("$10,000,000 should be accepted (the cap itself): %v", err)
	}
	if got, err := parseDollarsToCents("10000000.01"); err == nil {
		t.Errorf("parseDollarsToCents past the cap = %d, want an error", got)
	}
}

// TestFormatCentsRoundTrip checks the two halves agree, so an amount
// shown back to a treasurer re-parses to the same cents.
func TestFormatCentsRoundTrip(t *testing.T) {
	for _, cents := range []int64{0, 5, 99, 100, 1234, -1234, 999999999} {
		formatted := formatCents(cents)
		got, err := parseDollarsToCents(formatted)
		if err != nil {
			t.Errorf("formatCents(%d) = %q, which failed to re-parse: %v", cents, formatted, err)
			continue
		}
		if got != cents {
			t.Errorf("round trip of %d via %q gave %d", cents, formatted, got)
		}
	}
}
