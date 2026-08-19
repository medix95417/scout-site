-- 0010_newsletters.sql
--
-- Phase 3's newsletter feature: a leader composes a subject/body as a
-- draft, edits it freely, then sends it once to every family currently in
-- the unit's roster — see internal/newsletter. Deliberately its own table
-- rather than another content_pages page_type: content_pages is for
-- public/members-visible *website* content rendered on demand, but a
-- newsletter is private outbound email with a one-way "sent" transition
-- (never re-rendered, never toggled back to draft) and needs its own
-- send-time bookkeeping (when, and to how many recipients) that has no
-- equivalent on a web page.
CREATE TYPE newsletter_status AS ENUM ('draft', 'sent');

CREATE TABLE newsletters (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    unit_id         uuid NOT NULL REFERENCES units(id) ON DELETE CASCADE,
    subject         text NOT NULL,
    body            text NOT NULL DEFAULT '',
    status          newsletter_status NOT NULL DEFAULT 'draft',
    created_by      uuid NOT NULL REFERENCES members(id),
    sent_at         timestamptz,
    recipient_count integer,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_newsletters_unit_id ON newsletters(unit_id);
