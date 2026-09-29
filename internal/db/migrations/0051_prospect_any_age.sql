-- A prospective Scout can be younger than Scouting takes.
--
-- The age floor was 3, which is roughly when a child could join a
-- Lion den. But a unit meets families with a one-year-old sibling at a
-- recruiting night and wants to remember them for the year they are
-- old enough — that is the whole point of keeping a prospect list, and
-- refusing the age meant either leaving it blank or writing it in the
-- notes where nothing can sort on it.
--
-- Still bounded, because a typo in an age field is worth catching: the
-- range is now 0 to 21, so a slipped digit that makes someone 90 is
-- still refused. Blank remains allowed and remains the common case —
-- plenty of families don't say.
ALTER TABLE prospects DROP CONSTRAINT IF EXISTS prospects_child_age_check;
ALTER TABLE prospects ADD CONSTRAINT prospects_child_age_check
    CHECK (child_age IS NULL OR child_age BETWEEN 0 AND 21);
