-- A leader-issued temporary password (a new family's first login, a new
-- individual member login, or a leader-triggered reset — see
-- internal/roster's CreateFamilyWithMember/CreateMemberLogin/
-- ResetFamilyPassword/ResetMemberLoginPassword) must be replaced the first
-- time it's used to log in, so a temporary credential never quietly
-- becomes someone's permanent password. Defaults to false so this
-- migration doesn't retroactively force every existing login to change
-- its password — it's only ever set true at the moment a temporary
-- password is issued.
ALTER TABLE users ADD COLUMN must_change_password boolean NOT NULL DEFAULT false;

-- Short-lived, single-use marker that a login has passed the password
-- check but must set a new password before a real session is issued — same
-- shape as pending_two_factor_logins (0007_totp.sql), carried in its own
-- cookie distinct from both the real session cookie and the pending-2FA
-- cookie, so a half-authenticated visitor never gets a valid session
-- before a temporary password has actually been replaced.
CREATE TABLE pending_password_changes (
    token      text PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    next       text NOT NULL DEFAULT '/', -- where to redirect after a successful change, same as the "next" param on /login
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_pending_password_changes_user_id ON pending_password_changes(user_id);
