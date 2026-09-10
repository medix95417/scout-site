package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/db"
	"github.com/47-yonkers/scout-site/internal/twofactor"
)

// Integration tests for the security-key half of two-factor, against a
// real database, in the same shape as internal/roster's. They skip
// without TEST_DATABASE_URL.
//
// The WebAuthn ceremony itself is go-webauthn's and is verified end to
// end in a browser (see the pull request that added this); what is
// tested here is what this package decides — when backup codes are
// issued and removed, when a login counts as having a second factor,
// and that a challenge answers once.

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping auth integration tests")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// newLogin creates a family login and returns its id.
func newLogin(t *testing.T, pool *pgxpool.Pool) User {
	t.Helper()
	ctx := context.Background()
	email := fmt.Sprintf("sk-%d@example.test", time.Now().UnixNano())
	var familyID string
	if err := pool.QueryRow(ctx, `INSERT INTO families (name) VALUES ('Key Family') RETURNING id`).Scan(&familyID); err != nil {
		t.Fatal(err)
	}
	hash, _ := HashPassword("Sc0utTr00p!")
	var id string
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (family_id, email, password_hash) VALUES ($1, $2, $3) RETURNING id`, familyID, email, hash,
	).Scan(&id); err != nil {
		t.Fatal(err)
	}
	u, err := UserByID(ctx, pool, id)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// fakeCredential stands in for what the library returns from a verified
// registration. Only the fields this package stores matter.
func fakeCredential(seed string) *webauthn.Credential {
	return &webauthn.Credential{
		ID:              []byte("cred-" + seed),
		PublicKey:       []byte("pk-" + seed),
		AttestationType: "none",
		Authenticator:   webauthn.Authenticator{AAGUID: []byte("aaguid"), SignCount: 3},
	}
}

func backupCodeCount(t *testing.T, pool *pgxpool.Pool, userID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM totp_backup_codes WHERE user_id = $1 AND used_at IS NULL`, userID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// confirmTOTP enrols and confirms an authenticator app on the login.
func confirmTOTP(t *testing.T, pool *pgxpool.Pool, u User) []string {
	t.Helper()
	ctx := context.Background()
	secret, err := BeginTOTPEnrollment(ctx, pool, u, "Sc0utTr00p!")
	if err != nil {
		t.Fatalf("begin totp: %v", err)
	}
	code, err := twofactor.CurrentCode(secret)
	if err != nil {
		t.Fatal(err)
	}
	codes, err := ConfirmTOTPEnrollment(ctx, pool, u.ID, code)
	if err != nil {
		t.Fatalf("confirm totp: %v", err)
	}
	return codes
}

// TestSecurityKey_FirstFactorIssuesBackupCodes: the first second factor
// of either kind brings backup codes with it; a second key does not
// replace them, since the person may have written the first batch down.
func TestSecurityKey_FirstFactorIssuesBackupCodes(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	u := newLogin(t, pool)

	if has, _ := HasSecondFactor(ctx, pool, u.ID); has {
		t.Fatal("a fresh login should have no second factor")
	}
	codes, err := AddSecurityKey(ctx, pool, u.ID, "blue YubiKey", fakeCredential("a"))
	if err != nil {
		t.Fatalf("adding first key: %v", err)
	}
	if len(codes) != NumBackupCodes {
		t.Errorf("first key should issue %d backup codes, got %d", NumBackupCodes, len(codes))
	}
	if has, _ := HasSecondFactor(ctx, pool, u.ID); !has {
		t.Error("a login with a key has a second factor")
	}

	more, err := AddSecurityKey(ctx, pool, u.ID, "spare", fakeCredential("b"))
	if err != nil {
		t.Fatalf("adding second key: %v", err)
	}
	if more != nil {
		t.Error("a second key must not reissue backup codes")
	}
	if n := backupCodeCount(t, pool, u.ID); n != NumBackupCodes {
		t.Errorf("backup codes after second key = %d, want %d untouched", n, NumBackupCodes)
	}

	// The same authenticator cannot be registered twice, on any login.
	other := newLogin(t, pool)
	if _, err := AddSecurityKey(ctx, pool, other.ID, "stolen", fakeCredential("a")); !errors.Is(err, ErrSecurityKeyAlreadyRegistered) {
		t.Errorf("re-registering a credential id should fail with ErrSecurityKeyAlreadyRegistered, got %v", err)
	}

	keys, err := ListSecurityKeys(ctx, pool, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0].Name != "blue YubiKey" || keys[1].Name != "spare" {
		t.Errorf("ListSecurityKeys = %+v", keys)
	}
}

// TestSecurityKey_BackupCodesOutliveOneFactor: codes go only when the
// last factor of either kind goes.
func TestSecurityKey_BackupCodesOutliveOneFactor(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	// Key first, then app: confirming the app keeps the key's batch.
	u := newLogin(t, pool)
	first, _ := AddSecurityKey(ctx, pool, u.ID, "key", fakeCredential("c"))
	if codes := confirmTOTP(t, pool, u); codes != nil {
		t.Error("confirming an app on a login that already has a key must not reissue backup codes")
	}
	if n := backupCodeCount(t, pool, u.ID); n != len(first) {
		t.Errorf("codes after confirming app = %d, want %d", n, len(first))
	}

	// Remove the key: the app remains, so the codes remain.
	keys, _ := ListSecurityKeys(ctx, pool, u.ID)
	if err := RemoveSecurityKey(ctx, pool, u.ID, keys[0].ID); err != nil {
		t.Fatal(err)
	}
	if n := backupCodeCount(t, pool, u.ID); n != NumBackupCodes {
		t.Errorf("codes after removing the key with an app still confirmed = %d, want %d", n, NumBackupCodes)
	}
	// Disable the app: nothing left, codes go.
	if err := DisableTOTP(ctx, pool, u.ID); err != nil {
		t.Fatal(err)
	}
	if n := backupCodeCount(t, pool, u.ID); n != 0 {
		t.Errorf("codes after the last factor went = %d, want 0", n)
	}
	if has, _ := HasSecondFactor(ctx, pool, u.ID); has {
		t.Error("no factor should remain")
	}

	// App first, then key: disabling the app keeps the codes while the key stays.
	v := newLogin(t, pool)
	confirmTOTP(t, pool, v)
	if _, err := AddSecurityKey(ctx, pool, v.ID, "key", fakeCredential("d")); err != nil {
		t.Fatal(err)
	}
	if err := DisableTOTP(ctx, pool, v.ID); err != nil {
		t.Fatal(err)
	}
	if n := backupCodeCount(t, pool, v.ID); n != NumBackupCodes {
		t.Errorf("codes after disabling the app with a key still registered = %d, want %d", n, NumBackupCodes)
	}

	// Removing a key that is not yours is "not found", not a delete.
	stranger := newLogin(t, pool)
	vkeys, _ := ListSecurityKeys(ctx, pool, v.ID)
	if err := RemoveSecurityKey(ctx, pool, stranger.ID, vkeys[0].ID); !errors.Is(err, ErrSecurityKeyNotFound) {
		t.Errorf("removing another login's key: got %v, want ErrSecurityKeyNotFound", err)
	}
	if remaining, _ := ListSecurityKeys(ctx, pool, v.ID); len(remaining) != 1 {
		t.Error("another login's removal attempt deleted the key")
	}
}

// TestSecurityKey_StepUpOncePresent: once a key exists, enrolling an app
// needs the password, same as re-enrolling over a confirmed app did.
func TestSecurityKey_StepUpOncePresent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	u := newLogin(t, pool)
	if _, err := AddSecurityKey(ctx, pool, u.ID, "key", fakeCredential("e")); err != nil {
		t.Fatal(err)
	}
	if _, err := BeginTOTPEnrollment(ctx, pool, u, ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("enrolling an app with a key present and no password: got %v, want ErrInvalidCredentials", err)
	}
	if _, err := BeginTOTPEnrollment(ctx, pool, u, "Sc0utTr00p!"); err != nil {
		t.Errorf("enrolling with the right password: %v", err)
	}
}

