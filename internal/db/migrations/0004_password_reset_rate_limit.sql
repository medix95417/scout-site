-- 0004_password_reset_rate_limit.sql
-- Backs rate limiting on "forgot password" submissions (see
-- internal/auth.RecordAndCheckPasswordResetRequest). Protects an
-- account's inbox — a family's, or a Scout's own login — from being
-- flooded by repeated reset requests. Added after the Phase 1 security
-- audit's follow-up pass; see SECURITY_AUDIT.md.

-- One row per email address that has ever submitted the "forgot password"
-- form recently. Keyed by the normalized (lowercased) email itself, not a
-- user id — same reasoning as login_failures (0003_lockout.sql): this
-- must track (and rate-limit) submissions against emails with no account
-- too, or the presence/absence of a row would itself leak which emails
-- are registered.
CREATE TABLE password_reset_requests (
    email             text PRIMARY KEY,
    request_count     integer NOT NULL DEFAULT 0,
    first_request_at  timestamptz NOT NULL DEFAULT now(),
    last_request_at   timestamptz NOT NULL DEFAULT now()
);
