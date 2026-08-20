-- 0013_custom_roles.sql
--
-- Lets a super_admin create a new role on the fly (per unit) and pick
-- which capabilities it grants, instead of every role being one of a
-- fixed, code-defined set. See internal/units for the capability names
-- and how a member/family's effective capabilities are resolved.
--
-- role_assignments.role was the fixed member_role enum; it becomes plain
-- text so it can hold either one of the original fixed role slugs (their
-- meaning/behavior is unchanged — see internal/units' systemRoleCapabilities)
-- or a custom_roles.slug. Existing rows keep their exact values; only the
-- column's type changes, not any data.
ALTER TABLE role_assignments ALTER COLUMN role TYPE text USING role::text;
DROP TYPE member_role;

CREATE TABLE custom_roles (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    unit_id      uuid NOT NULL REFERENCES units(id) ON DELETE CASCADE,
    slug         text NOT NULL,
    label        text NOT NULL,
    -- Subset of internal/units' capability names. Validated against the
    -- fixed set here as well as in Go, same defense-in-depth as other
    -- small closed sets in this schema.
    capabilities text[] NOT NULL DEFAULT '{}'
        CHECK (capabilities <@ ARRAY['edit_content', 'approve_submissions', 'submit_for_approval', 'manage_ledger', 'super_admin']::text[]),
    created_by   uuid REFERENCES members(id),
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (unit_id, slug)
);
CREATE INDEX idx_custom_roles_unit_id ON custom_roles(unit_id);
