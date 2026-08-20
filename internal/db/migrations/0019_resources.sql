-- 0019_resources.sql
--
-- A curated resources page — documents and links leaders want visitors to
-- find easily (handbooks, forms, useful outside sites), distinct from the
-- general file library (see 0012_files.sql), which is an upload/management
-- surface aimed at leaders, not a curated visitor-facing list.
--
-- Each resource points at either an already-uploaded file or an external
-- URL, never both — the CHECK constraint below enforces that at the
-- database level rather than trusting every future write path to get it
-- right. is_public is its own flag, independent of files.is_public: a
-- resource can make a members-only-by-default file visible to the public
-- through this page without changing that file's own public flag (which
-- also governs its raw /files/{id}/download URL and eligibility for the
-- homepage/hero image pickers).
CREATE TABLE resources (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    unit_id     uuid NOT NULL REFERENCES units(id) ON DELETE CASCADE,
    title       text NOT NULL,
    description text NOT NULL DEFAULT '',
    file_id     uuid REFERENCES files(id) ON DELETE CASCADE,
    url         text,
    is_public   boolean NOT NULL DEFAULT false,
    created_by  uuid REFERENCES members(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT resources_exactly_one_target CHECK (
        (file_id IS NOT NULL AND url IS NULL) OR (file_id IS NULL AND url IS NOT NULL)
    )
);
CREATE INDEX idx_resources_unit_id ON resources(unit_id, created_at DESC);
