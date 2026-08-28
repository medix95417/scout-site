-- An editable date for a photo album (and any other content page that
-- wants one), separate from created_at.
--
-- created_at answers "when was this typed into the site", which is the
-- wrong question for a photo album: a leader uploading last spring's
-- campout today would have it file above this month's, because the only
-- date the site had was the upload's. photo_date answers "when did this
-- happen", which is what a reader is actually scanning the Photos page
-- for and what it now sorts on.
--
-- Nullable on purpose rather than defaulted to now(): NULL means "nobody
-- has said", and every read falls back to created_at for those, so
-- existing albums keep exactly the date and order they have today and a
-- leader only fills this in when the two genuinely differ.
ALTER TABLE content_pages
    ADD COLUMN photo_date date;

-- Deliberately no index on the sort expression. The Photos page orders by
-- COALESCE(photo_date, created_at) DESC, and created_at is a timestamptz
-- whose cast to date depends on the session TimeZone — Postgres rejects
-- that in an index because it isn't IMMUTABLE. It would not earn its keep
-- anyway: content_pages holds a unit's news posts and photo albums, which
-- is dozens of rows, already filtered by unit_id and page_type on the
-- existing idx_content_pages_unit_id.
