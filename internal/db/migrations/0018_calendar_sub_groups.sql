-- 0018_calendar_sub_groups.sql
--
-- Lets an event be scoped to one patrol/den (e.g. "Bear Den 3 hike")
-- instead of the whole unit — nullable, so every existing event (and any
-- new one that doesn't set it) keeps behaving exactly as before: visible
-- to the whole unit's members (subject to the existing public/members
-- visibility split). ON DELETE SET NULL rather than CASCADE: deleting a
-- sub_group shouldn't silently delete its past events, just widen who can
-- see them back to the whole unit.
ALTER TABLE events ADD COLUMN sub_group_id uuid REFERENCES sub_groups(id) ON DELETE SET NULL;
