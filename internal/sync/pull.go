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

// Pull applies Drive changes to the root. For each edited file it compares
// local, Drive and base with Decide: a Drive-only change is downloaded, and a
// change both sides made identically only moves the base. An upload needs
// nothing here (the watcher or the startup scan sends it). Conflicts, trashed
// and removed files are left alone for ticket 41. A change whose revision is
// the one the database already holds is syncd's own upload coming back and is
// skipped. One failing file does not stop the rest; only the state database
// failing does.
func Pull(ctx context.Context, root string, repo *store.Repo, changes []drive.Change, download DownloadFunc, exp *expected) error {
	for _, c := range changes {
		log := slog.With("path", c.RelPath, "drive_file_id", c.FileID)
		if c.Removed || c.Trashed || !c.Tagged || !safeRel(c.RelPath) || c.MD5 == "" {
			log.Debug("remote change not applied", "removed", c.Removed, "trashed", c.Trashed, "tagged", c.Tagged)
			continue
		}
		var derr dbError
		err := pullOne(ctx, root, repo, c, download, exp, log)
		if errors.As(err, &derr) {
			return derr.error
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Warn("pull failed", "err", err)
		}
	}
	return nil
}

func pullOne(ctx context.Context, root string, repo *store.Repo, c drive.Change, download DownloadFunc, exp *expected, log *slog.Logger) error {
	row, err := repo.GetByDriveID(ctx, c.FileID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return dbError{err}
	}
	tracked := err == nil
	if tracked && c.RevisionID != "" && c.RevisionID == row.BaseRevisionID {
		return nil // our own upload or earlier download
	}
	rel := c.RelPath
	if tracked {
		rel = row.RelPath // a local rename not yet pushed wins over the old tag
	}
	dst := filepath.Join(root, filepath.FromSlash(rel))
	local := ""
	switch st, err := os.Lstat(dst); {
	case errors.Is(err, fs.ErrNotExist):
		if tracked {
			return nil // deleted locally: edit vs delete is ticket 41
		}
	case err != nil:
		return err
	case !st.Mode().IsRegular():
		return errors.New("a folder or special file is in the way")
	default:
		if local, err = fileMD5(dst); err != nil {
			return err
		}
	}

	switch Decide(local, c.MD5, row.BaseMD5) {
	case ActionDownload:
		exp.set(rel, c.MD5)
		defer exp.clear(rel)
		// ponytail: no re-check that the local file is still at base just before the rename; a save in that window is lost to the download (it stays in Drive history only).
		if err := download(ctx, c.FileID, c.MD5, dst); err != nil {
			return err
		}
	case ActionUpdateBase:
	case ActionConflict:
		log.Warn("edited on both sides, left for conflict handling", "rel", rel)
		return nil
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
	if err != nil {
		return dbError{err}
	}
	log.Info("pulled", "revision_id", c.RevisionID)
	return nil
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
	if err := Pull(ctx, root, repo, changes, src.Download, exp); err != nil {
		return err
	}
	return repo.SetMeta(ctx, store.MetaChangesToken, next)
}

// ChangeSource is the Drive side of the poller in Run.
type ChangeSource struct {
	Start    func(ctx context.Context) (string, error)
	List     func(ctx context.Context, token string) ([]drive.Change, string, error)
	Download DownloadFunc
	Every    time.Duration // default 30s
}
