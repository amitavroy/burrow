-- +goose Up
CREATE TABLE files (
    id               INTEGER PRIMARY KEY,
    rel_path         TEXT    NOT NULL UNIQUE,
    drive_file_id    TEXT,
    size             INTEGER NOT NULL DEFAULT 0,
    mtime            INTEGER NOT NULL DEFAULT 0,
    inode            INTEGER,
    local_md5        TEXT,
    synced_md5       TEXT,
    base_md5         TEXT,
    base_revision_id TEXT
);

CREATE UNIQUE INDEX files_drive_file_id ON files (drive_file_id) WHERE drive_file_id IS NOT NULL;

-- +goose Down
DROP INDEX files_drive_file_id;
DROP TABLE files;
