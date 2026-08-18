// Package twofactor implements TOTP (RFC 6238, built on the HOTP algorithm
// from RFC 4226) two-factor authentication for the Treasurer and
// super_admin roles — the two roles that can see or move a unit's money
// once Phase 2's ledger is live.
//
// Deliberately pure standard library, zero external dependencies: a
// 6-digit HMAC-based one-time code needs nothing more than crypto/hmac,
// crypto/sha1, and encoding/base32, all in the Go standard library. This
// also means (unlike most of this codebase, which can't be fully built in
// the sandbox this was developed in — see other packages' comments on
// golang.org/x/crypto not being fetchable offline) this package alone
// could be compiled and its output checked against the official RFC 4226
// Appendix D test vectors without any network access — see the delivery
// notes for how that was done.
package twofactor

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // SHA-1 is what RFC 6238 specifies for the default TOTP algorithm; this is HMAC-SHA1 (a MAC), not using SHA-1 for collision resistance, so SHA-1's known collision weaknesses don't apply here.
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	secretBytes = 20 // 160 bits — RFC 4226's recommended HOTP secret length
	digits      = 6
	stepSeconds = 30
)

var base32Enc = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateSecret returns a new random base32-encoded TOTP shared secret
// (uppercase, unpadded — the conventional shape authenticator apps expect
// for manual entry).
func GenerateSecret() (string, error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("twofactor: generating secret: %w", err)
	}
	return base32Enc.EncodeToString(b), nil
}

// code computes the HOTP code (RFC 4226 Section 5.3) for secret at the
// given counter value; TOTP (RFC 6238) is just HOTP with the counter
// derived from the current time step instead of an incrementing number.
func code(secret string, counter uint64) (string, error) {
	key, err := base32Enc.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", fmt.Errorf("twofactor: decoding secret: %w", err)
	}

	var counterBytes [8]byte
	binary.BigEndian.PutUint64(counterBytes[:], counter)

	mac := hmac.New(sha1.New, key)
	mac.Write(counterBytes[:])
	sum := mac.Sum(nil)

	// Dynamic truncation, RFC 4226 Section 5.3.
	offset := sum[len(sum)-1] & 0x0f
	truncated := (uint32(sum[offset])&0x7f)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])

	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, truncated%mod), nil
}

// CurrentCode returns the TOTP code for right now. Only used by enrollment
// tooling to show what "the current code" looks like in tests/tooling —
// never trusted as a substitute for a user-submitted code, since anything
// computing this server-side already has the secret and doesn't need to
// prove possession of it.
func CurrentCode(secret string) (string, error) {
	return code(secret, uint64(time.Now().Unix())/stepSeconds)
}

// Verify checks a user-submitted code against secret, accepting the
// previous, current, or next 30-second step (a ±30s window) to absorb
// ordinary clock drift between the server and someone's phone. Each
// candidate is compared in constant time so response timing can't leak
// which step (if any) was close to matching.
func Verify(secret, submittedCode string) (bool, error) {
	submittedCode = strings.TrimSpace(submittedCode)
	if len(submittedCode) != digits {
		return false, nil
	}

	now := uint64(time.Now().Unix()) / stepSeconds
	steps := []uint64{now, now + 1}
	if now > 0 {
		steps = append(steps, now-1)
	}
	for _, step := range steps {
		want, err := code(secret, step)
		if err != nil {
			return false, err
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(submittedCode)) == 1 {
			return true, nil
		}
	}
	return false, nil
}

// ProvisioningURI builds the standard otpauth:// URI that authenticator
// apps can import (typically by scanning a QR code rendered from it
// elsewhere, or in apps that support it, by pasting the URI directly).
// This project deliberately doesn't render a QR code image — doing so
// well needs an image/QR-encoding library, and this sandbox has no
// network access to fetch one (see the package-level "no external
// dependency" note) — so enrollment instead shows the secret itself,
// grouped for easy manual typing, which every mainstream authenticator
// app (Google Authenticator, Authy, 1Password, Microsoft Authenticator,
// etc.) supports as an explicit "enter a setup key manually" option. The
// URI is still generated and shown as a fallback for anyone whose app can
// import from pasted text/a link.
func ProvisioningURI(issuer, accountName, secret string) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(accountName)
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", issuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", fmt.Sprintf("%d", digits))
	v.Set("period", fmt.Sprintf("%d", stepSeconds))
	return "otpauth://totp/" + label + "?" + v.Encode()
}

// FormatSecretForDisplay groups a base32 secret into 4-character chunks
// (the conventional presentation, e.g. "ABCD EFGH IJKL...") so it's
// easier to read and type correctly into an authenticator app by hand.
func FormatSecretForDisplay(secret string) string {
	var b strings.Builder
	for i, r := range secret {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// backupCodeAlphabet excludes visually ambiguous characters (0/O, 1/I) —
// these codes are meant to be written down and typed back in by hand.
const backupCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// GenerateBackupCode returns one random, human-typeable one-time recovery
// code, formatted as two hyphenated 5-character groups (e.g.
// "7HXK9-2MZQR"). Not used as cryptographic key material — just a bearer
// credential that gets bcrypt-hashed at rest like a password — so the
// small modulo bias from `byte % len(alphabet)` (33 doesn't evenly divide
// 256) is an acceptable, standard trade-off here, not a meaningful
// weakness.
func GenerateBackupCode() (string, error) {
	raw := make([]byte, 10)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("twofactor: generating backup code: %w", err)
	}
	chars := make([]byte, 10)
	for i, v := range raw {
		chars[i] = backupCodeAlphabet[int(v)%len(backupCodeAlphabet)]
	}
	return string(chars[:5]) + "-" + string(chars[5:]), nil
}