// TestVerifySecondFactorCode_BackupCodeForKeyOnlyLogin: the backup code
// path must work for someone whose only factor is a key, and must not
// exist for someone with no factor.
func TestVerifySecondFactorCode_BackupCodeForKeyOnlyLogin(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	u := newLogin(t, pool)
	codes, err := AddSecurityKey(ctx, pool, u.ID, "key", fakeCredential("f"))
	if err != nil {
		t.Fatal(err)
	}
	ok, err := VerifySecondFactorCode(ctx, pool, u.ID, codes[0])
	if err != nil || !ok {
		t.Errorf("a backup code should verify for a key-only login: ok=%v err=%v", ok, err)
	}
	ok, err = VerifySecondFactorCode(ctx, pool, u.ID, codes[0])
	if err != nil || ok {
		t.Errorf("a backup code is single use: second try ok=%v err=%v", ok, err)
	}
	if ok, err := VerifySecondFactorCode(ctx, pool, u.ID, "123456"); err != nil || ok {
		t.Errorf("a six-digit guess must not pass a key-only login: ok=%v err=%v", ok, err)
	}

	none := newLogin(t, pool)
	if _, err := VerifySecondFactorCode(ctx, pool, none.ID, "anything"); !errors.Is(err, ErrTOTPNotEnrolled) {
		t.Errorf("a login with no factor: got %v, want ErrTOTPNotEnrolled", err)
	}
}

