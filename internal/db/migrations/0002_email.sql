-- 0002_email.sql
-- Backs the email system: self-service password reset tokens, and
-- idempotency tracking for event-reminder emails (so a repeated cron run
-- never double-sends). See internal/mailer, internal/auth, and
-- internal/reminders.

CREATE TABLE password_reset_tokens (
    token      text PRIMARY KEY, -- opaque, cryptographically random — same shape as sessions.token
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz, -- null until consumed; a used or expired token is rejected on redemption
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_password_reset_tokens_user_id ON password_reset_tokens(user_id);

-- One row per (event, member) once a reminder email has actually been sent
-- successfully — the reminders job's WHERE clause anti-joins against this so
-- re-running it (e.g. hourly via cron) never emails the same person twice
-- for the same event.
CREATE TABLE event_reminders_sent (
    event_id  uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    member_id uuid NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    sent_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, member_id)
);
