-- 0008_system_settings.sql
-- A small, generic key/value table for site-wide on/off toggles — see
-- internal/settings. Deliberately a key/value table rather than one
-- column per setting: adding a new toggle later is then just a new
-- entry in internal/settings.Toggles (a Go struct literal) plus whatever
-- code reads it, not a new migration every time. A row's absence means
-- "use the default" (see internal/settings.Get) rather than every
-- possible key needing to be pre-populated.
--
-- Values are boolean-only for now, matching the only kind of setting
-- requested so far ("system wide items as a toggle") — if a future
-- setting needs something richer than on/off, that's a schema change to
-- make then, not something to over-build now.

-- id is a real uuid (rather than making key the primary key) so a change
-- can be recorded in audit_log the same way every other entity is —
-- audit_log.entity_id is uuid NOT NULL, and key is a human-readable
-- string like "require_two_factor_for_all", not a uuid.
CREATE TABLE system_settings (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    key        text NOT NULL UNIQUE,
    value      boolean NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by uuid REFERENCES members(id)
);
