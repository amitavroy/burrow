package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
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

	for job, e := range res.Entries {
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		// job numbers the file within this run, until the queue (ticket 17)
		// provides real job IDs. Only paths and IDs are logged, never content.
		log := slog.With("path", e.RelPath, "job", job+1)
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
				log.Debug("unchanged, size and mtime match", "drive_file_id", row.DriveFileID)
				sum.Synced++
				continue
			}
			md5sum, herr := fileMD5(path)
			if herr != nil {
				log.Warn("hash failed", "drive_file_id", row.DriveFileID, "err", herr)
				sum.Failed++
				report(Event{RelPath: e.RelPath, Err: herr})
				continue
			}
			row.LocalMD5 = md5sum
			if md5sum == row.SyncedMD5 {
				row.Size, row.MTime = e.Size, e.MTime
				if err := repo.Upsert(context.WithoutCancel(ctx), row); err != nil {
					log.Error("record failed", "drive_file_id", row.DriveFileID, "err", err)
					return sum, fmt.Errorf("record %s: %w", e.RelPath, err)
				}
				log.Info("refreshed row, content unchanged", "drive_file_id", row.DriveFileID)
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
			// Base is the state after the last successful sync with Drive;
			// only an upload or update moves it.
			rec.BaseMD5, rec.BaseRevisionID = info.MD5, info.RevisionID
			if err := record(context.WithoutCancel(ctx), repo, rec, info, e.Size); err != nil {
				log.Error("record failed", "drive_file_id", info.ID, "err", err)
				return sum, fmt.Errorf("record %s: %w", e.RelPath, err)
			}
			if known {
				log.Info("updated", "drive_file_id", info.ID, "revision_id", info.RevisionID, "size", e.Size)
				sum.Updated++
			} else {
				log.Info("uploaded", "drive_file_id", info.ID, "revision_id", info.RevisionID, "size", e.Size)
				sum.Uploaded++
			}
			report(Event{RelPath: e.RelPath, Updated: known})
		case errors.Is(err, drive.ErrNotSignedIn), errors.Is(err, drive.ErrSessionExpired):
			log.Error("sync aborted, sign-in lost", "err", err)
			return sum, err
		case ctx.Err() != nil:
			return sum, ctx.Err()
		default:
			log.Warn("upload failed", "drive_file_id", row.DriveFileID, "err", err)
			sum.Failed++
			report(Event{RelPath: e.RelPath, Err: err})
		}
	}
	return sum, nil
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
