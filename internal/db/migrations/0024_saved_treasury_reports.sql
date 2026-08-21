-- 0024_saved_treasury_reports.sql
--
-- Named, saved report filter presets for /treasury/reports (see
-- internal/ledger's report-building functions and
-- internal/web/treasury_reports.go). Deliberately unit-wide, not tied to
-- whoever saved it: any Treasurer/super_admin in the unit can see, run,
-- or delete any saved report, per the requirement that these are the
-- unit's presets, not a personal bookmark — created_by is kept only for
-- the audit trail, the same way every other entity in this codebase
-- records who touched it, not as an access-control column.
--
-- filters is a raw JSON blob of exactly the report page's own
-- query-string parameters (date range, account IDs, transaction types,
-- etc.) — saving a report is just remembering those parameters; running
-- a saved report re-renders the report page with them restored, nothing
-- computed or interpreted specially at save time. A later run always
-- recomputes against current data (e.g. a saved "This Month" income
-- report is really just whatever explicit start/end dates were selected
-- when it was saved, not a rolling window — see the package's own
-- comment on this trade-off).
CREATE TABLE saved_treasury_reports (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    unit_id     uuid NOT NULL REFERENCES units(id) ON DELETE CASCADE,
    name        text NOT NULL,
    report_type text NOT NULL,
    filters     jsonb NOT NULL DEFAULT '{}',
    created_by  uuid NOT NULL REFERENCES members(id),
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_saved_treasury_reports_unit_id ON saved_treasury_reports(unit_id);
