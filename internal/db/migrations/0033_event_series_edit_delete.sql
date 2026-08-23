-- 0033_event_series_edit_delete.sql
--
-- series_id groups the individual occurrences a "repeats" event creates
-- into one recognizable set — purely a display/bookkeeping tag, not a
-- foreign key, since each occurrence is (and remains) a fully independent
-- events row: its own RSVPs, its own permission slip, its own approval
-- routing, and — per this same release — its own edit/delete. There is no
-- "edit/delete the whole series" operation; a leader who wants to change
-- every occurrence still does it one at a time, same as if they'd been
-- created separately. NULL means "not part of a repeating series."
ALTER TABLE events ADD COLUMN series_id uuid;
CREATE INDEX idx_events_series_id ON events(series_id) WHERE series_id IS NOT NULL;
