package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
)

// Repo is the only way to reach the database after startup. One goroutine
// owns the *sql.DB; typed methods send it requests over a channel, so callers
// never see SQL or the connection. Never log SQL arguments.
type Repo struct {
	mu     sync.RWMutex // guards closed and sending on reqs
	closed bool
	reqs   chan request
	done   chan struct{}

	closeErr error
}

type result struct {
	val any
	err error
}

// request is run on the owner goroutine; reply carries the result back.
type request struct {
	run   func(*sql.DB) (any, error)
	reply chan result
}

// OpenRepo opens and migrates the database at path and starts the owner
// goroutine.
func OpenRepo(path string) (*Repo, error) {
	db, err := Open(path)
	if err != nil {
		return nil, err
	}
	r := &Repo{
		reqs: make(chan request),
		done: make(chan struct{}),
	}
	go r.loop(db)
	return r, nil
}

func (r *Repo) loop(db *sql.DB) {
	defer close(r.done)
	for req := range r.reqs {
		req.reply <- safeRun(db, req.run)
	}
	r.closeErr = db.Close()
}

// safeRun recovers a panic inside a request so the owner keeps serving. The
// panic value is left out of the error since it could carry arguments.
func safeRun(db *sql.DB, run func(*sql.DB) (any, error)) (res result) {
	defer func() {
		if p := recover(); p != nil {
			res = result{err: errors.New("store: panic in database request")}
		}
	}()
	val, err := run(db)
	return result{val: val, err: err}
}

// call runs fn on the owner goroutine and returns its typed result, so the
// methods below never touch the untyped reply.
func call[T any](ctx context.Context, r *Repo, fn func(*sql.DB) (T, error)) (T, error) {
	v, err := r.do(ctx, func(db *sql.DB) (any, error) { return fn(db) })
	if err != nil {
		var zero T
		return zero, err
	}
	return v.(T), nil
}

// do hands run to the owner goroutine and waits for the reply. A cancelled
// context is honoured while sending and while waiting; the owner still
// finishes a statement it already started.
func (r *Repo) do(ctx context.Context, run func(*sql.DB) (any, error)) (any, error) {
	reply := make(chan result, 1) // buffered so the owner never blocks on a gone caller

	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return nil, ErrClosed
	}
	select {
	case r.reqs <- request{run: run, reply: reply}:
		r.mu.RUnlock()
	case <-ctx.Done():
		r.mu.RUnlock()
		return nil, ctx.Err()
	}

	select {
	case res := <-reply:
		return res.val, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close stops accepting requests, waits for the owner goroutine to finish and
// closes the database. It is safe to call more than once.
func (r *Repo) Close() error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.reqs)
	}
	r.mu.Unlock()
	<-r.done
	return r.closeErr
}

// Upsert inserts f or updates the row with the same RelPath.
func (r *Repo) Upsert(ctx context.Context, f File) error {
	_, err := call(ctx, r, func(db *sql.DB) (struct{}, error) {
		_, err := db.Exec(`
INSERT INTO files (rel_path, drive_file_id, size, mtime, inode, local_md5, synced_md5, base_md5, base_revision_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(rel_path) DO UPDATE SET
    drive_file_id    = excluded.drive_file_id,
    size             = excluded.size,
    mtime            = excluded.mtime,
    inode            = excluded.inode,
    local_md5        = excluded.local_md5,
    synced_md5       = excluded.synced_md5,
    base_md5         = excluded.base_md5,
    base_revision_id = excluded.base_revision_id`,
			f.RelPath, nullString(f.DriveFileID), f.Size, f.MTime, nullInode(f.Inode),
			nullString(f.LocalMD5), nullString(f.SyncedMD5), nullString(f.BaseMD5), nullString(f.BaseRevisionID))
		return struct{}{}, err
	})
	if err != nil {
		return fmt.Errorf("upsert %q: %w", f.RelPath, err)
	}
	return nil
}

const fileColumns = `rel_path, drive_file_id, size, mtime, inode, local_md5, synced_md5, base_md5, base_revision_id`

// Get returns the file with the given RelPath, or ErrNotFound.
func (r *Repo) Get(ctx context.Context, relPath string) (File, error) {
	f, err := call(ctx, r, func(db *sql.DB) (File, error) {
		return scanFile(db.QueryRow(`SELECT `+fileColumns+` FROM files WHERE rel_path = ?`, relPath))
	})
	if err != nil {
		return File{}, fmt.Errorf("get %q: %w", relPath, err)
	}
	return f, nil
}

// GetByDriveID returns the file with the given Drive file ID, or ErrNotFound.
func (r *Repo) GetByDriveID(ctx context.Context, id string) (File, error) {
	f, err := call(ctx, r, func(db *sql.DB) (File, error) {
		return scanFile(db.QueryRow(`SELECT `+fileColumns+` FROM files WHERE drive_file_id = ?`, id))
	})
	if err != nil {
		return File{}, fmt.Errorf("get by drive id: %w", err)
	}
	return f, nil
}

// Delete removes the file with the given RelPath, or returns ErrNotFound.
func (r *Repo) Delete(ctx context.Context, relPath string) error {
	_, err := call(ctx, r, func(db *sql.DB) (struct{}, error) {
		res, err := db.Exec(`DELETE FROM files WHERE rel_path = ?`, relPath)
		if err != nil {
			return struct{}{}, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return struct{}{}, err
		}
		if n == 0 {
			return struct{}{}, ErrNotFound
		}
		return struct{}{}, nil
	})
	if err != nil {
		return fmt.Errorf("delete %q: %w", relPath, err)
	}
	return nil
}

// List returns every file ordered by RelPath.
func (r *Repo) List(ctx context.Context) ([]File, error) {
	files, err := call(ctx, r, func(db *sql.DB) ([]File, error) {
		rows, err := db.Query(`SELECT ` + fileColumns + ` FROM files ORDER BY rel_path`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var files []File
		for rows.Next() {
			f, err := scanFile(rows)
			if err != nil {
				return nil, err
			}
			files = append(files, f)
		}
		return files, rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list files: %w", err)
	}
	return files, nil
}

// rowScanner is satisfied by *sql.Row and *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

func scanFile(row rowScanner) (File, error) {
	var (
		f                                                   File
		driveID, localMD5, syncedMD5, baseMD5, baseRevision sql.Null[string]
		inode                                               sql.Null[int64]
	)
	err := row.Scan(&f.RelPath, &driveID, &f.Size, &f.MTime, &inode, &localMD5, &syncedMD5, &baseMD5, &baseRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, ErrNotFound
	}
	if err != nil {
		return File{}, err
	}
	f.DriveFileID = driveID.V
	f.Inode = uint64(inode.V)
	f.LocalMD5 = localMD5.V
	f.SyncedMD5 = syncedMD5.V
	f.BaseMD5 = baseMD5.V
	f.BaseRevisionID = baseRevision.V
	return f, nil
}

// nullString maps "" to NULL.
func nullString(s string) sql.Null[string] {
	return sql.Null[string]{V: s, Valid: s != ""}
}

// nullInode maps 0 to NULL. SQLite stores int64, so the uint64 is
// reinterpreted bit for bit and converted back on read.
func nullInode(i uint64) sql.Null[int64] {
	return sql.Null[int64]{V: int64(i), Valid: i != 0}
}
