package sync

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	stdsync "sync"
	"time"

	"github.com/amitavroy/burrow/internal/drive"
	"github.com/amitavroy/burrow/internal/store"
)

// UploadFunc sends one local file to Drive and returns what Drive reports. A
// *drive.Uploader's Upload method is the real one; tests pass a fake.
type UploadFunc func(ctx context.Context, localPath, relPath string) (drive.FileInfo, error)

// Event reports the outcome for one file: Err is nil when it was uploaded or,
// with Updated set, replaced in place on Drive.
type Event struct {
	RelPath string
	Updated bool
	// MovedFrom is set when the file was a rename or move of a tracked file:
	// RelPath is the new path and nothing was uploaded.
	MovedFrom string
	// Trashed is set when the file is gone locally and was moved to the Drive
	// trash: RelPath is the old path.
	Trashed bool
	Err     error
}

// Summary counts what one Sync did.
type Summary struct {
	// Uploaded counts new files; Updated counts changed files replaced in place.
	Uploaded int
	Updated  int
	// Moved counts renamed or moved files, handled without an upload.
	Moved int
	// Trashed counts files deleted locally and moved to the Drive trash.
	Trashed int
	// Synced counts files skipped because Drive already has their content.
	Synced int
	Failed int
	// Ignored is the number of files and directories the ignore rules skipped.
	Ignored int
	// Unreadable are entries the scan could not read; their files were not
	// uploaded.
	Unreadable []error
}

// Sync scans root, queues a job for every file in the jobs table, and drains
// the queue one job at a time. Jobs left over from an interrupted run are
// picked up first (a job whose file is no longer in the scan is dropped). A
// file that already has a row with a Drive ID is skipped when its size and
// mtime match the row, or when its streamed MD5 equals Drive's (the row is
// then refreshed); otherwise its content is replaced in place on Drive. After
// each successful upload or update its row is written straight away (Drive ID,
// Drive's MD5, and the size and mtime seen by the scan, which are never newer
// than what was uploaded), then the job is deleted, so a killed run resumes
// where it stopped. A file that fails to upload is reported and counted, its
// job is marked failed, and the run goes on; the next run queues it again.
// Sync stops at once, returning the error, when the sign-in is gone
// (drive.ErrNotSignedIn or drive.ErrSessionExpired), when ctx is cancelled,
// when the root cannot be scanned, or when repo fails; the job in flight goes
// back to pending. The Summary is valid in every case.
func Sync(ctx context.Context, root string, repo *store.Repo, upload UploadFunc, report func(Event)) (Summary, error) {
	return SyncWorkers(ctx, root, repo, upload, report, 1)
}

// MoveFunc renames and moves the Drive file driveFileID to where newRel
// belongs. A *drive.Uploader's Move method is the real one.
type MoveFunc func(ctx context.Context, driveFileID, newRel string) (drive.FileInfo, error)

// TrashFunc moves the Drive file driveFileID to the Drive trash. A
// *drive.Uploader's Trash method is the real one.
type TrashFunc func(ctx context.Context, driveFileID string) error

// SyncOption tunes SyncWorkers.
type SyncOption func(*runState)

// WithMove lets the sync recognise a renamed or moved file (see tryMove) and
// move it in Drive instead of uploading it again.
func WithMove(m MoveFunc) SyncOption { return func(r *runState) { r.move = m } }

// WithTrash lets the sync trash the Drive file of a tracked file that is gone
// locally. Without it, deleted files are left alone in Drive.
func WithTrash(t TrashFunc) SyncOption { return func(r *runState) { r.trash = t } }

