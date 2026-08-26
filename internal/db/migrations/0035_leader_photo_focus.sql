-- 0035_leader_photo_focus.sql
--
-- The "Our Leaders" page shows each photo filling a fixed-height card
-- (see leaders.html) — object-cover crops whatever doesn't fit, and a
-- portrait headshot in a wide card often lost the top of someone's head
-- or their chin to a default center crop with no way to fix it. This
-- lets whoever manages a leader's profile nudge which part of the photo
-- stays visible: top, center (the original default, unchanged), or
-- bottom.
ALTER TABLE leaders ADD COLUMN photo_focus text NOT NULL DEFAULT 'center';
