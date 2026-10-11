-- +goose Up
CREATE TABLE jobs (
    id         INTEGER PRIMARY KEY,
    rel_path   TEXT    NOT NULL UNIQUE,
    status     TEXT    NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'failed')),
    attempts   INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX jobs_status_id ON jobs (status, id);

-- +goose Down
DROP INDEX jobs_status_id;
DROP TABLE jobs;
