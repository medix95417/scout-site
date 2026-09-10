-- Security keys as a second factor, alongside the authenticator app.
--
-- A security key is a WebAuthn/FIDO2 credential: a YubiKey or similar,
-- or the passkey a phone or laptop offers. The browser holds the private
-- half; we keep the public key and the credential id the authenticator
-- minted, and at login the authenticator signs our challenge. Nothing
-- here is secret in the way a TOTP seed is — a leaked public key
-- verifies signatures and cannot make them — which is one of the
-- reasons to offer this at all.
--
-- One person may hold several keys (a YubiKey on the keyring and one in
-- the drawer is the setup every vendor recommends), and may hold keys
-- alongside a TOTP enrollment. Either kind satisfies the second step at
-- login. totp_backup_codes (0007) now backs both: it is generated when
-- the first factor of either kind is confirmed and removed when the last
-- one goes, see internal/auth.
CREATE TABLE security_keys (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name            text NOT NULL,                     -- what the owner calls it: "blue YubiKey"
    credential_id   bytea NOT NULL UNIQUE,             -- minted by the authenticator; what the browser presents at login
    public_key      bytea NOT NULL,                    -- COSE-encoded, as stored by go-webauthn
    attestation_type text NOT NULL DEFAULT '',
    transports      text[] NOT NULL DEFAULT '{}',      -- usb/nfc/ble/internal/hybrid — a hint to the browser, never a check
    aaguid          bytea,                             -- authenticator model, informational
    sign_count      bigint NOT NULL DEFAULT 0,         -- monotonic per authenticator; a step backwards suggests a cloned key
    backup_eligible boolean NOT NULL DEFAULT false,    -- a synced passkey rather than a hardware-bound key
    backup_state    boolean NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now(),
    last_used_at    timestamptz
);
CREATE INDEX idx_security_keys_user_id ON security_keys(user_id);

-- The server-side half of a ceremony in progress: the challenge handed to
-- the browser, waiting for the signed answer. One row per person per
-- kind, replaced by each new attempt and deleted the moment it is
-- consumed, so a challenge answers exactly once. Same posture as
-- pending_two_factor_logins: short-lived, and nothing in it grants
-- anything by itself.
CREATE TABLE webauthn_sessions (
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind       text NOT NULL CHECK (kind IN ('register', 'login')),
    data       jsonb NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (user_id, kind)
);
