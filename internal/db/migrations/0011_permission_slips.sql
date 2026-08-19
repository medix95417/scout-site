-- 0011_permission_slips.sql
--
-- Phase 3's digital permission slips / consent forms — see
-- internal/permission. A leader attaches one consent form to an event
-- (one-to-one, like a trip fund — a second slip for the same event would
-- just fragment who's signed what, so UNIQUE(event_id) enforces it at the
-- schema level same as ledger_accounts does for a trip fund's event_id).
-- A parent/guardian then signs it once per Scout of theirs attending —
-- deliberately per-Scout, not per-family: RSVPs are a household-level
-- "are we coming," but a permission slip is BSA-style per-participant
-- consent, so two Scouts from the same family at the same event each need
-- their own signature row.
CREATE TABLE permission_slips (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id   uuid NOT NULL UNIQUE REFERENCES events(id) ON DELETE CASCADE,
    unit_id    uuid NOT NULL REFERENCES units(id) ON DELETE CASCADE,
    title      text NOT NULL,
    body       text NOT NULL DEFAULT '',
    created_by uuid NOT NULL REFERENCES members(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_permission_slips_unit_id ON permission_slips(unit_id);

-- signer_name is the typed e-signature itself (a parent/guardian typing
-- their full legal name) — kept as its own text column rather than only
-- looking up signed_by_member_id's name, since a member's name could be
-- edited later (see roster.UpdateMember) and the signature should record
-- what was actually typed at the moment of signing, not whatever the
-- roster says today.
CREATE TABLE permission_slip_signatures (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    permission_slip_id  uuid NOT NULL REFERENCES permission_slips(id) ON DELETE CASCADE,
    scout_member_id     uuid NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    signed_by_member_id uuid NOT NULL REFERENCES members(id),
    signer_name         text NOT NULL,
    signed_at           timestamptz NOT NULL DEFAULT now(),
    UNIQUE (permission_slip_id, scout_member_id)
);
CREATE INDEX idx_permission_slip_signatures_slip_id ON permission_slip_signatures(permission_slip_id);
