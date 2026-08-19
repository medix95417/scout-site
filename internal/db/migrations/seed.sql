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

INSERT INTO units (slug, name, unit_type, hostname, theme_color)
VALUES
    ('troop-47', 'Troop 47', 'troop', 'troop.47-yonkers.org', '#14532d'),
    ('pack-47',  'Pack 47',  'pack',  'pack.47-yonkers.org',  '#1e3a8a')
ON CONFLICT (hostname) DO NOTHING;
