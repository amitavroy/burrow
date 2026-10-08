package sync

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/amitavroy/burrow/internal/drive"
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
	Failed   int
	// Ignored is the number of files and directories the ignore rules skipped.
	Ignored int
	// Unreadable are entries the scan could not read; their files were not
	// uploaded.
	Unreadable []error
}

// Sync scans root and uploads every file, one at a time in rel_path order,
// reporting each outcome through report. A file that fails to upload is
// reported and counted, and the run goes on. Sync stops at once, returning the
// error, when the sign-in is gone (drive.ErrNotSignedIn or
// drive.ErrSessionExpired), when ctx is cancelled, or when the root itself
// cannot be scanned. The Summary is valid in every case.
func Sync(ctx context.Context, root string, upload UploadFunc, report func(Event)) (Summary, error) {
	res, err := Scan(root)
	if err != nil {
		return Summary{}, err
	}
	sum := Summary{Ignored: res.Ignored, Unreadable: res.Errors}

	for _, e := range res.Entries {
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		_, err := upload(ctx, filepath.Join(root, filepath.FromSlash(e.RelPath)), e.RelPath)
		switch {
		case err == nil:
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
