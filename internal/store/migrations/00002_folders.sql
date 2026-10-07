-- +goose Up
CREATE TABLE folders (
    rel_dir         TEXT PRIMARY KEY,
    drive_folder_id TEXT NOT NULL
);

-- +goose Down
DROP TABLE folders;
