-- 0039_custom_role_approve_expenses.sql
--
-- Migration 0038's sibling gap: the expense-authorization capability
-- (units.CapApproveExpenses, added with the spending-approval control)
-- went into the Go capability list but not into this CHECK constraint,
-- which mirrors it. That made the documented escape hatch — "a unit that
-- wants an Assistant Scoutmaster to be able to authorize spending can
-- grant it through a custom role" — fail on insert rather than work.
ALTER TABLE custom_roles DROP CONSTRAINT custom_roles_capabilities_check;
ALTER TABLE custom_roles ADD CONSTRAINT custom_roles_capabilities_check
    CHECK (capabilities <@ ARRAY[
        'edit_content',
        'approve_submissions',
        'submit_for_approval',
        'manage_ledger',
        'approve_expenses',
        'super_admin'
    ]::text[]);
