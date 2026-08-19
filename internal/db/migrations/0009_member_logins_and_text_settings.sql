-- 0009_member_logins_and_text_settings.sql
-- Two independent additions:
--
-- 1. Individual Scout logins. Until now every login represented a whole
--    family's shared account (users.family_id), with no way for one
--    specific member — say, a Scout who's registered in both the Troop
--    and Pack — to log in as themselves rather than as "whoever the
--    family login currently resolves to" (see
--    family.ActingMemberForFamilyInUnit). Adding a nullable, UNIQUE
--    member_id lets a users row optionally represent one specific
--    member's own login instead of the whole family's shared one. NULL
--    means "this is still a family-wide login" — every existing row is
--    untouched by this migration. A plain UNIQUE constraint (not
--    combined with NOT NULL) is safe here because Postgres treats every
--    NULL as distinct for uniqueness purposes, so any number of
--    family-wide (member_id IS NULL) rows can coexist; only two rows
--    both claiming the *same* member are rejected — exactly "at most one
--    individual login per member."
--
-- 2. Text-valued site settings, alongside the existing boolean-only ones
--    (0008_system_settings.sql). SMTP host/port/username/from are being
--    added to /admin/settings (see internal/settings's TextSettings) so
--    a non-technical admin doesn't need the CLI/env vars for those — but
--    the SMTP password deliberately stays env-var-only (see README.md),
--    so this table only ever needs to hold the non-secret half of that
--    config. value is relaxed to nullable so a text-setting row can
--    leave it NULL rather than needing a meaningless boolean placeholder
--    (and vice versa: a boolean toggle's value_text stays NULL).
ALTER TABLE users ADD COLUMN member_id uuid REFERENCES members(id) ON DELETE CASCADE;
ALTER TABLE users ADD CONSTRAINT users_member_id_key UNIQUE (member_id);

ALTER TABLE system_settings ALTER COLUMN value DROP NOT NULL;
ALTER TABLE system_settings ADD COLUMN value_text text;
