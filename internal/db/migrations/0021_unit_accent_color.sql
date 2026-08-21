-- 0021_unit_accent_color.sql
--
-- Adds a second per-unit brand color. theme_color remains the primary/
-- structural color (header, hero bands, calendar highlights); accent_color
-- is reserved for buttons and other calls-to-action, matching the Scouting
-- America Brand Guidelines' pairing of a neutral/primary color with a
-- separate action color (Scouts BSA: olive + red; Cub Scouts: blue + gold).
-- Defaults to a neutral gray so any unit that hasn't set one keeps looking
-- exactly as it did before this migration (a button in that gray, rather
-- than a jarring unset color).
ALTER TABLE units ADD COLUMN accent_color text NOT NULL DEFAULT '#374151';

-- Bring the two live units in line with the official brand guidelines —
-- Troop 47 (Scouts BSA) and Pack 47 (Cub Scouts) — including the official
-- program trademarks, extracted from the Scouting America Brand Guidelines
-- and served from internal/web/static/logos.
UPDATE units SET
    theme_color = '#243E2C',
    accent_color = '#CE1126',
    logo_url = '/static/logos/scouts-bsa-trademark.png'
WHERE slug = 'troop-47';

UPDATE units SET
    theme_color = '#003F87',
    accent_color = '#FDC116',
    logo_url = '/static/logos/cub-scouts-trademark.png'
WHERE slug = 'pack-47';
