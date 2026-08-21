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
