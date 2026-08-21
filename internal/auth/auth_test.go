package auth

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestNormalizeEmail(t *testing.T) {
	cases := map[string]string{
		"  Foo@Example.COM ": "foo@example.com",
		"Bar@Example.com":    "bar@example.com",
		"baz@example.com":    "baz@example.com",
		"":                   "",
	}
	for in, want := range cases {
		if got := NormalizeEmail(in); got != want {
			t.Errorf("NormalizeEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidatePasswordComplexity(t *testing.T) {
	cases := []struct {
		password string
		wantOK   bool
	}{
		{"short1A", false},                   // under MinPasswordLength
		{"alllowercase", false},              // only 1 class (lowercase)
		{"ALLUPPERCASE", false},              // only 1 class (uppercase)
		{"12345678", false},                  // only 1 class (digits)
		{"password", false},                  // classic weak password, only 1 class
		{"lowercase123", false},              // lowercase + digit = only 2 classes
		{"Lowercase123", true},               // lower + upper + digit = 3 classes
		{"Sc0utTr00p!", true},                // lower + upper + digit + symbol = 4 classes
		{"correcthorsebatterystaple", false}, // long, but only 1 class
	}
	for _, c := range cases {
		err := ValidatePasswordComplexity(c.password)
		gotOK := err == nil
		if gotOK != c.wantOK {
			t.Errorf("ValidatePasswordComplexity(%q): got ok=%v (err=%v), want ok=%v", c.password, gotOK, err, c.wantOK)
		}
	}
}

func TestHashPassword_ValidBcryptHash(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "" {
		t.Fatal("HashPassword returned an empty hash")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("correct horse battery staple")); err != nil {
		t.Errorf("bcrypt hash does not verify against the original password: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("wrong password")); err == nil {
		t.Error("bcrypt hash verified against the wrong password")
	}
}

func TestVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	u := User{PasswordHash: hash}

	if !VerifyPassword(u, "correct horse battery staple") {
		t.Error("VerifyPassword rejected the correct password")
	}
	if VerifyPassword(u, "wrong password") {
		t.Error("VerifyPassword accepted an incorrect password")
	}
	if VerifyPassword(u, "") {
		t.Error("VerifyPassword accepted an empty password")
	}
}

func TestHashPassword_SaltsDifferently(t *testing.T) {
	h1, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	h2, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if h1 == h2 {
		t.Error("hashing the same password twice produced identical hashes (bcrypt should salt each call)")
	}
}

func TestRandomToken_LengthAndUniqueness(t *testing.T) {
	a, err := RandomToken(32)
	if err != nil {
		t.Fatalf("RandomToken: %v", err)
	}
	b, err := RandomToken(32)
	if err != nil {
		t.Fatalf("RandomToken: %v", err)
	}
	if a == b {
		t.Error("two calls to RandomToken produced the same value")
	}
	// base64 URL-safe raw encoding: no padding, no '+' or '/'.
	for _, c := range []string{a, b} {
		if strings.ContainsAny(c, "+/=") {
			t.Errorf("RandomToken output %q contains non-URL-safe characters", c)
		}
	}
}

func TestGenerateTemporaryPassword_NonEmptyAndUnique(t *testing.T) {
	a, err := GenerateTemporaryPassword()
	if err != nil {
		t.Fatalf("GenerateTemporaryPassword: %v", err)
	}
	b, err := GenerateTemporaryPassword()
	if err != nil {
		t.Fatalf("GenerateTemporaryPassword: %v", err)
	}
	if a == "" {
		t.Fatal("GenerateTemporaryPassword returned an empty string")
	}
	if a == b {
		t.Error("two calls to GenerateTemporaryPassword produced the same value")
	}
}
