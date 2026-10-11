-- +goose Up
CREATE TABLE sync_errors (
    id         INTEGER PRIMARY KEY,
    rel_path   TEXT    NOT NULL UNIQUE,
    message    TEXT    NOT NULL,
    detail     TEXT    NOT NULL DEFAULT '',
    attempts   INTEGER NOT NULL DEFAULT 1,
    first_seen INTEGER NOT NULL,
    last_seen  INTEGER NOT NULL
);

CREATE INDEX sync_errors_last_seen ON sync_errors (last_seen DESC);

-- +goose Down
DROP INDEX sync_errors_last_seen;
DROP TABLE sync_errors;
