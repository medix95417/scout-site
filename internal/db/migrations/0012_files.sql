-- 0012_files.sql
--
-- General file storage plus event photos — see internal/files. Both share
-- one table: an "event photo" is just a file whose category says so and
-- that's (usually) linked to the event it was taken at via event_files. The
-- category exists mainly to separate the two upload flows/UI surfaces
-- (a general document library vs. an event's photo strip), not because the
-- underlying storage differs.
--
-- Actual bytes live in S3-compatible object storage (see internal/storage),
-- not the database — storage_key is the object key there. Files table rows
-- are metadata plus that pointer, same division of labor as any other
-- "database stores metadata, object storage stores bytes" design.
CREATE TYPE file_category AS ENUM ('general', 'event_photo');

CREATE TABLE files (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    unit_id      uuid NOT NULL REFERENCES units(id) ON DELETE CASCADE,
    filename     text NOT NULL,
    content_type text NOT NULL,
    size_bytes   bigint NOT NULL,
    storage_key  text NOT NULL UNIQUE,
    category     file_category NOT NULL DEFAULT 'general',
    uploaded_by  uuid REFERENCES members(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_files_unit_id ON files(unit_id, created_at DESC);

-- Many-to-many on purpose — the same permission slip PDF or packing list
-- often applies to several events (a recurring campout, a multi-session
-- class), and the user asked for exactly that: "allow files to be linked
-- to calendar events, sometimes the same file for many events."
CREATE TABLE event_files (
    event_id   uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    file_id    uuid NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, file_id)
);
CREATE INDEX idx_event_files_file_id ON event_files(file_id);
