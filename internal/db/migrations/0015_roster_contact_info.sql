-- 0015_roster_contact_info.sql
--
-- Address is household-level (one per family, alongside the family the
-- rest of this schema already treats as the unit of a household); email
-- and phone numbers are per-person, since two adults in the same family
-- often have different ones. All nullable/default-false — a family that's
-- never filled these in should look exactly like it did before this
-- migration, both in what's stored and in what's shown to anyone else.
--
-- release_address/release_email/release_phone are opt-in (default false)
-- and self-service (see internal/family's SetFamilyAddress/
-- SetContactInfo — no CanEditUnitContent needed to change your own):
-- nothing here is visible to other families in the unit until whoever it
-- belongs to turns the matching toggle on themselves. release_phone
-- covers both home_phone and cell_phone together — the request was for
-- "address, email, and phone" as three release choices, not four.
ALTER TABLE families ADD COLUMN address text;
ALTER TABLE families ADD COLUMN release_address boolean NOT NULL DEFAULT false;

ALTER TABLE members ADD COLUMN email text;
ALTER TABLE members ADD COLUMN home_phone text;
ALTER TABLE members ADD COLUMN cell_phone text;
ALTER TABLE members ADD COLUMN release_email boolean NOT NULL DEFAULT false;
ALTER TABLE members ADD COLUMN release_phone boolean NOT NULL DEFAULT false;
