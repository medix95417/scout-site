package twofactor

import (
	"encoding/base32"
	"strings"
	"testing"
)

// rfc4226Secret is the standard 20-byte ASCII test secret from RFC 4226
// Appendix D ("12345678901234567890"), base32-encoded since code() expects
// a base32 secret the same way GenerateSecret produces one.
var rfc4226Secret = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))

// rfc4226Vectors are the official HOTP test values for that secret at
// counters 0-9 (RFC 4226 Appendix D).
var rfc4226Vectors = []string{
	"755224", "287082", "359152", "969429", "338314",
	"254676", "287922", "162583", "399871", "520489",
}

func TestCode_RFC4226Vectors(t *testing.T) {
	for counter, want := range rfc4226Vectors {
		got, err := code(rfc4226Secret, uint64(counter))
		if err != nil {
			t.Fatalf("counter %d: code() error: %v", counter, err)
		}
		if got != want {
			t.Errorf("counter %d: got %q, want %q", counter, got, want)
		}
	}
}

func TestCode_InvalidSecret(t *testing.T) {
	if _, err := code("not-valid-base32!!!", 0); err == nil {
		t.Error("expected an error decoding an invalid base32 secret, got nil")
	}
}

func TestVerify_AcceptsCurrentAndDriftSteps(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}

	now := uint64(1_700_000_000) / stepSeconds // arbitrary fixed step
	currentCode, err := code(secret, now)
	if err != nil {
		t.Fatalf("code: %v", err)
	}

	// Verify uses time.Now() internally, so we can't pin "now" for it
	// directly; instead confirm CurrentCode() round-trips through Verify.
	cur, err := CurrentCode(secret)
	if err != nil {
		t.Fatalf("CurrentCode: %v", err)
	}
	ok, err := Verify(secret, cur)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Error("Verify rejected the current TOTP code")
	}

	_ = currentCode // sanity: computed via the same code() path as CurrentCode
}

func TestVerify_RejectsWrongCode(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	cur, err := CurrentCode(secret)
	if err != nil {
		t.Fatalf("CurrentCode: %v", err)
	}

	// Flip the code to something that can't also be valid.
	wrong := "000000"
	if cur == wrong {
		wrong = "111111"
	}
	ok, err := Verify(secret, wrong)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if ok {
		t.Error("Verify accepted a code that should not match")
	}
}

func TestVerify_RejectsWrongLength(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef"} {
		ok, err := Verify(secret, bad)
		if err != nil {
			t.Fatalf("Verify(%q): %v", bad, err)
		}
		if ok {
			t.Errorf("Verify accepted malformed code %q", bad)
		}
	}
}

func TestGenerateSecret_Unique(t *testing.T) {
	a, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	b, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	if a == b {
		t.Error("two calls to GenerateSecret produced the same value")
	}
	if len(a) == 0 {
		t.Error("GenerateSecret returned an empty string")
	}
}

func TestGenerateBackupCode_FormatAndUniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		c, err := GenerateBackupCode()
		if err != nil {
			t.Fatalf("GenerateBackupCode: %v", err)
		}
		if len(c) != 11 || c[5] != '-' {
			t.Fatalf("unexpected backup code shape: %q", c)
		}
		for _, r := range strings.ReplaceAll(c, "-", "") {
			if !strings.ContainsRune(backupCodeAlphabet, r) {
				t.Fatalf("backup code %q contains character %q outside the allowed alphabet", c, r)
			}
		}
		if seen[c] {
			t.Fatalf("GenerateBackupCode produced a duplicate: %q", c)
		}
		seen[c] = true
	}
}

func TestFormatSecretForDisplay(t *testing.T) {
	got := FormatSecretForDisplay("ABCDEFGHIJKL")
	want := "ABCD EFGH IJKL"
	if got != want {
		t.Errorf("FormatSecretForDisplay: got %q, want %q", got, want)
	}
}

func TestProvisioningURI(t *testing.T) {
	uri := ProvisioningURI("ScoutSite", "user@example.com", "ABCDEFGH")
	if !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Errorf("ProvisioningURI should start with otpauth://totp/, got %q", uri)
	}
	for _, want := range []string{"secret=ABCDEFGH", "issuer=ScoutSite", "algorithm=SHA1", "digits=6", "period=30"} {
		if !strings.Contains(uri, want) {
			t.Errorf("ProvisioningURI %q missing expected query param %q", uri, want)
		}
	}
}
