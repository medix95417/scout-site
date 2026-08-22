-- 0028_permission_slip_required.sql
--
-- Adds a per-event "requires a permission slip" flag, distinct from
-- whether an event actually has a permission slip attached (see
-- permission_slips.event_id) — most events (a weekly meeting) never need
-- one; only trips/campouts do. A leader sets this when creating an
-- event; a unit can then opt (via a settings toggle) into only showing
-- the "Permission slip" link/page for events marked this way instead of
-- on every single event.
--
-- Existing events that already have a permission slip attached are
-- backfilled to true, since a leader clearly already decided one was
-- needed there; everything else defaults to false.
ALTER TABLE events ADD COLUMN requires_permission_slip boolean NOT NULL DEFAULT false;

UPDATE events SET requires_permission_slip = true
WHERE id IN (SELECT event_id FROM permission_slips);
