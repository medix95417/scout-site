-- 0027_fix_scouts_olive_hex.sql
--
-- Corrects a one-digit transcription typo from migration
-- 0021_unit_accent_color.sql: the BSA Digital Brand Guidelines' "Scouts
-- Olive" program color is #243E26, not #243E2C. Cosmetically invisible
-- (one step in the blue channel), but worth being byte-exact with the
-- guideline now that it's been re-checked against the source Zeplin.
UPDATE units SET theme_color = '#243E26' WHERE slug = 'troop-47' AND theme_color = '#243E2C';
