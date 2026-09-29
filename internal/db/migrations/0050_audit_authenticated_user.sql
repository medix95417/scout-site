-- Preserve the login ID even if that login is subsequently removed. Existing
-- rows remain unknown; never backfill a guessed human identity.
ALTER TABLE audit_log ADD COLUMN authenticated_user_id uuid;
