package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SyncError is the current problem with one file. Times are Unix nanoseconds.
type SyncError struct {
	RelPath string
	// Message is readable (from drive.Describe); Detail is the raw error text,
	// for debugging. Neither holds tokens or request bodies.
	Message   string
	Detail    string
	Attempts  int
	FirstSeen int64
	LastSeen  int64
}

// RecordError notes that the file at relPath failed. A file keeps one row: a
// repeat updates the message and counts another attempt.
func (r *Repo) RecordError(ctx context.Context, relPath, message, detail string) error {
	now := time.Now().UnixNano()
	return r.exec(ctx, "record error", `
INSERT INTO sync_errors (rel_path, message, detail, first_seen, last_seen) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(rel_path) DO UPDATE SET
    message = excluded.message, detail = excluded.detail,
    attempts = attempts + 1, last_seen = excluded.last_seen`,
		relPath, message, detail, now, now)
}

// ClearError removes the error of a file that is fine again. No row is not an
// error.
func (r *Repo) ClearError(ctx context.Context, relPath string) error {
	return r.exec(ctx, "clear error", `DELETE FROM sync_errors WHERE rel_path = ?`, relPath)
}

// ListErrors returns every current error, newest first.
func (r *Repo) ListErrors(ctx context.Context) ([]SyncError, error) {
	errs, err := call(ctx, r, func(db *sql.DB) ([]SyncError, error) {
		rows, err := db.Query(`
SELECT rel_path, message, detail, attempts, first_seen, last_seen
FROM sync_errors ORDER BY last_seen DESC, id DESC`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []SyncError
		for rows.Next() {
			var e SyncError
			if err := rows.Scan(&e.RelPath, &e.Message, &e.Detail, &e.Attempts, &e.FirstSeen, &e.LastSeen); err != nil {
				return nil, err
			}
			out = append(out, e)
		}
		return out, rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list errors: %w", err)
	}
	return errs, nil
}
