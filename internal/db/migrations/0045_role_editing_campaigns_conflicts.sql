-- Four independent additions that happen to land together, kept in one
-- migration because each is small and none depends on the others:
--
--   1. Per-unit overrides of a built-in role's capabilities.
--   2. Prospect email consent, and the campaigns sent under it.
--   3. Who a newsletter actually reached.
--   4. Imported calendar events held back for a leader to rule on.

-- 1. Built-in roles, tuned per unit ------------------------------------
--
-- The nine fixed role slugs (units.systemRoleCapabilities) grant a fixed
-- set of capabilities in code. That is the right default and stays the
-- default: a row here exists only for a role a unit has deliberately
-- changed, and its absence means "use the code's answer". Storing the
-- delta rather than a copy means a later change to a built-in role's
-- defaults still reaches every unit that never overrode it, and there is
-- never a stale duplicate of the code's table sitting in the database.
--
-- Scoped per unit for the same reason role_assignments is: the Troop
-- deciding its Assistant Scoutmasters may authorize spending says nothing
-- about the Pack.
CREATE TABLE role_capability_overrides (
    unit_id      uuid        NOT NULL REFERENCES units(id) ON DELETE CASCADE,
    -- One of the fixed slugs. Not a FK — the list lives in Go, and a
    -- custom role is edited on custom_roles itself, not here.
    role_slug    text        NOT NULL,
    capabilities text[]      NOT NULL DEFAULT '{}'
        CHECK (capabilities <@ ARRAY[
            'edit_content',
            'approve_submissions',
            'submit_for_approval',
            'manage_ledger',
            'approve_expenses',
            'super_admin'
        ]::text[]),
    updated_by   uuid        REFERENCES members(id),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (unit_id, role_slug)
);

-- 2. Prospect email consent --------------------------------------------
--
-- The join form now says what the address will be used for and that it
-- can be withdrawn; this is where withdrawing it is recorded. Set either
-- by the person themselves through the unsubscribe link in a campaign
-- email, or by a leader on the admin page — the flag reads the same
-- whichever did it, because the obligation it encodes is the same.
--
-- An opt-out never deletes the enquiry: a leader still needs to know the
-- family exists and that they asked not to be emailed, or the next
-- campaign re-adds them by hand.
ALTER TABLE prospects ADD COLUMN email_opt_out boolean NOT NULL DEFAULT false;
ALTER TABLE prospects ADD COLUMN opt_out_at timestamptz;

