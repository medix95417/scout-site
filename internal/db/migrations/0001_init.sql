-- 0001_init.sql
-- Phase 1 schema. See scout-website-architecture-phase1.md Section 4 for the
-- rationale behind each table's shape — the short version: every table Phase 2's
-- fund ledger will need to reference (members, units, events) has a stable uuid
-- primary key here, so Phase 2 adds new tables with foreign keys into this
-- schema instead of altering it.

-- ---------------------------------------------------------------------------
-- Households / accounts
-- ---------------------------------------------------------------------------

CREATE TABLE families (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL, -- e.g. "The Smith Family", used in leader-facing UI
    created_at timestamptz NOT NULL DEFAULT now()
);

-- email is stored lowercased by the application (see internal/auth) and
-- compared case-insensitively that way, rather than relying on the citext
-- extension — one fewer moving part for a self-hosted deploy to worry about.
CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    family_id     uuid NOT NULL REFERENCES families(id) ON DELETE CASCADE,
    email         text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    token      text PRIMARY KEY, -- opaque, cryptographically random
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_sessions_user_id ON sessions(user_id);

-- ---------------------------------------------------------------------------
-- People: adults and youth, both stored as "members" of a family
-- ---------------------------------------------------------------------------

CREATE TYPE member_type AS ENUM ('adult', 'youth');

