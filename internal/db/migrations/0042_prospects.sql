-- Prospective families: someone who filled in the "interested in
-- joining" form on the public site, and the unit's record of what
-- happened next.
--
-- Stored rather than only emailed, because an email is a notification
-- and not a record: it lands in whoever's inbox, gets read once, and
-- nobody can answer "who enquired last month and did we call them
-- back?". The email still goes out — it's how a leader finds out — but
-- the row is what makes the enquiry trackable.
--
-- Deliberately not tied to members or families. A prospect is not on the
-- roster and may never be; joining is a separate act a leader performs
-- on the roster page, and linking the two would invite a half-real
-- person in the members table.

CREATE TYPE prospect_status AS ENUM ('new', 'contacted', 'visited', 'joined', 'declined');

CREATE TABLE prospects (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    unit_id        uuid NOT NULL REFERENCES units(id) ON DELETE CASCADE,

    -- Everything below is typed by a member of the public into a form on
    -- the open internet. Lengths are bounded here as well as in the
    -- handler: the handler's caps are the friendly error, these are the
    -- guarantee.
    parent_name    text NOT NULL CHECK (length(parent_name) BETWEEN 1 AND 120),
    parent_email   text NOT NULL CHECK (length(parent_email) BETWEEN 3 AND 200),
    parent_phone   text NOT NULL DEFAULT '' CHECK (length(parent_phone) <= 40),
    child_name     text NOT NULL CHECK (length(child_name) BETWEEN 1 AND 120),
    child_age      int CHECK (child_age IS NULL OR child_age BETWEEN 3 AND 21),
    child_grade    text NOT NULL DEFAULT '' CHECK (length(child_grade) <= 40),
    child_school   text NOT NULL DEFAULT '' CHECK (length(child_school) <= 120),
    message        text NOT NULL DEFAULT '' CHECK (length(message) <= 2000),

    -- The tracking half.
    status         prospect_status NOT NULL DEFAULT 'new',
    notes          text NOT NULL DEFAULT '' CHECK (length(notes) <= 4000),

    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- The admin list is "this unit's prospects, newest first", and the
-- default view filters to the ones still needing attention.
CREATE INDEX idx_prospects_unit_created ON prospects (unit_id, created_at DESC);
CREATE INDEX idx_prospects_unit_status ON prospects (unit_id, status);
