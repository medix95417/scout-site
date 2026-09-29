-- Per-unit prospect statuses, and a second way to sort prospects.
--
-- Two changes, one table.
--
-- The five statuses shipped as a Postgres enum, which made them the
-- same five for every unit forever: a Troop that wants "Left voicemail"
-- between "New enquiry" and "Contacted" needed a migration and a
-- release to get it. They move here, one row per status per unit, so a
-- leader can add their own — the same shape as custom roles, where the
-- system ships sensible defaults and a unit adjusts them.
--
-- And a prospect now has a category alongside its status. The status is
-- where a family has got to and moves as they progress; a category is
-- something that doesn't move — which program they asked about, how
-- they heard about the unit — and overloading one dropdown with both
-- was the alternative. Both lists live in this table, told apart by
-- kind, the same way approval_requests uses entity_type and
-- content_pages uses page_type.
--
-- Retiring rather than deleting is the point of retired_at. A status a
-- unit has stopped using is still on the prospects who were given it,
-- and still named in the audience of every campaign that targeted it,
-- so deleting the row would leave those reading as a bare slug or
-- nothing at all. A retired label keeps its label text and is simply no
-- longer offered for anything new.

CREATE TABLE prospect_labels (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    unit_id    uuid NOT NULL REFERENCES units(id) ON DELETE CASCADE,

    -- 'status' — where the family has got to; 'category' — how the unit
    -- files them. Same table, because the two need identical handling:
    -- add, rename, reorder, retire.
    kind       text NOT NULL CHECK (kind IN ('status', 'category')),

    -- value is the stable identifier written into prospects.status /
    -- prospects.category and into prospect_campaigns.target_statuses.
    -- Generated from the label once, on creation, and never changed
    -- after: renaming a label must not orphan the rows using it.
    value      text NOT NULL CHECK (value ~ '^[a-z0-9][a-z0-9_-]{0,39}$'),
    label      text NOT NULL CHECK (length(btrim(label)) BETWEEN 1 AND 60),

    -- Only meaningful for kind='status': whether a prospect at this
    -- status has been dealt with, and so drops out of the default "open
    -- enquiries" list. Was hardcoded as "joined or declined".
    closed     boolean NOT NULL DEFAULT false,

    sort_order int NOT NULL DEFAULT 0,
    retired_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),

    UNIQUE (unit_id, kind, value)
);

CREATE INDEX prospect_labels_unit_kind_idx ON prospect_labels (unit_id, kind, sort_order);

-- The enum becomes plain text, since the permitted values are now a
-- per-unit question this table answers rather than a global one the
-- type can. Existing values carry over unchanged — they are exactly the
-- five seeded below.
ALTER TABLE prospects ALTER COLUMN status DROP DEFAULT;
ALTER TABLE prospects ALTER COLUMN status TYPE text USING status::text;
ALTER TABLE prospects ALTER COLUMN status SET DEFAULT 'new';
ALTER TABLE prospects ADD CONSTRAINT prospects_status_shape
    CHECK (status ~ '^[a-z0-9][a-z0-9_-]{0,39}$');

DROP TYPE prospect_status;

-- '' means uncategorised, which is what every existing prospect is and
-- what a new enquiry from the public form still is: a category is a
-- leader's filing decision, and the form doesn't ask for one.
ALTER TABLE prospects ADD COLUMN category text NOT NULL DEFAULT ''
    CHECK (category = '' OR category ~ '^[a-z0-9][a-z0-9_-]{0,39}$');

-- Every existing unit keeps exactly the workflow it had. New installs
-- get the same five from seed.sql, and a unit that somehow has none is
-- given them on read (see prospect.EnsureDefaultLabels), so a dropdown
-- is never empty.
INSERT INTO prospect_labels (unit_id, kind, value, label, closed, sort_order)
SELECT u.id, 'status', d.value, d.label, d.closed, d.sort_order
FROM units u
CROSS JOIN (VALUES
    ('new',       'New enquiry',       false, 1),
    ('contacted', 'Contacted',         false, 2),
    ('visited',   'Visited a meeting', false, 3),
    ('joined',    'Joined',            true,  4),
    ('declined',  'Not joining',       true,  5)
) AS d(value, label, closed, sort_order)
ON CONFLICT (unit_id, kind, value) DO NOTHING;