// SyncWorkers is Sync with workers jobs in flight at once (at least 1). The
// upload function must be safe for concurrent use when workers > 1; report is
// never called concurrently. The first fatal error stops every worker, each
// putting its job back in the queue, and is the error returned.
func SyncWorkers(ctx context.Context, root string, repo *store.Repo, upload UploadFunc, report func(Event), workers int, opts ...SyncOption) (Summary, error) {
	// No other process uses the database, so a job still marked running is
	// one a dead process left behind.
	if _, err := repo.ResetRunningJobs(ctx); err != nil {
		return Summary{}, err
	}
	plan, res, err := diff(ctx, root, repo)
	if err != nil {
		return Summary{}, err
	}
	run := &runState{sum: Summary{Ignored: res.Ignored, Unreadable: res.Errors}, report: report}
	for _, o := range opts {
		o(run)
	}
	entries := make(map[string]Entry, len(res.Entries))
	paths := make([]string, len(res.Entries))
	for i, e := range res.Entries {
		entries[e.RelPath], paths[i] = e, e.RelPath
	}
	if err := noteScanErrors(ctx, root, repo, res, entries); err != nil {
		return run.sum, err
	}
	// Missing files become jobs too: the worker trashes them in Drive. A
	// rename is not missing (diff pairs it with its new path).
	if err := repo.EnqueueJobs(ctx, append(paths, plan.Missing...)); err != nil {
		return run.sum, err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		once  stdsync.Once
		fatal error
		wg    stdsync.WaitGroup
	)
	stop := func(err error) { once.Do(func() { fatal = err; cancel() }) }
	for range max(workers, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := worker(ctx, root, repo, upload, run, func(rel string) (Entry, bool) { e, ok := entries[rel]; return e, ok }, nil); err != nil {
				stop(err)
			}
		}()
	}
	wg.Wait()
	// All workers are done, so nothing else touches run.
	return run.sum, fatal
}

