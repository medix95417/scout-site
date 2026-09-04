-- Calendar in and out.
--
-- Two related but separate things land here:
--
--   1. calendar_feed_tokens — a personal, secret .ics address each person
--      can subscribe to from their phone, showing exactly the events that
--      person is allowed to see.
--   2. calendar_feeds — an external calendar (a Google secret address,
--      most often) the unit subscribes TO, whose events are copied onto
--      the unit calendar.

-- 1. Outbound: the personal subscription link.
--
-- One row per person per unit, because a login can hold roles in both the
-- Troop and the Pack and each subdomain is its own calendar. Regenerating
-- a link replaces the row's token, which is what makes a lost phone
-- recoverable.
--
-- The token is stored as a SHA-256 hash, never in the clear, matching how
-- sessions and password-reset tokens are already handled (migration
-- 0037). The consequence is the same as for those: the plaintext exists
-- only in the URL held by the subscriber, so the site can show it at the
-- moment it is generated and never again — the settings page offers
-- "regenerate", not "reveal".
--
-- Hashing is plain SHA-256 rather than bcrypt on purpose. A feed request
-- arrives with only the token and must find its row, which a slow salted
-- hash cannot do; the token is 32 bytes of CSPRNG output, so unlike a
-- human-chosen password there is nothing for an offline attack to guess.
CREATE TABLE calendar_feed_tokens (
    user_id      uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    unit_id      uuid        NOT NULL REFERENCES units(id) ON DELETE CASCADE,
    token_hash   text        NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    -- Purely informational, shown on the settings page so somebody can
    -- tell whether their phone is actually still pulling the feed.
    last_used_at timestamptz,
    PRIMARY KEY (user_id, unit_id)
);

-- The lookup every feed request makes.
CREATE INDEX idx_calendar_feed_tokens_hash ON calendar_feed_tokens (token_hash);

-- 2. Inbound: external calendars this unit imports from.
CREATE TABLE calendar_feeds (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    unit_id      uuid        NOT NULL REFERENCES units(id) ON DELETE CASCADE,
    -- What a leader calls it on the admin page ("Council calendar").
    name         text        NOT NULL,
    -- The .ics URL. Held in the clear because it has to be re-fetched on
    -- a schedule, and a Google secret address is a bearer credential for
    -- read access to that calendar: anyone with database access can read
    -- the calendar it points at. That is the trade the feature makes, and
    -- the admin page says so where a leader pastes it.
    url          text        NOT NULL,
    -- Imported events are created as members-only by default. A leader
    -- who wants the council calendar on the public site can switch it.
    visibility   visibility  NOT NULL DEFAULT 'members',
    enabled      boolean     NOT NULL DEFAULT true,
    last_fetched_at timestamptz,
    -- "ok" or a short human-readable failure, shown on the admin page so
    -- a feed that quietly stopped working is visible rather than just
    -- absent. NULL until first fetched.
    last_status  text,
    last_event_count integer,
    created_by   uuid        NOT NULL REFERENCES members(id),
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (unit_id, url)
);

CREATE INDEX idx_calendar_feeds_unit ON calendar_feeds (unit_id);

-- Which imported event came from where.
--
-- feed_id NULL means an ordinary event somebody created on this site;
-- set means it was copied from that feed and is managed by it. Deleting a
-- feed takes its imported events with it, which is the behaviour a leader
-- expects from "remove this calendar".
ALTER TABLE events ADD COLUMN feed_id uuid REFERENCES calendar_feeds(id) ON DELETE CASCADE;

-- The source calendar's own UID for the event. Together with feed_id this
-- is what makes a re-import an update rather than a duplicate: the
-- importer matches on it, so a meeting whose time moved in Google moves
-- here too instead of appearing twice.
ALTER TABLE events ADD COLUMN external_uid text;

CREATE UNIQUE INDEX idx_events_feed_external_uid
    ON events (feed_id, external_uid)
    WHERE feed_id IS NOT NULL;
