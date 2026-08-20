-- 0020_file_display_names.sql
--
-- Uploaded files are keyed by their original filename (e.g.
-- "IMG_6440.jpeg"), which is rarely a friendly label to show a family
-- browsing a photo picker. display_name is an optional, leader-editable
-- override shown in place of the filename wherever a file is listed or
-- picked; '' (the default) means "no override yet — show the filename."
ALTER TABLE files ADD COLUMN display_name text NOT NULL DEFAULT '';
