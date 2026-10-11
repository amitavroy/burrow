package drive

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"

	drv "google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

// Move renames and moves the Drive file driveFileID so that it sits where a
// file with rel_path newRelPath belongs: the folder that mirrors its directory
// under MySync (created if missing), named after its base name, with the
// rel_path tag updated. No content is sent and the Drive file ID, content and
// revisions are kept. It is safe to repeat: when the file is already in place
// the call changes nothing but the name and tag. A file Drive cannot find (or
// that is trashed) returns ErrFileNotFound. Sign-in errors are as for Upload.
func (u *Uploader) Move(ctx context.Context, driveFileID, newRelPath string) (FileInfo, error) {
	if err := validateRelPath(newRelPath); err != nil {
		return FileInfo{}, err
	}
	var out *drv.File
	err := withRetry(ctx, func() error {
		if err := u.connect(ctx); err != nil {
			return err
		}
		cur, err := u.svc.Files.Get(driveFileID).Fields("id,parents,trashed").Context(ctx).Do()
		var gerr *googleapi.Error
		if errors.As(err, &gerr) && gerr.Code == http.StatusNotFound {
			return fmt.Errorf("%w: %s", ErrFileNotFound, driveFileID)
		}
		if err != nil {
			return sessionError(fmt.Errorf("move %s: %w", newRelPath, err))
		}
		if cur.Trashed {
			return fmt.Errorf("%w: %s is in the trash", ErrFileNotFound, driveFileID)
		}
		dirID, err := u.ensureDir(ctx, path.Dir(newRelPath))
		if err != nil {
			return sessionError(err)
		}
		call := u.svc.Files.Update(driveFileID, &drv.File{
			Name:          path.Base(newRelPath),
			AppProperties: map[string]string{tagRelPath: newRelPath},
		}).Fields(uploadFields).Context(ctx)
		if !slices.Contains(cur.Parents, dirID) {
			call = call.AddParents(dirID)
		}
		if old := slices.DeleteFunc(slices.Clone(cur.Parents), func(p string) bool { return p == dirID }); len(old) > 0 {
			call = call.RemoveParents(strings.Join(old, ","))
		}
		out, err = call.Do()
		if err != nil {
			return sessionError(fmt.Errorf("move %s: %w", newRelPath, err))
		}
		return nil
	})
	if err != nil {
		return FileInfo{}, err
	}
	return fileInfo(out), nil
}
