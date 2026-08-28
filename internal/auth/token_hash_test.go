package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// TestHashToken_IsWhatGoesInTheDatabase covers the property the change
// exists for: what's stored must not be the token itself, so a leaked
// backup or a dump shared for debugging yields nothing replayable.
func TestHashToken_IsWhatGoesInTheDatabase(t *testing.T) {
	token, err := RandomToken(32)
	if err != nil {
		t.Fatalf("RandomToken: %v", err)
	}

	stored := hashToken(token)
	if stored == token {
		t.Fatal("hashToken returned the token unchanged — the whole point is that the stored value differs")
	}
	if strings.Contains(stored, token) {
		t.Error("the stored value still contains the token")
	}

	want := sha256.Sum256([]byte(token))
	if stored != hex.EncodeToString(want[:]) {
		t.Errorf("hashToken(%q) = %q, want the hex SHA-256 of the token", token, stored)
	}
	if len(stored) != 64 {
		t.Errorf("stored hash is %d chars, want 64 hex chars", len(stored))
	}
}

// TestHashToken_DeterministicAndDistinct — lookups work by hashing the
// presented token and matching, so the same token must always hash the
// same way, and different tokens must not collide.
func TestHashToken_DeterministicAndDistinct(t *testing.T) {
	if hashToken("abc") != hashToken("abc") {
		t.Error("hashToken is not deterministic — session lookup would fail at random")
	}

	seen := map[string]string{}
	for i := 0; i < 200; i++ {
		token, err := RandomToken(32)
		if err != nil {
			t.Fatalf("RandomToken: %v", err)
		}
		h := hashToken(token)
		if prev, dup := seen[h]; dup {
			t.Fatalf("two distinct tokens hashed alike: %q and %q", prev, token)
		}
		seen[h] = token
	}
}

// TestRandomToken_IsUnguessable is the assumption that lets plain SHA-256
// be the right choice here rather than bcrypt: the input already carries
// full entropy, so there's nothing to brute-force.
func TestRandomToken_IsUnguessable(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		token, err := RandomToken(32)
		if err != nil {
			t.Fatalf("RandomToken: %v", err)
		}
		if seen[token] {
			t.Fatal("RandomToken repeated a value — it must not be predictable")
		}
		seen[token] = true
		// 32 bytes base64url with no padding.
		if len(token) < 40 {
			t.Errorf("token %q is shorter than expected for 32 bytes of entropy", token)
		}
	}
}
