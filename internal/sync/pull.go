package sync

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	stdsync "sync"
	"time"

	"github.com/amitavroy/burrow/internal/drive"
	"github.com/amitavroy/burrow/internal/store"
)

// expected holds the MD5 of each file a download is putting in place. The
// watcher reports the rename that lands it, and process drops a job whose file
// has that MD5, so a download is never uploaded back (echo suppression).
type expected struct {
	mu stdsync.Mutex
	m  map[string]string
}

func (e *expected) set(rel, md5 string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.m == nil {
		e.m = map[string]string{}
	}
	e.m[rel] = md5
}

func (e *expected) clear(rel string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.m, rel)
}

func (e *expected) get(rel string) (string, bool) {
	if e == nil {
		return "", false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	md5, ok := e.m[rel]
	return md5, ok
}

// errDeferred means a change cannot be applied yet and should be read again.
var errDeferred = errors.New("deferred")

// Pull applies Drive changes to the root. For each edited file it compares
// local, Drive and base with Decide:
//
//   - only Drive changed: download it;
//   - both changed to the same content: only the base moves;
//   - only local changed: nothing here, the watcher or startup scan uploads it;
//   - both changed differently, or both created the path: keep both. The local
//     file is renamed to a conflicted copy (see keepLocalCopy), Drive's version
//     is downloaded to the original path, and the copy is queued as a new file.
//
// Edit vs delete: the edit wins. A file deleted locally but edited on Drive is
// downloaded again; a file trashed or removed on Drive but edited locally is
// forgotten by the database and uploaded as a new file. A delete with no edit
// is not applied to the other side from here (local deletes already trash in
// Drive; Drive deletes are not applied locally).
//
// A change whose revision is the one the database already holds is syncd's own
// upload coming back and is skipped. A file renamed locally whose move has not
// been pushed yet is deferred: deferred is true and the caller should read the
// same changes again later. One failing file does not stop the rest; only the
// state database failing does.
func Pull(ctx context.Context, root string, repo *store.Repo, changes []drive.Change, download DownloadFunc, exp *expected, opts RestoreOptions) (deferred bool, err error) {
	if opts.Host == "" {
		opts.Host = "this-machine"
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	for _, c := range changes {
		log := slog.With("path", c.RelPath, "drive_file_id", c.FileID)
		gone := c.Removed || c.Trashed
		if !gone && (!c.Tagged || !safeRel(c.RelPath) || c.MD5 == "") {
			log.Debug("remote change not applied", "tagged", c.Tagged)
			continue
		}
		var derr dbError
		err := pullOne(ctx, root, repo, c, download, exp, opts, log)
		switch {
		case errors.As(err, &derr):
			return deferred, derr.error
		case errors.Is(err, errDeferred):
			deferred = true
		case err != nil:
			if ctx.Err() != nil {
				return deferred, ctx.Err()
			}
			log.Warn("pull failed", "err", err)
		}
	}
	return deferred, nil
}

func pullOne(ctx context.Context, root string, repo *store.Repo, c drive.Change, download DownloadFunc, exp *expected, opts RestoreOptions, log *slog.Logger) error {
	row, err := repo.GetByDriveID(ctx, c.FileID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return dbError{err}
	}
	tracked := err == nil
	if !tracked && (c.Removed || c.Trashed) {
		return nil // already forgotten, e.g. our own trash coming back
	}
	if tracked && !c.Removed && !c.Trashed && c.RevisionID != "" && c.RevisionID == row.BaseRevisionID {
		return nil // our own upload or earlier download
	}
	rel := c.RelPath
	if tracked {
		rel = row.RelPath // a local rename not yet pushed wins over the old tag
	}
	dst := filepath.Join(root, filepath.FromSlash(rel))
	local, exists := "", false
	switch st, err := os.Lstat(dst); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	case !st.Mode().IsRegular():
		return errors.New("a folder or special file is in the way")
	default:
		exists = true
		if local, err = fileMD5(dst); err != nil {
			return err
		}
	}

	if c.Removed || c.Trashed {
		if exists && local != row.BaseMD5 { // edit vs delete: the edit wins
			if err := repo.Delete(ctx, rel); err != nil {
				return dbError{err}
			}
			if err := repo.EnqueueJobs(ctx, []string{rel}); err != nil {
				return dbError{err}
			}
			log.Info("deleted on Drive but edited here, uploading again", "rel", rel)
		}
		return nil
	}

	act := Decide(local, c.MD5, row.BaseMD5)
	if tracked && !exists { // deleted here
		if c.MD5 == row.BaseMD5 {
			return nil // Drive did not change the content; the delete job handles it
		}
		if moved(ctx, root, repo, rel) {
			return errDeferred // renamed here, not pushed yet: edit vs delete is not the case
		}
		act = ActionDownload // edit vs delete: the edit wins
	}

	var copyRel string
	switch act {
	case ActionConflict:
		if copyRel, err = keepLocalCopy(root, rel, opts); err != nil {
			return err
		}
		log.Info("edited on both sides, kept both", "rel", rel, "copy", copyRel)
		fallthrough
	case ActionDownload:
		exp.set(rel, c.MD5)
		defer exp.clear(rel)
		// ponytail: no re-check that the local file is still at base just before the rename; a save in that window is lost to the download (it stays in Drive history only).
		if err := download(ctx, c.FileID, c.MD5, dst); err != nil {
			if copyRel != "" { // put the user's file back
				if rerr := os.Rename(filepath.Join(root, filepath.FromSlash(copyRel)), dst); rerr != nil {
					log.Error("could not put the local file back", "kept_as", copyRel, "err", rerr)
				}
			}
			return err
		}
	case ActionUpdateBase:
	default:
		return nil
	}

	st, err := os.Lstat(dst)
	if err != nil {
		return err
	}
	rec := store.File{
		RelPath: rel, DriveFileID: c.FileID, Size: st.Size(), MTime: st.ModTime().UnixNano(), Inode: fileID(dst, st),
		LocalMD5: c.MD5, SyncedMD5: c.MD5, BaseMD5: c.MD5, BaseRevisionID: c.RevisionID,
	}
	bg := context.WithoutCancel(ctx) // the file is in place; its record must be too
	if c.RevisionID == "" {
		err = repo.Upsert(bg, rec)
	} else {
		err = repo.RecordUpload(bg, rec, store.Revision{
			DriveFileID: c.FileID, RevisionID: c.RevisionID, MD5: c.MD5, Size: st.Size(), Time: time.Now().UnixNano(), Source: "download",
		})
	}
	if err == nil && copyRel != "" { // the user's version syncs up as a new file
		err = repo.EnqueueJobs(bg, []string{copyRel})
	}
	if err != nil {
		return dbError{err}
	}
	log.Info("pulled", "revision_id", c.RevisionID)
	return nil
}

// moved reports whether the file at rel is gone only because it was renamed or
// moved and the move has not been pushed to Drive yet.
// ponytail: a full walk, only reached for a file deleted here and edited on Drive.
func moved(ctx context.Context, root string, repo *store.Repo, rel string) bool {
	plan, _, err := diff(ctx, root, repo)
	if err != nil {
		return true // cannot tell: try again later rather than restore a duplicate
	}
	for _, m := range plan.Moved {
		if m.From == rel {
			return true
		}
	}
	return false
}

// pollChanges reads the Drive changes feed once and applies it with Pull. The
// stored token moves only after Pull returns, so a crash re-reads the same
// changes (Pull is idempotent). With no token it only takes a baseline.
func pollChanges(ctx context.Context, root string, repo *store.Repo, src *ChangeSource, exp *expected) error {
	baseline := func() error {
		tok, err := src.Start(ctx)
		if err != nil {
			return err
		}
		return repo.SetMeta(ctx, store.MetaChangesToken, tok)
	}
	token, _, err := repo.GetMeta(ctx, store.MetaChangesToken)
	if err != nil {
		return err
	}
	if token == "" {
		return baseline()
	}
	changes, next, err := src.List(ctx, token)
	if errors.Is(err, drive.ErrPageTokenExpired) {
		slog.Warn("changes page token expired; Drive edits before now are not applied")
		return baseline()
	}
	if err != nil {
		return err
	}
	deferred, err := Pull(ctx, root, repo, changes, src.Download, exp, RestoreOptions{Host: src.Host})
	if err != nil || deferred {
		return err // the token stays, so the same changes are read again
	}
	return repo.SetMeta(ctx, store.MetaChangesToken, next)
}

// ChangeSource is the Drive side of the poller in Run.
type ChangeSource struct {
	Start    func(ctx context.Context) (string, error)
	List     func(ctx context.Context, token string) ([]drive.Change, string, error)
	Download DownloadFunc
	Host     string        // names conflicted copies; default "this-machine"
	Every    time.Duration // default 30s
}
