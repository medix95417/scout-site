-- 0014_unit_settings.sql
--
-- Per-unit on/off toggles — a sibling to system_settings (0008), which is
-- deliberately site-wide only (see internal/settings' package doc). Some
-- settings (e.g. whether /advancement is enabled) should be flippable
-- independently per unit, since Troop and Pack may want different
-- answers — a separate table rather than adding a nullable unit_id to
-- system_settings keeps that package's "site-wide, full stop" semantics
-- unambiguous rather than threading a sometimes-nil unit scope through
-- every function there.
CREATE TABLE unit_settings (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    unit_id    uuid NOT NULL REFERENCES units(id) ON DELETE CASCADE,
    key        text NOT NULL,
    value      boolean NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by uuid REFERENCES members(id),
    UNIQUE (unit_id, key)
);