-- Reusable email bodies, for prospect campaigns and newsletters alike.
--
-- Distinct from newsletter.StarterTemplates, which are code-defined
-- starting points shipped with the site. These are what a unit wrote and
-- wants back next time.
CREATE TABLE email_templates (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    unit_id    uuid        NOT NULL REFERENCES units(id) ON DELETE CASCADE,
    -- Which composer offers it. A recruiting letter and a pack newsletter
    -- are not interchangeable, and mixing them in one list makes both
    -- harder to use.
    kind       text        NOT NULL CHECK (kind IN ('prospect', 'newsletter')),
    name       text        NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    subject    text        NOT NULL CHECK (length(subject) <= 200),
    body       text        NOT NULL,
    created_by uuid        REFERENCES members(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    -- Saving under a name that already exists overwrites it, which is
    -- what "save this template" means to the person doing it.
    UNIQUE (unit_id, kind, name)
);

CREATE TYPE campaign_status AS ENUM ('draft', 'sending', 'sent');

-- One mass email to prospects.
--
-- Deliberately its own table rather than a row in newsletters with a
-- flag: the audience is chosen by prospect status, the consent rules are
-- different, and every recipient is a member of the public rather than
-- somebody with a login.
CREATE TABLE prospect_campaigns (
    id              uuid            PRIMARY KEY DEFAULT gen_random_uuid(),
    unit_id         uuid            NOT NULL REFERENCES units(id) ON DELETE CASCADE,
    subject         text            NOT NULL,
    body            text            NOT NULL DEFAULT '',
    -- Which prospect statuses this went to. Recorded as sent, not
    -- re-derived: a prospect's status moves, and "who did we email in
    -- March" must not change answer in April.
    target_statuses text[]          NOT NULL DEFAULT '{}',
    status          campaign_status NOT NULL DEFAULT 'draft',
    created_by      uuid            NOT NULL REFERENCES members(id),
    sent_at         timestamptz,
    recipient_count integer         NOT NULL DEFAULT 0,
    created_at      timestamptz     NOT NULL DEFAULT now(),
    updated_at      timestamptz     NOT NULL DEFAULT now()
);
CREATE INDEX idx_prospect_campaigns_unit ON prospect_campaigns (unit_id, created_at DESC);

-- Exactly who a campaign reached, and who it failed to reach.
--
-- The email address is copied in rather than joined through prospect_id,
-- so the record survives the prospect being deleted and still says where
-- the message went. prospect_id is kept alongside it, nullable, only to
-- link back while the prospect still exists.
CREATE TABLE prospect_campaign_recipients (
    campaign_id uuid        NOT NULL REFERENCES prospect_campaigns(id) ON DELETE CASCADE,
    prospect_id uuid        REFERENCES prospects(id) ON DELETE SET NULL,
    email       text        NOT NULL,
    name        text        NOT NULL DEFAULT '',
    sent_at     timestamptz,
    -- Empty when it went out. A short reason when it didn't, so a
    -- bounced address is visible rather than silently missing.
    error       text        NOT NULL DEFAULT '',
    PRIMARY KEY (campaign_id, email)
);

-- 3. Who a newsletter reached ------------------------------------------
--
-- newsletters.recipient_count already records how many; this records
-- which. Same reasoning as above: the address is copied in, so the list
-- still reads correctly after a family leaves the unit.
CREATE TABLE newsletter_recipients (
    newsletter_id uuid        NOT NULL REFERENCES newsletters(id) ON DELETE CASCADE,
    email         text        NOT NULL,
    sent_at       timestamptz,
    error         text        NOT NULL DEFAULT '',
    PRIMARY KEY (newsletter_id, email)
);

-- 4. Calendar import conflicts -----------------------------------------
--
-- An imported event that overlaps something the unit put on its own
-- calendar. Previously both simply appeared, side by side, and the
-- duplicate was somebody's problem to notice; now the import holds the
-- incoming copy here and the calendar stays as it was until a leader
-- rules on it.
--
-- The incoming event is stored in full rather than re-fetched at
-- resolution time: the feed may have changed or gone away by then, and a
-- leader must be deciding about the thing they were shown.
CREATE TABLE calendar_feed_conflicts (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    feed_id      uuid        NOT NULL REFERENCES calendar_feeds(id) ON DELETE CASCADE,
    unit_id      uuid        NOT NULL REFERENCES units(id) ON DELETE CASCADE,
    -- The source calendar's UID, so a refresh updates the pending
    -- conflict rather than stacking a new one every time it runs.
    external_uid text        NOT NULL,

    -- The incoming event, as offered by the source.
    title        text        NOT NULL,
    description  text        NOT NULL DEFAULT '',
    location     text        NOT NULL DEFAULT '',
    starts_at    timestamptz NOT NULL,
    ends_at      timestamptz,

    -- What it clashes with. ON DELETE CASCADE because a conflict with a
    -- deleted event is not a conflict any more — the reason to hold the
    -- import back is gone, and the next refresh will bring it in.
    existing_event_id uuid   NOT NULL REFERENCES events(id) ON DELETE CASCADE,

    detected_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (feed_id, external_uid)
);
CREATE INDEX idx_calendar_feed_conflicts_unit ON calendar_feed_conflicts (unit_id, detected_at DESC);

-- A decision a leader made about one incoming event, remembered so the
-- next refresh does not ask again.
--
-- Separate from the conflict row because it outlives it: the conflict is
-- resolved and gone, but "always skip this event" has to still be true
-- an hour later when the feed is fetched again. 'skip' is the only
-- decision that needs remembering — importing or replacing produces an
-- events row, and the ordinary (feed_id, external_uid) match handles it
-- from then on.
CREATE TABLE calendar_feed_ignores (
    feed_id      uuid        NOT NULL REFERENCES calendar_feeds(id) ON DELETE CASCADE,
    external_uid text        NOT NULL,
    decided_by   uuid        REFERENCES members(id),
    decided_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (feed_id, external_uid)
);