// TestWebAuthnSession_AnswersOnce: the stored challenge is consumed by
// the read, replaced by a new begin, and gone once expired.
func TestWebAuthnSession_AnswersOnce(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	u := newLogin(t, pool)

	s1 := &webauthn.SessionData{Challenge: "one", UserID: []byte(u.ID), Expires: time.Now().Add(time.Minute)}
	if err := SaveWebAuthnSession(ctx, pool, u.ID, WebAuthnRegister, s1); err != nil {
		t.Fatal(err)
	}
	s2 := &webauthn.SessionData{Challenge: "two", UserID: []byte(u.ID), Expires: time.Now().Add(time.Minute)}
	if err := SaveWebAuthnSession(ctx, pool, u.ID, WebAuthnRegister, s2); err != nil {
		t.Fatal(err)
	}
	got, err := TakeWebAuthnSession(ctx, pool, u.ID, WebAuthnRegister)
	if err != nil {
		t.Fatal(err)
	}
	if got.Challenge != "two" {
		t.Errorf("a second begin should supersede the first: got challenge %q", got.Challenge)
	}
	if _, err := TakeWebAuthnSession(ctx, pool, u.ID, WebAuthnRegister); !errors.Is(err, ErrNoWebAuthnSession) {
		t.Errorf("a taken session must not be takeable again: got %v", err)
	}

	// Kinds are separate: a login challenge does not answer a registration.
	if err := SaveWebAuthnSession(ctx, pool, u.ID, WebAuthnLogin, s1); err != nil {
		t.Fatal(err)
	}
	if _, err := TakeWebAuthnSession(ctx, pool, u.ID, WebAuthnRegister); !errors.Is(err, ErrNoWebAuthnSession) {
		t.Errorf("a login session must not satisfy a registration take: got %v", err)
	}

	// Expired rows are absent.
	if _, err := pool.Exec(ctx, `UPDATE webauthn_sessions SET expires_at = now() - interval '1 second' WHERE user_id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := TakeWebAuthnSession(ctx, pool, u.ID, WebAuthnLogin); !errors.Is(err, ErrNoWebAuthnSession) {
		t.Errorf("an expired session must be absent: got %v", err)
	}
}

// TestPendingTwoFactorLogin_KeyPath: the helpers the security-key login
// uses — read without consuming, count failures under the cap, consume
// exactly once.
func TestPendingTwoFactorLogin_KeyPath(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	u := newLogin(t, pool)

	token, _, err := CreatePendingTwoFactorLogin(ctx, pool, u.ID, "/treasury")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		userID, next, err := PendingTwoFactorLoginOwner(ctx, pool, token)
		if err != nil || userID != u.ID || next != "/treasury" {
			t.Fatalf("owner read %d: %s %s %v", i, userID, next, err)
		}
	}
	for i := 0; i < maxPendingTwoFactorAttempts; i++ {
		if err := RecordPendingTwoFactorFailure(ctx, pool, token); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := PendingTwoFactorLoginOwner(ctx, pool, token); !errors.Is(err, ErrInvalidPendingLogin) {
		t.Errorf("after %d failures the pending login must be unusable: got %v", maxPendingTwoFactorAttempts, err)
	}

	token2, _, _ := CreatePendingTwoFactorLogin(ctx, pool, u.ID, "/")
	if err := ConsumePendingTwoFactorLogin(ctx, pool, token2); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if err := ConsumePendingTwoFactorLogin(ctx, pool, token2); !errors.Is(err, ErrInvalidPendingLogin) {
		t.Errorf("second consume must fail: got %v", err)
	}
	if _, _, err := PendingTwoFactorLoginOwner(ctx, pool, token2); !errors.Is(err, ErrInvalidPendingLogin) {
		t.Errorf("a consumed pending login must not be readable: got %v", err)
	}
}
