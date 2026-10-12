package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// MetaChangesToken is the meta key that holds the Drive changes page token.
const MetaChangesToken = "changes_page_token"

// GetMeta returns the value stored under key, and false when there is none.
func (r *Repo) GetMeta(ctx context.Context, key string) (string, bool, error) {
	type got struct {
		v  string
		ok bool
	}
	g, err := call(ctx, r, func(db *sql.DB) (got, error) {
		var v string
		err := db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
		if errors.Is(err, sql.ErrNoRows) {
			return got{}, nil
		}
		return got{v, err == nil}, err
	})
	if err != nil {
		return "", false, fmt.Errorf("get meta %q: %w", key, err)
	}
	return g.v, g.ok, nil
}

// SetMeta stores value under key, replacing any earlier value.
func (r *Repo) SetMeta(ctx context.Context, key, value string) error {
	return r.exec(ctx, "set meta", `
INSERT INTO meta (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
}
