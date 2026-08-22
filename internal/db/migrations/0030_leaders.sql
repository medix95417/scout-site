-- 0030_leaders.sql
--
-- The public "Our Leaders" page — a simple, admin-maintained listing of a
-- unit's adult leaders: name, role title, a brief bio, and an optional
-- photo. Deliberately a dedicated table rather than another content_pages
-- page_type (see 0017_sub_group_pages.sql/content.go's posts/galleries):
-- a leader has several distinct structured fields (name, role title, bio,
-- photo), not one free-text Body, so separate columns are a better fit
-- than cramming them into one text blob with a line-based convention.
--
-- Reuses the existing content_status enum (0001_init.sql) for the same
-- draft/published lifecycle news posts and photo albums already have — a
-- leader profile being drafted shouldn't appear on the public page until
-- whoever's editing it is ready.
CREATE TABLE leaders (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    unit_id     uuid NOT NULL REFERENCES units(id) ON DELETE CASCADE,
    name        text NOT NULL,
    role_title  text NOT NULL DEFAULT '',
    bio         text NOT NULL DEFAULT '',
    photo_url   text NOT NULL DEFAULT '',
    sort_order  integer NOT NULL DEFAULT 0,
    status      content_status NOT NULL DEFAULT 'draft',
    created_by  uuid REFERENCES members(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_leaders_unit_id ON leaders(unit_id, sort_order, name);
