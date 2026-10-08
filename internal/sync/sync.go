package sync

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/amitavroy/burrow/internal/drive"
	"github.com/amitavroy/burrow/internal/store"
)

// UploadFunc sends one local file to Drive and returns what Drive reports. A
// *drive.Uploader's Upload method is the real one; tests pass a fake.
type UploadFunc func(ctx context.Context, localPath, relPath string) (drive.FileInfo, error)

// Event reports the outcome for one file: Err is nil when it was uploaded.
type Event struct {
	RelPath string
	Err     error
}

// Summary counts what one Sync did.
type Summary struct {
	Uploaded int
	// Synced counts files skipped because the state database already has
	// them on Drive.
	Synced int
	Failed int
	// Ignored is the number of files and directories the ignore rules skipped.
	Ignored int
	// Unreadable are entries the scan could not read; their files were not
	// uploaded.
	Unreadable []error
}

// Sync scans root and uploads every file that has no row in repo yet, one at a
// time in rel_path order, reporting each outcome through report. A file that
// already has a row with a Drive ID is skipped without being compared: change
// detection comes later. After each successful upload its row is written
// straight away (Drive ID, Drive's MD5, and the size and mtime seen by the
// scan, which are never newer than what was uploaded), so an interrupted run
// resumes where it stopped. A file that fails to upload is reported and
// counted, and the run goes on. Sync stops at once, returning the error, when
// the sign-in is gone (drive.ErrNotSignedIn or drive.ErrSessionExpired), when
// ctx is cancelled, when the root cannot be scanned, or when repo fails. The
// Summary is valid in every case.
func Sync(ctx context.Context, root string, repo *store.Repo, upload UploadFunc, report func(Event)) (Summary, error) {
	res, err := Scan(root)
	if err != nil {
		return Summary{}, err
	}
	sum := Summary{Ignored: res.Ignored, Unreadable: res.Errors}

	for _, e := range res.Entries {
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		row, err := repo.Get(ctx, e.RelPath)
		if err == nil && row.DriveFileID != "" {
			sum.Synced++
			continue
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return sum, err
		}
		info, err := upload(ctx, filepath.Join(root, filepath.FromSlash(e.RelPath)), e.RelPath)
		switch {
		case err == nil:
			// An interrupt must not lose the record of a file that is already
			// on Drive: the write is local and quick, so it ignores cancellation.
			err := repo.Upsert(context.WithoutCancel(ctx), store.File{
				RelPath:     e.RelPath,
				DriveFileID: info.ID,
				Size:        e.Size,
				MTime:       e.MTime,
				SyncedMD5:   info.MD5,
			})
			if err != nil {
				return sum, fmt.Errorf("record %s: %w", e.RelPath, err)
			}
			sum.Uploaded++
			report(Event{RelPath: e.RelPath})
		case errors.Is(err, drive.ErrNotSignedIn), errors.Is(err, drive.ErrSessionExpired):
			return sum, err
		case ctx.Err() != nil:
			return sum, ctx.Err()
		default:
			sum.Failed++
			report(Event{RelPath: e.RelPath, Err: err})
		}
	}
	return sum, nil
}
