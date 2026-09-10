package auth

// Security keys as a second factor: the database-backed half of WebAuthn,
// in the same shape as totp.go. A YubiKey, or the passkey a phone or
// laptop offers, holds a private key; we keep the public half and the
// credential id, hand the browser a challenge, and check the signature
// that comes back. The ceremony itself — challenge, attestation, client
// data, signature verification — is go-webauthn's; this file owns what
// is stored, and the rules about backup codes that both kinds of factor
// now share.
//
// Two rules are worth stating up front because they are easy to get
// wrong in either direction:
//
//   - Backup codes belong to the account, not to the authenticator app.
//     They are generated when the first second factor of either kind is
//     confirmed, and deleted only when the last one is removed. A person
//     with a security key and no app still needs a way in when the key
//     is in a drawer at home.
//   - A challenge answers exactly once. The server-side session behind a
//     ceremony is deleted the moment it is read, so a captured response
//     cannot be replayed against it.

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/47-yonkers/scout-site/internal/twofactor"
)

// SecurityKey is one registered authenticator.
type SecurityKey struct {
	ID             string
	UserID         string
	Name           string
	CredentialID   []byte
	PublicKey      []byte
	Attestation    string
	Transports     []string
	AAGUID         []byte
	SignCount      uint32
	BackupEligible bool
	BackupState    bool
	CreatedAt      time.Time
	LastUsedAt     *time.Time
}

// ErrSecurityKeyNotFound is returned when a key id does not belong to the
// user asking — the same answer whether it never existed or is somebody
// else's.
var ErrSecurityKeyNotFound = errors.New("auth: no such security key on this login")

// ErrSecurityKeyAlreadyRegistered is returned when the authenticator the
// browser presents is already on this site, under any login. The browser
// normally refuses this itself via the exclusion list, so reaching the
// server with it means the list was bypassed.
var ErrSecurityKeyAlreadyRegistered = errors.New("auth: that security key is already registered")

// WebAuthnSessionDuration bounds a ceremony: the time between handing the
// browser a challenge and receiving the signed answer. Long enough to
// find the key and touch it; short enough that an abandoned challenge is
// not sitting around.
const WebAuthnSessionDuration = 5 * time.Minute

const (
	// WebAuthnRegister and WebAuthnLogin name the two ceremonies a
	// session row can belong to.
	WebAuthnRegister = "register"
	WebAuthnLogin    = "login"
)

