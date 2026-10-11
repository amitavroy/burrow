package drive

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	drv "google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

// Trash moves the Drive file driveFileID to the Drive trash (trashed: true).
// It never deletes for good, and the file can be restored from the trash. It
// is safe to repeat. A file Drive cannot find returns ErrFileNotFound. Sign-in
// errors are as for Upload.
func (u *Uploader) Trash(ctx context.Context, driveFileID string) error {
	return withRetry(ctx, func() error {
		if err := u.connect(ctx); err != nil {
			return err
		}
		_, err := u.svc.Files.Update(driveFileID, &drv.File{Trashed: true}).Fields("id,trashed").Context(ctx).Do()
		var gerr *googleapi.Error
		if errors.As(err, &gerr) && gerr.Code == http.StatusNotFound {
			return fmt.Errorf("%w: %s", ErrFileNotFound, driveFileID)
		}
		if err != nil {
			return sessionError(fmt.Errorf("trash %s: %w", driveFileID, err))
		}
		return nil
	})
}
