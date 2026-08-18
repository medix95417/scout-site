-- 0007_totp.sql
-- Phase 2: TOTP-based two-factor authentication for the Treasurer and
-- super_admin roles specifically — see internal/twofactor. Requested
-- explicitly given that this login (a shared family account) now gates
-- access to real money once the ledger ships, and some of this site's
-- accounts belong to minors.

CREATE TABLE totp_credentials (
    user_id      uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    secret       text NOT NULL, -- base32-encoded TOTP shared secret (RFC 4648 / RFC 6238)
    confirmed_at timestamptz,   -- null until the user proves they've set it up correctly by entering one valid code; an unconfirmed secret is never enforced at login
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- One-time backup codes, generated at enrollment, for recovering access if
-- the authenticator device is lost. Hashed with bcrypt, same as a
-- password — these are bearer secrets, not something to store in the
-- clear even though they're single-use.
CREATE TABLE totp_backup_codes (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash  text NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_totp_backup_codes_user_id ON totp_backup_codes(user_id);

-- Short-lived, single-use marker that a login has passed the password
-- check and is waiting on a TOTP code — same opaque-random-token shape as
-- sessions/password_reset_tokens (see internal/auth), carried in its own
-- cookie distinct from the real session cookie so a half-authenticated
-- visitor never gets a valid session token before the second factor
-- succeeds.
CREATE TABLE pending_two_factor_logins (
    token      text PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    next       text NOT NULL DEFAULT '/', -- where to redirect after a successful code, same as the "next" param on /login
    attempts   integer NOT NULL DEFAULT 0, -- wrong-code guesses against this one pending login; capped in internal/auth so a stolen/observed pending-login cookie can't be used to brute-force a 6-digit code
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_pending_two_factor_logins_user_id ON pending_two_factor_logins(user_id);
