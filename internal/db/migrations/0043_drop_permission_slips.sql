-- Permission slips are gone. The unit decided digital consent forms
-- aren't something it needs, so rather than leave a switched-off feature
-- and two tables nobody reads, the whole thing comes out: the
-- internal/permission package, its handlers and template, the two
-- settings toggles, and the storage below.
--
-- This is deliberately destructive. Any slips and signatures already
-- collected are deleted with the tables and are recoverable only from a
-- backup taken before this migration ran (scripts/backup.sh). That was
-- the explicit choice when the feature was dropped; if you are reading
-- this because you need an old signature, restore a pre-0043 dump into a
-- scratch database and read it there rather than trying to revive the
-- feature.
--
-- Order matters: signatures reference slips, so they go first. The
-- events column goes last because nothing else depends on it.
DROP TABLE IF EXISTS permission_slip_signatures;
DROP TABLE IF EXISTS permission_slips;

-- Set per-event by whoever created the event, and read only by the
-- permission-slip UI that no longer exists.
ALTER TABLE events DROP COLUMN IF EXISTS requires_permission_slip;

-- The two toggles that gated the feature. Leaving these behind would put
-- dead rows in front of anyone reading system settings out of the
-- database, and settings.go no longer declares either key.
DELETE FROM unit_settings WHERE key IN ('permission_slips_enabled', 'permission_slip_enforcement');
DELETE FROM system_settings WHERE key IN ('permission_slips_enabled', 'permission_slip_enforcement');
