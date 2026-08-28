-- 0037_hash_stored_tokens.sql
--
-- Session, password-reset, pending-2FA and pending-password-change tokens
-- used to be stored exactly as they were handed to the user. Anyone who
-- could read the database — a leaked backup, a dump shared for debugging,
-- a restored snapshot — therefore had live, replayable sessions and
-- working account-takeover links until they expired. internal/auth now
-- stores only sha256(token) and looks rows up by that hash, so a stolen
-- copy of these tables is inert (see hashToken).
--
-- The existing rows hold plaintext tokens that would never match a hash
-- anyway, so they're deleted rather than left behind: that scrubs the
-- plaintext this change exists to get rid of, instead of leaving it
-- sitting in the table (and in every backup taken from here on) as dead
-- weight.
--
-- The visible effect is a one-time sign-out: everyone logs in again, and
-- any password-reset link emailed in the last hour needs re-requesting.
-- All four tables are short-lived session state, not records worth
-- keeping.
DELETE FROM sessions;
DELETE FROM password_reset_tokens;
DELETE FROM pending_two_factor_logins;
DELETE FROM pending_password_changes;
