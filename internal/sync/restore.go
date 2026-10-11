package sync

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/amitavroy/burrow/internal/drive"
	"github.com/amitavroy/burrow/internal/store"
)

// DownloadFunc saves the Drive file driveFileID at localPath, verified against
// wantMD5. A *drive.Uploader's Download method is the real one.
type DownloadFunc func(ctx context.Context, driveFileID, wantMD5, localPath string) error

// Restore outcomes.
const (
	RestoreMatched    = "matched"    // already there with the same content: row only, no transfer
	RestoreDownloaded = "downloaded" // missing locally: downloaded
	RestoreConflict   = "conflict"   // there with other content: kept as a conflicted copy, Drive's version downloaded
	RestoreFailed     = "failed"
)

// RestoreEvent reports what happened to one remote file.
type RestoreEvent struct {
	RelPath string
	Action  string
	// ConflictCopy is the rel_path the local file was kept under (RestoreConflict).
	ConflictCopy string
	Err          error
}

// RestoreSummary counts the outcomes of one Restore.
type RestoreSummary struct{ Matched, Downloaded, Conflicts, Failed int }

// RestoreOptions tune Restore. The zero value works.
type RestoreOptions struct {
	Host string    // names conflicted copies; default "this-machine"
	Now  time.Time // dates conflicted copies; default time.Now()
}

// Restore makes the sync root hold what remote lists and records every file in
// the database, so a later `run` finds nothing to do. For each file, at
// root/rel_path:
//
//   - missing: downloaded (verified, atomic, see Uploader.Download);
//   - present with the same MD5: only the row is written, nothing is transferred;
//   - present with other content: the local file is renamed to
//     "name (conflicted copy, host, date).ext" and Drive's version is downloaded
//     to the original path, so both survive and the original path matches Drive.
//     The copy is then an ordinary new file for the next sync.
//
// A problem with one file (an unsafe path, a failed download, a directory in
// the way) is reported and counted and the rest go on. Remote files with the
// same rel_path: the first wins and the others are reported as failed. Restore
// stops with an error only when ctx is cancelled, the sign-in is gone or the
// database fails. The rows get Source "download" in file_revisions.
// ponytail: sequential; bounded concurrency like the upload workers if a big restore is too slow.
func Restore(ctx context.Context, root string, repo *store.Repo, remote []drive.RemoteFile, download DownloadFunc, report func(RestoreEvent), opts RestoreOptions) (RestoreSummary, error) {
	if opts.Host == "" {
		opts.Host = "this-machine"
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	var sum RestoreSummary
	seen := map[string]bool{}
	fail := func(rel string, err error) {
		sum.Failed++
		report(RestoreEvent{RelPath: rel, Action: RestoreFailed, Err: err})
	}
	for _, rf := range remote {
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		rel := rf.RelPath
		switch {
		case !safeRel(rel):
			fail(rel, errors.New("unsafe path in Drive, skipped"))
			continue
		case seen[rel]:
			fail(rel, fmt.Errorf("more than one Drive file has this path, kept the first (%s skipped)", rf.ID))
			continue
		case rf.MD5 == "":
			fail(rel, errors.New("Drive reports no MD5, cannot verify"))
			continue
		}
		seen[rel] = true
		ev, err := restoreOne(ctx, root, repo, rf, download, opts)
		switch {
		case err == nil:
		case errors.Is(err, drive.ErrNotSignedIn), errors.Is(err, drive.ErrSessionExpired), ctx.Err() != nil, errors.As(err, new(dbError)):
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			return sum, err
		default:
			fail(rel, err)
			continue
		}
		switch ev.Action {
		case RestoreMatched:
			sum.Matched++
		case RestoreDownloaded:
			sum.Downloaded++
		case RestoreConflict:
			sum.Conflicts++
		}
		report(ev)
	}
	return sum, nil
}

// dbError marks a failure of the state database, which must stop Restore.
type dbError struct{ error }

func (e dbError) Unwrap() error { return e.error }

func restoreOne(ctx context.Context, root string, repo *store.Repo, rf drive.RemoteFile, download DownloadFunc, opts RestoreOptions) (RestoreEvent, error) {
	rel := rf.RelPath
	dst := filepath.Join(root, filepath.FromSlash(rel))
	ev := RestoreEvent{RelPath: rel}

	st, err := os.Lstat(dst)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		ev.Action = RestoreDownloaded
	case err != nil:
		return ev, err
	case !st.Mode().IsRegular():
		return ev, errors.New("a folder or special file is in the way")
	default:
		sum, err := fileMD5(dst)
		if err != nil {
			return ev, err
		}
		if sum == rf.MD5 {
			ev.Action = RestoreMatched
		} else {
			ev.Action = RestoreConflict
			ev.ConflictCopy, err = keepLocalCopy(root, rel, opts)
			if err != nil {
				return ev, err
			}
		}
	}

	if ev.Action != RestoreMatched {
		if err := download(ctx, rf.ID, rf.MD5, dst); err != nil {
			if ev.ConflictCopy != "" { // put the user's file back
				if rerr := os.Rename(filepath.Join(root, filepath.FromSlash(ev.ConflictCopy)), dst); rerr != nil {
					slog.Error("could not put the local file back", "path", rel, "kept_as", ev.ConflictCopy, "err", rerr)
				}
			}
			return ev, err
		}
	}

	st, err = os.Lstat(dst)
	if err != nil {
		return ev, err
	}
	rec := store.File{
		RelPath: rel, DriveFileID: rf.ID, Size: st.Size(), MTime: st.ModTime().UnixNano(), Inode: fileID(dst, st),
		LocalMD5: rf.MD5, SyncedMD5: rf.MD5, BaseMD5: rf.MD5, BaseRevisionID: rf.RevisionID,
	}
	bg := context.WithoutCancel(ctx) // the file is in place; its record must be too
	if rf.RevisionID == "" {
		err = repo.Upsert(bg, rec)
	} else {
		err = repo.RecordUpload(bg, rec, store.Revision{
			DriveFileID: rf.ID, RevisionID: rf.RevisionID, MD5: rf.MD5, Size: st.Size(), Time: time.Now().UnixNano(), Source: "download",
		})
	}
	if err != nil {
		return ev, dbError{err}
	}
	return ev, nil
}

// keepLocalCopy renames the local file at rel to an unused conflicted-copy name
// in the same folder and returns that name's rel_path.
func keepLocalCopy(root, rel string, opts RestoreOptions) (string, error) {
	dir, name := path.Split(rel)
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	date := opts.Now.Format("2006-01-02")
	for n := 1; ; n++ {
		label := fmt.Sprintf("%s (conflicted copy, %s, %s)%s", stem, opts.Host, date, ext)
		if n > 1 {
			label = fmt.Sprintf("%s (conflicted copy, %s, %s %d)%s", stem, opts.Host, date, n, ext)
		}
		copyRel := dir + label
		copyAbs := filepath.Join(root, filepath.FromSlash(copyRel))
		if _, err := os.Lstat(copyAbs); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if err := os.Rename(filepath.Join(root, filepath.FromSlash(rel)), copyAbs); err != nil {
			return "", err
		}
		return copyRel, nil
	}
}

// safeRel reports whether rel is a plain relative slash path that stays inside
// the root: Drive tags are data from outside, and a "../" in one must never
// write outside the sync root.
func safeRel(rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.ContainsAny(rel, `\`) {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
