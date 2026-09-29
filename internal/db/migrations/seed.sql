-- seed.sql — creates the two units so the site has something to route to.
-- Safe to re-run (ON CONFLICT DO NOTHING).
--
-- Uses the confirmed production hostnames under the 47-yonkers.org parent
-- domain. For local testing, either:
--   (a) add entries to your machine's /etc/hosts pointing these hostnames at
--       127.0.0.1 (harmless — it's a local-only override), or
--   (b) change the two `hostname` values below to troop.localhost /
--       pack.localhost before running this against your dev database.
-- See README.md "Local development" for the full walkthrough.

-- Colors and logos follow the Scouting America Brand Guidelines: Scouts BSA
-- (Troop) uses olive + red, Cub Scouts (Pack) uses blue + gold. See
-- migration 0021_unit_accent_color.sql and internal/web/static/logos.
INSERT INTO units (slug, name, unit_type, hostname, theme_color, accent_color, logo_url)
VALUES
    ('troop-47', 'Troop 47', 'troop', 'troop.47-yonkers.org', '#243E26', '#CE1126', '/static/logos/scouts-bsa-trademark.png'),
    ('pack-47',  'Pack 47',  'pack',  'pack.47-yonkers.org',  '#003F87', '#FDC116', '/static/logos/cub-scouts-trademark.png')
ON CONFLICT (hostname) DO NOTHING;

-- The default prospect workflow, one row per unit. Same five statuses
-- the prospect_status enum held before migration 0048 moved them here
-- so a unit can add its own; see that file for why. No categories are
-- seeded — that list starts empty on purpose, since what a unit wants
-- to file prospects by is entirely local to it.
INSERT INTO prospect_labels (unit_id, kind, value, label, closed, sort_order)
SELECT u.id, 'status', d.value, d.label, d.closed, d.sort_order
FROM units u
CROSS JOIN (VALUES
    ('new',       'New enquiry',       false, 1),
    ('contacted', 'Contacted',         false, 2),
    ('visited',   'Visited a meeting', false, 3),
    ('joined',    'Joined',            true,  4),
    ('declined',  'Not joining',       true,  5)
) AS d(value, label, closed, sort_order)
ON CONFLICT (unit_id, kind, value) DO NOTHING;
