-- 0053_group_email.sql
--
-- Emailing something other than a newsletter: a news post, or an update to
-- one den or patrol. Two things are needed, and both are here.
--
--   1. A capability for it, because "who may email a patrol" is not the
--      same question as "who may edit the site". A Patrol Leader may email
--      their own patrol without being able to touch the homepage.
--   2. A way to record what was sent and to whom, which is exactly what a
--      newsletter already is. Rather than a second copy of the pipeline
--      (claiming, pacing, recording each address, the audit entry), a
--      newsletters row now also records a news email or a group update, and
--      says who it was addressed to.

-- 1. The capability ------------------------------------------------------
--
-- email_group: send an update to the families of the den/patrol a role is
-- assigned to — or of every den/patrol, for a unit-wide leader. See
-- units.CapEmailGroup. Both tables that list the capabilities by name are
-- widened; they are the same list as units.AllCapabilities and have fallen
-- behind it before (see migration 0039).
ALTER TABLE custom_roles DROP CONSTRAINT custom_roles_capabilities_check;
ALTER TABLE custom_roles ADD CONSTRAINT custom_roles_capabilities_check
    CHECK (capabilities <@ ARRAY[
        'edit_content',
        'approve_submissions',
        'submit_for_approval',
        'manage_ledger',
        'approve_expenses',
        'super_admin',
        'email_group'
    ]::text[]);

ALTER TABLE role_capability_overrides DROP CONSTRAINT role_capability_overrides_capabilities_check;
ALTER TABLE role_capability_overrides ADD CONSTRAINT role_capability_overrides_capabilities_check
    CHECK (capabilities <@ ARRAY[
        'edit_content',
        'approve_submissions',
        'submit_for_approval',
        'manage_ledger',
        'approve_expenses',
        'super_admin',
        'email_group'
    ]::text[]);

-- A unit that has already edited what a built-in role grants has stored the
-- whole list, which replaces the code's defaults — so a role it customised
-- would otherwise come out of this migration without the new capability
-- that role gets everywhere else. That would be the leaders who touched
-- their settings losing a feature the ones who didn't get.
--
-- The new capability follows what the role already was: an adult leader who
-- still has edit_content gets it, and a Patrol Leader who still has
-- submit_for_approval gets it. A role the unit stripped of those is left
-- exactly as the unit left it.
UPDATE role_capability_overrides
SET capabilities = array_append(capabilities, 'email_group')
WHERE NOT ('email_group' = ANY(capabilities))
  AND (
        (role_slug IN ('cubmaster', 'den_leader', 'scoutmaster', 'assistant_scoutmaster', 'super_admin')
            AND 'edit_content' = ANY(capabilities))
     OR (role_slug = 'patrol_leader' AND 'submit_for_approval' = ANY(capabilities))
  );

-- 2. What was sent, and to whom -------------------------------------------
--
-- kind says what the row is. 'newsletter' is everything that existed before
-- this migration; 'news' is a news post emailed to families; 'group_update'
-- is a message to one or more dens/patrols.
ALTER TABLE newsletters
    ADD COLUMN kind text NOT NULL DEFAULT 'newsletter'
        CHECK (kind IN ('newsletter', 'news', 'group_update'));

-- The post an email was made from, if it was made from one. SET NULL rather
-- than CASCADE: deleting a post must not delete the record that it was
-- emailed to forty families.
ALTER TABLE newsletters
    ADD COLUMN post_id uuid REFERENCES content_pages(id) ON DELETE SET NULL;
CREATE INDEX idx_newsletters_post_id ON newsletters(post_id) WHERE post_id IS NOT NULL;

-- Who it is addressed to. 'unit' is every family on the roster, as a
-- newsletter always has been; 'groups' is only the families of the dens/
-- patrols listed in newsletter_audience_groups.
--
-- An explicit column, not "no rows means everyone". The two read the same
-- until the day a group is deleted between a send being claimed and the
-- send running: its audience row would cascade away, "no rows" would then
-- mean the whole unit, and a message meant for one patrol would go to every
-- family. With the column, a groups audience whose rows have gone reaches
-- nobody.
ALTER TABLE newsletters
    ADD COLUMN audience text NOT NULL DEFAULT 'unit'
        CHECK (audience IN ('unit', 'groups'));

CREATE TABLE newsletter_audience_groups (
    newsletter_id uuid NOT NULL REFERENCES newsletters(id) ON DELETE CASCADE,
    sub_group_id  uuid NOT NULL REFERENCES sub_groups(id) ON DELETE CASCADE,
    PRIMARY KEY (newsletter_id, sub_group_id)
);
CREATE INDEX idx_newsletter_audience_groups_group ON newsletter_audience_groups(sub_group_id);