// noteScanErrors records an error for every entry the scan could not read, and
// drops error rows that no scan result or job can clear any more: a file that
// was failing and has since been deleted. A readable file's row is cleared by
// its job.
func noteScanErrors(ctx context.Context, root string, repo *store.Repo, res Result, entries map[string]Entry) error {
	unreadable := map[string]bool{}
	for _, serr := range res.Errors {
		rel, ok := relOfError(root, serr)
		if !ok {
			continue
		}
		unreadable[rel] = true
		if err := repo.RecordError(ctx, rel, drive.Describe(serr), serr.Error()); err != nil {
			return err
		}
	}
	current, err := repo.ListErrors(ctx)
	if err != nil {
		return err
	}
	for _, e := range current {
		if _, ok := entries[e.RelPath]; !ok && !unreadable[e.RelPath] {
			if err := repo.ClearError(ctx, e.RelPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// runState is what the workers of one SyncWorkers share: the summary and the
// report callback, serialized by one lock.
type runState struct {
	mu     stdsync.Mutex
	sum    Summary
	report func(Event)
	move   MoveFunc  // nil: renames are uploaded as new files
	trash  TrashFunc // nil: deleted files are left in Drive
}

// add updates the summary and, when e is not nil, reports it, in that order
// and under one lock.
func (r *runState) add(count func(*Summary), e *Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	count(&r.sum)
	if e != nil {
		r.report(*e)
	}
}

// worker claims and processes jobs. lookup finds the file a job names (false:
// the file is gone or ignored, so the job is dropped). When the queue is empty
// the worker returns nil, or, if idle is set, calls it to wait for more work
// and goes on while it returns nil. It returns the error that must stop the run.
func worker(ctx context.Context, root string, repo *store.Repo, upload UploadFunc, run *runState, lookup func(rel string) (Entry, bool), idle func(context.Context) error) error {
	// Recording the outcome of a job is local and quick, so it ignores
	// cancellation: an interrupt must not leave a finished job queued.
	bg := context.WithoutCancel(ctx)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		job, err := repo.ClaimJob(ctx)
		if errors.Is(err, store.ErrNoJob) {
			if idle == nil {
				return nil
			}
			if err := idle(ctx); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		e, ok := lookup(job.RelPath)
		var out error // an error that must stop the worker
		panicked := safely("worker", func() {
			if !ok { // the file is gone or ignored now: trash it in Drive if it was deleted
				fileErr, fatal := trashGone(ctx, slog.With("path", job.RelPath, "job", job.ID), root, repo, run, job.RelPath)
				out = finishJob(bg, repo, job, fileErr, fatal)
				return
			}
			fileErr, fatal := process(ctx, slog.With("path", e.RelPath, "job", job.ID), root, repo, upload, run, e)
			out = finishJob(bg, repo, job, fileErr, fatal)
		}, "path", job.RelPath, "job", job.ID)
		if panicked {
			out = failPanicked(bg, repo, run, job)
		}
		if out != nil {
			return out
		}
	}
}

// finishJob records the outcome of a claimed job. The returned error must stop
// the worker (fatal, or the database failing).
func finishJob(bg context.Context, repo *store.Repo, job store.Job, fileErr, fatal error) error {
	switch {
	case fatal != nil:
		if err := repo.RequeueJob(bg, job.ID); err != nil {
			slog.Error("requeue failed", "job", job.ID, "err", err)
		}
		return fatal
	case fileErr != nil:
		return errors.Join(
			repo.FailJob(bg, job.ID, fileErr.Error()),
			repo.RecordError(bg, job.RelPath, drive.Describe(fileErr), fileErr.Error()),
		)
	}
	return errors.Join(repo.CompleteJob(bg, job.ID), repo.ClearError(bg, job.RelPath))
}

// trashGone moves the Drive file of rel to the Drive trash when rel is tracked
// and its local file is really gone (Lstat says not-exist; a file that is only
// ignored or not regular is left alone), then drops the row. The file_revisions
// rows stay. A Drive file that is already gone counts as done.
func trashGone(ctx context.Context, log *slog.Logger, root string, repo *store.Repo, run *runState, rel string) (fileErr, fatal error) {
	if run.trash == nil {
		return nil, nil
	}
	row, err := repo.Get(ctx, rel)
	if errors.Is(err, store.ErrNotFound) || (err == nil && row.DriveFileID == "") {
		return nil, nil // never reached Drive: nothing to trash
	}
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); !errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	switch err := run.trash(ctx, row.DriveFileID); {
	case err == nil, errors.Is(err, drive.ErrFileNotFound):
	case errors.Is(err, drive.ErrNotSignedIn), errors.Is(err, drive.ErrSessionExpired):
		log.Error("sync aborted, sign-in lost", "err", err)
		return nil, err
	case ctx.Err() != nil:
		return nil, ctx.Err()
	default:
		log.Warn("trash failed", "drive_file_id", row.DriveFileID, "err", err)
		run.add(func(s *Summary) { s.Failed++ }, &Event{RelPath: rel, Err: err})
		return err, nil
	}
	if err := repo.Delete(context.WithoutCancel(ctx), rel); err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("record %s: %w", rel, err)
	}
	log.Info("trashed, deleted locally", "drive_file_id", row.DriveFileID)
	run.add(func(s *Summary) { s.Trashed++ }, &Event{RelPath: rel, Trashed: true})
	return nil, nil
}

// failPanicked records a job whose processing panicked as an ordinary failed
// file, so the worker can take the next job.
func failPanicked(bg context.Context, repo *store.Repo, run *runState, job store.Job) error {
	const msg = "internal error (see log)"
	safely("report", func() {
		run.add(func(s *Summary) { s.Failed++ }, &Event{RelPath: job.RelPath, Err: errors.New("internal error")})
	})
	return errors.Join(
		repo.FailJob(bg, job.ID, msg),
		repo.RecordError(bg, job.RelPath, msg, msg),
	)
}

// process does the work of one job. A per-file problem comes back as fileErr
// (already reported and counted); fatal is an error that must stop the run.
func process(ctx context.Context, log *slog.Logger, root string, repo *store.Repo, upload UploadFunc, run *runState, e Entry) (fileErr, fatal error) {
	row, err := repo.Get(ctx, e.RelPath)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	path := filepath.Join(root, filepath.FromSlash(e.RelPath))
	// known is a row for a file already on Drive: changed content is
	// replaced in place, not uploaded as a new file.
	known := err == nil && row.DriveFileID != ""
	if known {
		// Same size and mtime: unchanged, no read. Otherwise hash the file:
		// content Drive already has only needs the row refreshed.
		if row.Size == e.Size && row.MTime == e.MTime {
			log.Debug("unchanged, size and mtime match", "drive_file_id", row.DriveFileID)
			// An old row, or a file replaced by a copy, has no or a stale inode.
			if e.Inode != 0 && row.Inode != e.Inode {
				if err := repo.SetInode(context.WithoutCancel(ctx), e.RelPath, e.Inode); err != nil {
					return nil, fmt.Errorf("record %s: %w", e.RelPath, err)
				}
			}
			run.add(func(s *Summary) { s.Synced++ }, nil)
			return nil, nil
		}
		md5sum, herr := fileMD5(path)
		if herr != nil {
			log.Warn("hash failed", "drive_file_id", row.DriveFileID, "err", herr)
			run.add(func(s *Summary) { s.Failed++ }, &Event{RelPath: e.RelPath, Err: herr})
			return herr, nil
		}
		row.LocalMD5 = md5sum
		if md5sum == row.SyncedMD5 {
			row.Size, row.MTime = e.Size, e.MTime
			if e.Inode != 0 {
				row.Inode = e.Inode
			}
			if err := repo.Upsert(context.WithoutCancel(ctx), row); err != nil {
				log.Error("record failed", "drive_file_id", row.DriveFileID, "err", err)
				return nil, fmt.Errorf("record %s: %w", e.RelPath, err)
			}
			log.Info("refreshed row, content unchanged", "drive_file_id", row.DriveFileID)
			run.add(func(s *Summary) { s.Synced++ }, nil)
			return nil, nil
		}
	}
	if !known && e.Inode != 0 && run.move != nil {
		if done, fileErr, fatal := tryMove(ctx, log, root, repo, run, e); done {
			return fileErr, fatal
		}
	}
	info, err := upload(ctx, path, e.RelPath)
	switch {
	case err == nil:
		// An interrupt must not lose the record of a file that is already
		// on Drive: the write is local and quick, so it ignores cancellation.
		rec := store.File{RelPath: e.RelPath}
		if known {
			rec = row
		}
		rec.DriveFileID, rec.Size, rec.MTime, rec.SyncedMD5 = info.ID, e.Size, e.MTime, info.MD5
		if e.Inode != 0 { // an unknown ID never erases a known one
			rec.Inode = e.Inode
		}
		// Base is the state after the last successful sync with Drive;
		// only an upload or update moves it.
		rec.BaseMD5, rec.BaseRevisionID = info.MD5, info.RevisionID
		if err := record(context.WithoutCancel(ctx), repo, rec, info, e.Size); err != nil {
			log.Error("record failed", "drive_file_id", info.ID, "err", err)
			return nil, fmt.Errorf("record %s: %w", e.RelPath, err)
		}
		if known {
			log.Info("updated", "drive_file_id", info.ID, "revision_id", info.RevisionID, "size", e.Size)
			run.add(func(s *Summary) { s.Updated++ }, &Event{RelPath: e.RelPath, Updated: true})
		} else {
			log.Info("uploaded", "drive_file_id", info.ID, "revision_id", info.RevisionID, "size", e.Size)
			run.add(func(s *Summary) { s.Uploaded++ }, &Event{RelPath: e.RelPath})
		}
		return nil, nil
	case errors.Is(err, drive.ErrNotSignedIn), errors.Is(err, drive.ErrSessionExpired):
		log.Error("sync aborted, sign-in lost", "err", err)
		return nil, err
	case ctx.Err() != nil:
		return nil, ctx.Err()
	default:
		log.Warn("upload failed", "drive_file_id", row.DriveFileID, "err", err)
		run.add(func(s *Summary) { s.Failed++ }, &Event{RelPath: e.RelPath, Err: err})
		return err, nil
	}
}

// tryMove recognises a renamed or moved file and moves it in Drive instead of
// uploading it again. e is a file with no usable row. It is a move of the
// tracked file at another path when exactly one tracked row has e's inode, that
// row's local file is gone, and e's content hashes to that row's synced MD5.
// The inode is only a hint (reused after a delete, shared by hard links); the
// hash and the vanished old path settle it. done is false when e is not a move
// (or the Drive file is gone), and the caller uploads it as new. Otherwise
// fileErr and fatal are the usual outcome of a job.
func tryMove(ctx context.Context, log *slog.Logger, root string, repo *store.Repo, run *runState, e Entry) (done bool, fileErr, fatal error) {
	cands, err := repo.FindByInode(ctx, e.Inode)
	if err != nil {
		return true, nil, err
	}
	var gone []store.File
	for _, c := range cands {
		if c.RelPath == e.RelPath || c.SyncedMD5 == "" {
			continue
		}
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(c.RelPath))); errors.Is(err, fs.ErrNotExist) {
			gone = append(gone, c)
		}
	}
	if len(gone) != 1 {
		return false, nil, nil
	}
	from := gone[0]
	sum, err := fileMD5(filepath.Join(root, filepath.FromSlash(e.RelPath)))
	if err != nil || sum != from.SyncedMD5 {
		return false, nil, nil // unreadable here (the upload will say so) or different content
	}

	info, err := run.move(ctx, from.DriveFileID, e.RelPath)
	switch {
	case err == nil:
	case errors.Is(err, drive.ErrFileNotFound):
		log.Info("renamed file's Drive file is gone, uploading as new", "from", from.RelPath, "drive_file_id", from.DriveFileID)
		if err := repo.Delete(context.WithoutCancel(ctx), from.RelPath); err != nil && !errors.Is(err, store.ErrNotFound) {
			return true, nil, err // the stale row would keep matching this inode
		}
		return false, nil, nil
	case errors.Is(err, drive.ErrNotSignedIn), errors.Is(err, drive.ErrSessionExpired):
		log.Error("sync aborted, sign-in lost", "err", err)
		return true, nil, err
	case ctx.Err() != nil:
		return true, nil, ctx.Err()
	default:
		log.Warn("move failed", "from", from.RelPath, "drive_file_id", from.DriveFileID, "err", err)
		run.add(func(s *Summary) { s.Failed++ }, &Event{RelPath: e.RelPath, Err: err})
		return true, err, nil
	}

	to := from
	to.Size, to.MTime, to.Inode, to.LocalMD5 = e.Size, e.MTime, e.Inode, sum
	bg := context.WithoutCancel(ctx)
	if err := repo.RenameFile(bg, from.RelPath, e.RelPath, to); err != nil {
		log.Error("record failed", "drive_file_id", from.DriveFileID, "err", err)
		return true, nil, fmt.Errorf("record %s: %w", e.RelPath, err)
	}
	if err := repo.ClearError(bg, from.RelPath); err != nil {
		return true, nil, err
	}
	log.Info("moved", "from", from.RelPath, "drive_file_id", info.ID)
	run.add(func(s *Summary) { s.Moved++ }, &Event{RelPath: e.RelPath, MovedFrom: from.RelPath})
	return true, nil, nil
}

// record writes the row and, when Drive reported a head revision, its
// file_revisions entry in one transaction. A missing revision ID (Drive can
// omit it) still records the row.
func record(ctx context.Context, repo *store.Repo, rec store.File, info drive.FileInfo, size int64) error {
	if info.RevisionID == "" {
		return repo.Upsert(ctx, rec)
	}
	return repo.RecordUpload(ctx, rec, store.Revision{
		DriveFileID: info.ID,
		RevisionID:  info.RevisionID,
		MD5:         info.MD5,
		Size:        size,
		Time:        time.Now().UnixNano(),
		Source:      "upload",
	})
}