CREATE TABLE members (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    family_id   uuid NOT NULL REFERENCES families(id) ON DELETE CASCADE,
    first_name  text NOT NULL,
    last_name   text NOT NULL,
    member_type member_type NOT NULL,
    birthdate   date, -- nullable; useful for youth aging-out logic later, optional for adults
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_members_family_id ON members(family_id);

-- ---------------------------------------------------------------------------
-- Units (Troop / Pack) and the dens/patrols beneath them
-- ---------------------------------------------------------------------------

CREATE TYPE unit_type AS ENUM ('troop', 'pack');
CREATE TYPE sub_group_type AS ENUM ('den', 'patrol');

CREATE TABLE units (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug        text NOT NULL UNIQUE,          -- e.g. "troop-47", "pack-47"
    name        text NOT NULL,                 -- e.g. "Troop 47"
    unit_type   unit_type NOT NULL,
    hostname    text NOT NULL UNIQUE,           -- e.g. "troop.47-yonkers.org"
    theme_color text NOT NULL DEFAULT '#1f2937',
    logo_url    text,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sub_groups (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    unit_id        uuid NOT NULL REFERENCES units(id) ON DELETE CASCADE,
    name           text NOT NULL,               -- e.g. "Bear Den 3", "Eagle Patrol"
    sub_group_type sub_group_type NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_sub_groups_unit_id ON sub_groups(unit_id);

-- ---------------------------------------------------------------------------
-- Roles — one generic table for both membership and leadership roles.
-- Phase 2's "treasurer" role is just a new allowed value here, not a new table.
-- ---------------------------------------------------------------------------

CREATE TYPE member_role AS ENUM (
    'super_admin',
    'cubmaster',
    'den_leader',
    'scoutmaster',
    'assistant_scoutmaster',
    'senior_patrol_leader',
    'patrol_leader',
    'parent',
    'scout'
);

CREATE TABLE role_assignments (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    member_id   uuid NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    unit_id     uuid NOT NULL REFERENCES units(id) ON DELETE CASCADE,
    sub_group_id uuid REFERENCES sub_groups(id) ON DELETE CASCADE, -- null = whole-unit scope
    role        member_role NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (member_id, unit_id, sub_group_id, role)
);
CREATE INDEX idx_role_assignments_member_id ON role_assignments(member_id);
CREATE INDEX idx_role_assignments_unit_id ON role_assignments(unit_id);

-- ---------------------------------------------------------------------------
-- Calendar
-- ---------------------------------------------------------------------------

CREATE TYPE visibility AS ENUM ('public', 'members');
CREATE TYPE content_status AS ENUM ('draft', 'pending_approval', 'published', 'rejected');

CREATE TABLE events (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    unit_id      uuid NOT NULL REFERENCES units(id) ON DELETE CASCADE,
    sub_group_id uuid REFERENCES sub_groups(id) ON DELETE CASCADE,
    title        text NOT NULL,
    description  text,
    location     text,
    starts_at    timestamptz NOT NULL,
    ends_at      timestamptz,
    visibility   visibility NOT NULL DEFAULT 'members',
    status       content_status NOT NULL DEFAULT 'published',
    created_by   uuid NOT NULL REFERENCES members(id),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_events_unit_id ON events(unit_id);
CREATE INDEX idx_events_starts_at ON events(starts_at);

CREATE TYPE rsvp_response AS ENUM ('yes', 'no', 'maybe');

CREATE TABLE rsvps (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id   uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    member_id  uuid NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    response   rsvp_response NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (event_id, member_id)
);

-- ---------------------------------------------------------------------------
-- Content: announcements, public pages, photo galleries (Phase 1 stores
-- gallery images as content_pages with page_type='gallery' and body holding
-- a JSON list of image URLs — deliberately simple; revisit if it outgrows this)
-- ---------------------------------------------------------------------------

CREATE TYPE content_page_type AS ENUM ('page', 'post', 'gallery');

CREATE TABLE content_pages (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    unit_id      uuid NOT NULL REFERENCES units(id) ON DELETE CASCADE,
    sub_group_id uuid REFERENCES sub_groups(id) ON DELETE CASCADE,
    slug         text NOT NULL,
    title        text NOT NULL,
    body         text NOT NULL DEFAULT '',
    page_type    content_page_type NOT NULL DEFAULT 'post',
    visibility   visibility NOT NULL DEFAULT 'members',
    status       content_status NOT NULL DEFAULT 'published',
    created_by   uuid NOT NULL REFERENCES members(id),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (unit_id, slug)
);
CREATE INDEX idx_content_pages_unit_id ON content_pages(unit_id);

-- ---------------------------------------------------------------------------
-- Generic approval workflow — reused unchanged by Phase 2 (e.g. treasurer
-- sign-off on a trip-fund transfer) by adding a new entity_type value only.
-- ---------------------------------------------------------------------------

CREATE TYPE approval_status AS ENUM ('pending', 'approved', 'rejected');

CREATE TABLE approval_requests (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    entity_type  text NOT NULL,   -- e.g. 'event'
    entity_id    uuid NOT NULL,
    unit_id      uuid NOT NULL REFERENCES units(id) ON DELETE CASCADE,
    submitted_by uuid NOT NULL REFERENCES members(id),
    status       approval_status NOT NULL DEFAULT 'pending',
    decided_by   uuid REFERENCES members(id),
    decided_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_approval_requests_entity ON approval_requests(entity_type, entity_id);
CREATE INDEX idx_approval_requests_status ON approval_requests(unit_id, status);

-- ---------------------------------------------------------------------------
-- Generic audit log — every write of consequence lands here. Phase 2's ledger
-- transactions log here too with entity_type='ledger_entry'; no schema change.
-- ---------------------------------------------------------------------------

CREATE TABLE audit_log (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    entity_type         text NOT NULL,
    entity_id           uuid NOT NULL,
    actor_id            uuid REFERENCES members(id), -- null for system-initiated actions (e.g. CSV import)
    action              text NOT NULL,               -- e.g. 'create', 'update', 'delete', 'approve', 'reject'
    before_state        jsonb,
    after_state         jsonb,
    approval_request_id uuid REFERENCES approval_requests(id),
    occurred_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_audit_log_entity ON audit_log(entity_type, entity_id);
CREATE INDEX idx_audit_log_occurred_at ON audit_log(occurred_at);

-- ---------------------------------------------------------------------------
-- Advancement (populated by the Scoutbook CSV import job — see
-- internal/scoutbook. Scoutbook remains the system of record; this is a
-- read-optimized mirror for display on the site.)
-- ---------------------------------------------------------------------------

CREATE TABLE advancement_records (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    member_id    uuid NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    unit_id      uuid NOT NULL REFERENCES units(id) ON DELETE CASCADE,
    record_type  text NOT NULL,   -- 'rank' or 'badge'
    name         text NOT NULL,   -- e.g. "Life", "First Aid Merit Badge"
    earned_date  date,
    source       text NOT NULL DEFAULT 'scoutbook_csv',
    imported_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_advancement_records_member_id ON advancement_records(member_id);
