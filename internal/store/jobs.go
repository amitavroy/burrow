package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Job is one queued piece of work: the file at RelPath needs syncing.
type Job struct {
	ID       int64
	RelPath  string
	Attempts int
}

// EnqueueJobs queues a job for each rel_path in one transaction. A path that
// already has a job is not duplicated: a pending or failed job goes back to
// pending. A running job keeps running but is marked dirty, so it is queued
// again when it finishes: the file changed while it was being uploaded.
func (r *Repo) EnqueueJobs(ctx context.Context, relPaths []string) error {
	_, err := call(ctx, r, func(db *sql.DB) (struct{}, error) {
		tx, err := db.Begin()
		if err != nil {
			return struct{}{}, err
		}
		defer tx.Rollback()
		stmt, err := tx.Prepare(`
INSERT INTO jobs (rel_path, created_at, updated_at) VALUES (?, ?, ?)
ON CONFLICT(rel_path) DO UPDATE SET
    status     = CASE WHEN jobs.status = 'running' THEN 'running' ELSE 'pending' END,
    dirty      = CASE WHEN jobs.status = 'running' THEN 1 ELSE jobs.dirty END,
    updated_at = excluded.updated_at`)
		if err != nil {
			return struct{}{}, err
		}
		defer stmt.Close()
		now := time.Now().UnixNano()
		for _, p := range relPaths {
			if _, err := stmt.Exec(p, now, now); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, tx.Commit()
	})
	if err != nil {
		return fmt.Errorf("enqueue jobs: %w", err)
	}
	return nil
}

// ClaimJob marks the oldest pending job running and returns it, or ErrNoJob.
// It is one statement on the owner goroutine, so no two callers get the same job.
func (r *Repo) ClaimJob(ctx context.Context) (Job, error) {
	j, err := call(ctx, r, func(db *sql.DB) (Job, error) {
		var j Job
		err := db.QueryRow(`
UPDATE jobs SET status = 'running', updated_at = ?
WHERE id = (SELECT id FROM jobs WHERE status = 'pending' ORDER BY id LIMIT 1)
RETURNING id, rel_path, attempts`, time.Now().UnixNano()).Scan(&j.ID, &j.RelPath, &j.Attempts)
		if errors.Is(err, sql.ErrNoRows) {
			return Job{}, ErrNoJob
		}
		return j, err
	})
	if err != nil {
		if errors.Is(err, ErrNoJob) {
			return Job{}, ErrNoJob
		}
		return Job{}, fmt.Errorf("claim job: %w", err)
	}
	return j, nil
}

// CompleteJob removes a finished job; the files and file_revisions rows hold
// the outcome. A job marked dirty (its path was queued again while it ran) goes
// back to pending instead.
func (r *Repo) CompleteJob(ctx context.Context, id int64) error {
	_, err := call(ctx, r, func(db *sql.DB) (struct{}, error) {
		tx, err := db.Begin()
		if err != nil {
			return struct{}{}, err
		}
		defer tx.Rollback()
		// Delete first: the update below turns dirty rows into clean ones.
		if _, err := tx.Exec(`DELETE FROM jobs WHERE id = ? AND dirty = 0`, id); err != nil {
			return struct{}{}, err
		}
		if _, err := tx.Exec(`UPDATE jobs SET status = 'pending', dirty = 0, updated_at = ? WHERE id = ? AND dirty = 1`,
			time.Now().UnixNano(), id); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, tx.Commit()
	})
	if err != nil {
		return fmt.Errorf("complete job: %w", err)
	}
	return nil
}

// FailJob marks a job failed, counts the attempt and keeps msg. A failed job
// waits for the next EnqueueJobs of its path or for RetryFailedJobs. A dirty
// job (queued again while it ran) goes to pending instead, since the file has
// changed since the failed attempt.
func (r *Repo) FailJob(ctx context.Context, id int64, msg string) error {
	return r.exec(ctx, "fail job",
		`UPDATE jobs SET status = CASE WHEN dirty = 1 THEN 'pending' ELSE 'failed' END, dirty = 0,
		 attempts = attempts + 1, last_error = ?, updated_at = ? WHERE id = ?`,
		msg, time.Now().UnixNano(), id)
}

// RequeueJob puts a job back to pending, for work cut short by a cancel or a
// lost sign-in.
func (r *Repo) RequeueJob(ctx context.Context, id int64) error {
	return r.exec(ctx, "requeue job",
		`UPDATE jobs SET status = 'pending', dirty = 0, updated_at = ? WHERE id = ?`, time.Now().UnixNano(), id)
}

// ResetRunningJobs puts jobs left running by a dead process back to pending
// and returns how many. Only one process may use the database at a time.
func (r *Repo) ResetRunningJobs(ctx context.Context) (int, error) {
	n, err := call(ctx, r, func(db *sql.DB) (int64, error) {
		res, err := db.Exec(`UPDATE jobs SET status = 'pending', dirty = 0, updated_at = ? WHERE status = 'running'`, time.Now().UnixNano())
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	})
	if err != nil {
		return 0, fmt.Errorf("reset running jobs: %w", err)
	}
	return int(n), nil
}

// RetryFailedJobs puts every failed job back to pending and returns how many,
// for a long-running process that retries on a timer.
func (r *Repo) RetryFailedJobs(ctx context.Context) (int, error) {
	n, err := call(ctx, r, func(db *sql.DB) (int64, error) {
		res, err := db.Exec(`UPDATE jobs SET status = 'pending', updated_at = ? WHERE status = 'failed'`, time.Now().UnixNano())
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	})
	if err != nil {
		return 0, fmt.Errorf("retry failed jobs: %w", err)
	}
	return int(n), nil
}

// CountJobs returns the number of jobs still to do (pending or running).
func (r *Repo) CountJobs(ctx context.Context) (int, error) {
	n, err := call(ctx, r, func(db *sql.DB) (int, error) {
		var n int
		err := db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE status IN ('pending', 'running')`).Scan(&n)
		return n, err
	})
	if err != nil {
		return 0, fmt.Errorf("count jobs: %w", err)
	}
	return n, nil
}

func (r *Repo) exec(ctx context.Context, what, query string, args ...any) error {
	_, err := call(ctx, r, func(db *sql.DB) (struct{}, error) {
		_, err := db.Exec(query, args...)
		return struct{}{}, err
	})
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}
