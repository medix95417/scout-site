-- 0003_lockout.sql
-- Backs account-level login lockout (see internal/auth's CheckLockout /
-- RecordLoginFailure / ClearLoginFailures). Added per the Phase 1 security
-- audit's "no rate limiting/lockout" recommendation — see SECURITY_AUDIT.md.

-- One row per email address that has ever failed a login recently. Keyed by
-- the normalized (lowercased) email itself, not a user id, because lockout
-- must apply the same way whether or not that email actually has an
-- account — otherwise "does this email get locked out after N attempts?"
-- becomes a way to test which emails exist.
CREATE TABLE login_failures (
    email            text PRIMARY KEY,
    failure_count    integer NOT NULL DEFAULT 0,
    first_failure_at timestamptz NOT NULL DEFAULT now(),
    last_failure_at  timestamptz NOT NULL DEFAULT now()
);