// ListSecurityKeys returns a user's keys, oldest first — the order they
// were added is the order a person remembers them in.
func ListSecurityKeys(ctx context.Context, pool *pgxpool.Pool, userID string) ([]SecurityKey, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, user_id, name, credential_id, public_key, attestation_type, transports,
		       aaguid, sign_count, backup_eligible, backup_state, created_at, last_used_at
		FROM security_keys WHERE user_id = $1 ORDER BY created_at, id
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []SecurityKey
	for rows.Next() {
		var k SecurityKey
		var signCount int64
		if err := rows.Scan(&k.ID, &k.UserID, &k.Name, &k.CredentialID, &k.PublicKey, &k.Attestation, &k.Transports,
			&k.AAGUID, &signCount, &k.BackupEligible, &k.BackupState, &k.CreatedAt, &k.LastUsedAt); err != nil {
			return nil, err
		}
		k.SignCount = uint32(signCount)
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// webauthnCredential is the library's view of a stored key.
func (k SecurityKey) webauthnCredential() webauthn.Credential {
	transports := make([]protocol.AuthenticatorTransport, 0, len(k.Transports))
	for _, t := range k.Transports {
		transports = append(transports, protocol.AuthenticatorTransport(t))
	}
	return webauthn.Credential{
		ID:              k.CredentialID,
		PublicKey:       k.PublicKey,
		AttestationType: k.Attestation,
		Transport:       transports,
		Flags:           webauthn.CredentialFlags{BackupEligible: k.BackupEligible, BackupState: k.BackupState},
		Authenticator:   webauthn.Authenticator{AAGUID: k.AAGUID, SignCount: k.SignCount},
	}
}

// WebAuthnUser is the shape go-webauthn wants a user in. It carries the
// login and every key the login holds, so the library can build an
// exclusion list at registration and an allow list at login.
//
// The user handle is the login's id. It is what the authenticator stores
// and returns, and the library checks it against the login the pending
// cookie identifies — so a key registered on one login cannot answer for
// another even if both were somehow in the same allow list.
type WebAuthnUser struct {
	User User
	Keys []SecurityKey
}

// LoadWebAuthnUser builds the adapter for a login id.
func LoadWebAuthnUser(ctx context.Context, pool *pgxpool.Pool, userID string) (WebAuthnUser, error) {
	u, err := UserByID(ctx, pool, userID)
	if err != nil {
		return WebAuthnUser{}, err
	}
	keys, err := ListSecurityKeys(ctx, pool, userID)
	if err != nil {
		return WebAuthnUser{}, err
	}
	return WebAuthnUser{User: u, Keys: keys}, nil
}

func (w WebAuthnUser) WebAuthnID() []byte { return []byte(w.User.ID) }

// WebAuthnName and WebAuthnDisplayName are what an authenticator shows
// next to a stored credential. The email is the one thing every login
// has that a person will recognise as theirs.
func (w WebAuthnUser) WebAuthnName() string        { return w.User.Email }
func (w WebAuthnUser) WebAuthnDisplayName() string { return w.User.Email }

func (w WebAuthnUser) WebAuthnCredentials() []webauthn.Credential {
	creds := make([]webauthn.Credential, 0, len(w.Keys))
	for _, k := range w.Keys {
		creds = append(creds, k.webauthnCredential())
	}
	return creds
}

// AddSecurityKey stores a credential the library has just verified, under
// the name its owner gave it. If this is the login's first second factor
// of either kind, a batch of backup codes is generated and returned in
// plaintext — the caller shows them once, exactly as ConfirmTOTPEnrollment
// does. Otherwise backupCodes is nil and the existing batch stands.
func AddSecurityKey(ctx context.Context, pool *pgxpool.Pool, userID, name string, cred *webauthn.Credential) (backupCodes []string, err error) {
	if name == "" {
		name = "Security key"
	}
	transports := make([]string, 0, len(cred.Transport))
	for _, t := range cred.Transport {
		transports = append(transports, string(t))
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op if already committed

	// Decided before the insert, since after it there is always a key.
	had, err := hasSecondFactorTx(ctx, tx, userID)
	if err != nil {
		return nil, err
	}

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM security_keys WHERE credential_id = $1)`, cred.ID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrSecurityKeyAlreadyRegistered
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO security_keys (user_id, name, credential_id, public_key, attestation_type, transports,
		                           aaguid, sign_count, backup_eligible, backup_state)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, userID, name, cred.ID, cred.PublicKey, cred.AttestationType, transports,
		cred.Authenticator.AAGUID, int64(cred.Authenticator.SignCount), cred.Flags.BackupEligible, cred.Flags.BackupState,
	); err != nil {
		return nil, err
	}

	if !had {
		if backupCodes, err = issueBackupCodesTx(ctx, tx, userID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return backupCodes, nil
}

// RemoveSecurityKey deletes one of the user's keys. If nothing is left —
// no keys and no confirmed authenticator app — the backup codes go too,
// since there is no longer a second step for them to stand in for.
func RemoveSecurityKey(ctx context.Context, pool *pgxpool.Pool, userID, keyID string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op if already committed

	tag, err := tx.Exec(ctx, `DELETE FROM security_keys WHERE id = $1 AND user_id = $2`, keyID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSecurityKeyNotFound
	}
	still, err := hasSecondFactorTx(ctx, tx, userID)
	if err != nil {
		return err
	}
	if !still {
		if _, err := tx.Exec(ctx, `DELETE FROM totp_backup_codes WHERE user_id = $1`, userID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// RecordSecurityKeyUse writes back what a successful login learned: the
// new signature counter and backup state, and when. A counter that went
// backwards is the library's clone warning; it is recorded rather than
// refused, because the common cause is a key that does not implement
// counters at all, and a login that fails for a reason nobody can act
// on is worse than a log line.
func RecordSecurityKeyUse(ctx context.Context, pool *pgxpool.Pool, credentialID []byte, signCount uint32, backupState bool) error {
	_, err := pool.Exec(ctx, `
		UPDATE security_keys SET sign_count = $1, backup_state = $2, last_used_at = now() WHERE credential_id = $3
	`, int64(signCount), backupState, credentialID)
	return err
}

// SecondFactorStatus reports what a login has: a confirmed authenticator
// app, and how many security keys. Either is enough to require the
// second step at login; the settings page shows both.
func SecondFactorStatus(ctx context.Context, pool *pgxpool.Pool, userID string) (totpConfirmed bool, keys int, err error) {
	err = pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM totp_credentials WHERE user_id = $1 AND confirmed_at IS NOT NULL),
		       (SELECT count(*) FROM security_keys WHERE user_id = $1)
	`, userID).Scan(&totpConfirmed, &keys)
	return totpConfirmed, keys, err
}

// HasSecondFactor is the one question the login flow asks: does this
// login need a second step before a session is issued. Every path that
// creates a session after a password check goes through this rather than
// TOTPStatus, or a login protected only by a security key would be let
// straight in.
func HasSecondFactor(ctx context.Context, pool *pgxpool.Pool, userID string) (bool, error) {
	totp, keys, err := SecondFactorStatus(ctx, pool, userID)
	return totp || keys > 0, err
}

func hasSecondFactorTx(ctx context.Context, tx pgx.Tx, userID string) (bool, error) {
	var has bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM totp_credentials WHERE user_id = $1 AND confirmed_at IS NOT NULL)
		    OR EXISTS(SELECT 1 FROM security_keys WHERE user_id = $1)
	`, userID).Scan(&has)
	return has, err
}

// issueBackupCodesTx replaces the user's backup codes with a fresh batch
// and returns the plaintexts. Shared by TOTP confirmation and first-key
// registration so both produce the same thing.
func issueBackupCodesTx(ctx context.Context, tx pgx.Tx, userID string) ([]string, error) {
	if _, err := tx.Exec(ctx, `DELETE FROM totp_backup_codes WHERE user_id = $1`, userID); err != nil {
		return nil, err
	}
	codes := make([]string, 0, NumBackupCodes)
	for i := 0; i < NumBackupCodes; i++ {
		plain, err := twofactor.GenerateBackupCode()
		if err != nil {
			return nil, err
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO totp_backup_codes (user_id, code_hash) VALUES ($1, $2)`, userID, string(hash),
		); err != nil {
			return nil, err
		}
		codes = append(codes, plain)
	}
	return codes, nil
}

