-- 0018_calendar_sub_groups.sql
--
-- events.sub_group_id has existed since 0001_init.sql, where it was defined
-- inline with ON DELETE CASCADE. An earlier version of THIS migration
-- mistakenly tried to ADD the column again, which fails with "column
-- already exists" on every database created from 0001 (i.e. all of them).
-- So this migration does NOT add the column.
--
-- What it does do is correct the foreign key's delete behavior from
-- CASCADE to SET NULL: deleting a patrol/den should just widen its events
-- back to whole-unit scope (sub_group_id becomes NULL), not silently
-- delete the events themselves. Written idempotently (DROP CONSTRAINT
-- IF EXISTS, then re-add) so it's safe whether the constraint is still the
-- original CASCADE one from 0001 or was already changed by hand.
ALTER TABLE events DROP CONSTRAINT IF EXISTS events_sub_group_id_fkey;
ALTER TABLE events ADD CONSTRAINT events_sub_group_id_fkey
	FOREIGN KEY (sub_group_id) REFERENCES sub_groups(id) ON DELETE SET NULL;
