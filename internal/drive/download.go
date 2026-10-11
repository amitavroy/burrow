package drive

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"google.golang.org/api/googleapi"
)

// ErrChecksumMismatch means the downloaded bytes do not hash to the MD5 the
// caller expected, usually because the file changed in Drive after it was listed.
var ErrChecksumMismatch = errors.New("downloaded content does not match the expected MD5")

// Download saves the Drive file driveFileID at localPath, streaming it (never
// held whole in memory) into a temporary file next to the target, checking its
// MD5 against wantMD5 while it is written, and only then renaming it into
// place, so localPath is either its old content or the complete new content,
// never a partial file. A mismatch leaves localPath untouched and the temporary
// file removed. Missing parent folders are created. An existing localPath is
// replaced: callers that must not overwrite check first.
// The temporary name ends in .tmp, which the sync ignores by default, so the
// watcher never uploads a half-written download.
// A file Drive cannot find (or that is trashed) returns ErrFileNotFound. Sign-in
// errors are as for Upload. Transient errors are retried from the start.
func (u *Uploader) Download(ctx context.Context, driveFileID, wantMD5, localPath string) error {
	if wantMD5 == "" {
		return fmt.Errorf("download %s: Drive reports no MD5 for it, so it cannot be verified", driveFileID)
	}
	dir := filepath.Dir(localPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("download %s: %w", localPath, err)
	}
	return withRetry(ctx, func() error {
		if err := u.connect(ctx); err != nil {
			return err
		}
		resp, err := u.svc.Files.Get(driveFileID).Context(ctx).Download()
		var gerr *googleapi.Error
		if errors.As(err, &gerr) && gerr.Code == http.StatusNotFound {
			return fmt.Errorf("%w: %s", ErrFileNotFound, driveFileID)
		}
		if err != nil {
			return sessionError(fmt.Errorf("download %s: %w", localPath, err))
		}
		defer resp.Body.Close()

		tmp, err := os.CreateTemp(dir, ".burrow-*.tmp")
		if err != nil {
			return fmt.Errorf("download %s: %w", localPath, err)
		}
		done := false
		defer func() {
			if !done {
				tmp.Close()
				os.Remove(tmp.Name())
			}
		}()
		h := md5.New()
		if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
			return fmt.Errorf("download %s: %w", localPath, err)
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != wantMD5 {
			return fmt.Errorf("%w: %s has %s, expected %s", ErrChecksumMismatch, driveFileID, got, wantMD5)
		}
		// CreateTemp makes the file private; a downloaded file gets normal permissions.
		if err := errors.Join(tmp.Chmod(0o644), tmp.Sync(), tmp.Close()); err != nil {
			return fmt.Errorf("download %s: %w", localPath, err)
		}
		if err := os.Rename(tmp.Name(), localPath); err != nil {
			return fmt.Errorf("download %s: %w", localPath, err)
		}
		done = true
		return nil
	})
}
