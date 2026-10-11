// Package store owns the SQLite state database (a rebuildable cache) and its
// migrations. Open applies the pragmas and runs the embedded goose migrations;
// the returned *sql.DB must be owned by a single goroutine. Repo is that owner
// for the files table: OpenRepo starts the goroutine, and typed methods
// (Upsert, Get, GetByDriveID, List, Delete) talk to it over a channel.
// RecordUpload and Revisions keep the file_revisions history written on each
// upload. EnqueueJobs, ClaimJob, CompleteJob, FailJob, RequeueJob and
// ResetRunningJobs are the persistent job queue (the jobs table) that a sync
// drains, so interrupted work resumes. RecordError, ClearError and ListErrors
// keep the sync_errors table: one readable row per file that is failing.
package store
