-- 0016_public_files.sql
--
-- The file library is members-only by design — every /files/{id}/download
-- normally requires being logged in. The homepage is public, though (hero
-- image, program photo, gallery strip), so a leader picking an existing
-- library photo for one of those slots needs a way to say "this specific
-- photo is fine to show to logged-out visitors too." is_public defaults to
-- false: a file only becomes publicly servable when a leader explicitly
-- flips it, never implicitly by being selected somewhere.
ALTER TABLE files ADD COLUMN is_public boolean NOT NULL DEFAULT false;
