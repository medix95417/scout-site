-- A fundraiser's council-approved allocation rule can vary by what's
-- actually being sold (popcorn vs. a car wash vs. a campership drive), and
-- units commonly run a fundraiser as one specific calendar event. Tying a
-- fundraiser to that event lets the fundraiser page show its date/context
-- without duplicating it. Nullable and SET NULL on delete, same as
-- ledger_accounts.event_id (0006_ledger.sql) for trip funds — a
-- fundraiser's own financial history must survive the calendar event it
-- was linked to being deleted later.
ALTER TABLE fundraisers ADD COLUMN event_id uuid REFERENCES events(id) ON DELETE SET NULL;
