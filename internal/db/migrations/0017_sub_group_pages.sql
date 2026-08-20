-- 0017_sub_group_pages.sql
--
-- Backs each patrol's/den's own members-only page (see internal/web's
-- GroupView) — a short description plus a handful of photos, similar to
-- the main homepage's "Our Program" blurb and gallery strip, but scoped to
-- one sub_group and never shown to a logged-out visitor. Photos are linked
-- from the existing file library the same way event photos already are
-- (see event_files/migration 0012) rather than needing their own upload
-- path — a sub-group page never needs a file to be marked "public" (see
-- migration 0016) since the page itself already requires login.
ALTER TABLE sub_groups ADD COLUMN description text;

CREATE TABLE sub_group_files (
    sub_group_id uuid NOT NULL REFERENCES sub_groups(id) ON DELETE CASCADE,
    file_id uuid NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    PRIMARY KEY (sub_group_id, file_id)
);
