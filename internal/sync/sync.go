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

// Event reports the outcome for one file: Err is nil when it was uploaded or,
// with Updated set, replaced in place on Drive.
type Event struct {
	RelPath string
	Updated bool
	Err     error
}

// Summary counts what one Sync did.
type Summary struct {
	// Uploaded counts new files; Updated counts changed files replaced in place.
	Uploaded int
	Updated  int
	// Synced counts files skipped because Drive already has their content.
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
// already has a row with a Drive ID is skipped when its size and mtime match
// the row, or when its streamed MD5 equals Drive's (the row is then refreshed);
// otherwise its content is replaced in place on Drive. After each successful
// upload or update its row is written straight away (Drive ID, Drive's MD5, and the size and mtime seen by the
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
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return sum, err
		}
		path := filepath.Join(root, filepath.FromSlash(e.RelPath))
		// known is a row for a file already on Drive: changed content is
		// replaced in place, not uploaded as a new file.
		known := err == nil && row.DriveFileID != ""
		if known {
			// Same size and mtime: unchanged, no read. Otherwise hash the file:
			// content Drive already has only needs the row refreshed.
			if row.Size == e.Size && row.MTime == e.MTime {
				sum.Synced++
				continue
			}
			md5sum, herr := fileMD5(path)
			if herr != nil {
				sum.Failed++
				report(Event{RelPath: e.RelPath, Err: herr})
				continue
			}
			row.LocalMD5 = md5sum
			if md5sum == row.SyncedMD5 {
				row.Size, row.MTime = e.Size, e.MTime
				if err := repo.Upsert(context.WithoutCancel(ctx), row); err != nil {
					return sum, fmt.Errorf("record %s: %w", e.RelPath, err)
				}
				sum.Synced++
				continue
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
			if err := repo.Upsert(context.WithoutCancel(ctx), rec); err != nil {
				return sum, fmt.Errorf("record %s: %w", e.RelPath, err)
			}
			if known {
				sum.Updated++
			} else {
				sum.Uploaded++
			}
			report(Event{RelPath: e.RelPath, Updated: known})
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