// SaveWebAuthnSession stores the server half of a ceremony, replacing any
// earlier one of the same kind for this login — a second "add a key"
// click supersedes the first rather than leaving two challenges live.
func SaveWebAuthnSession(ctx context.Context, pool *pgxpool.Pool, userID, kind string, session *webauthn.SessionData) error {
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO webauthn_sessions (user_id, kind, data, expires_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, kind) DO UPDATE SET data = EXCLUDED.data, expires_at = EXCLUDED.expires_at
	`, userID, kind, data, time.Now().Add(WebAuthnSessionDuration))
	return err
}

// ErrNoWebAuthnSession means there is no live challenge to answer —
// never issued, expired, or already answered.
var ErrNoWebAuthnSession = errors.New("auth: no security-key challenge is waiting for an answer")

// TakeWebAuthnSession returns the stored session and deletes it in the
// same statement, so a challenge can be answered once. An expired row is
// treated as absent.
func TakeWebAuthnSession(ctx context.Context, pool *pgxpool.Pool, userID, kind string) (*webauthn.SessionData, error) {
	var data []byte
	err := pool.QueryRow(ctx, `
		DELETE FROM webauthn_sessions WHERE user_id = $1 AND kind = $2 AND expires_at > now() RETURNING data
	`, userID, kind).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoWebAuthnSession
	}
	if err != nil {
		return nil, err
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, err
	}
	return &session, nil
}
