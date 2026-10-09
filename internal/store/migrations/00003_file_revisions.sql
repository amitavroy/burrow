-- +goose Up
CREATE TABLE file_revisions (
    id            INTEGER PRIMARY KEY,
    drive_file_id TEXT    NOT NULL,
    revision_id   TEXT    NOT NULL,
    md5           TEXT,
    size          INTEGER NOT NULL DEFAULT 0,
    time          INTEGER NOT NULL DEFAULT 0,
    source        TEXT    NOT NULL,
    UNIQUE (drive_file_id, revision_id)
);

-- +goose Down
DROP TABLE file_revisions;
