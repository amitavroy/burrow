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
		out, err = reparent(call, cur.Parents, dirID).Do()
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

// reparent makes dirID the only parent. When it already is, the parent
// arguments are left out, so repeating a move changes nothing.
func reparent(call *drv.FilesUpdateCall, parents []string, dirID string) *drv.FilesUpdateCall {
	if !slices.Contains(parents, dirID) {
		call = call.AddParents(dirID)
	}
	if old := slices.DeleteFunc(slices.Clone(parents), func(p string) bool { return p == dirID }); len(old) > 0 {
		call = call.RemoveParents(strings.Join(old, ","))
	}
	return call
}

// MoveFolder renames and moves the Drive folder that mirrors oldDir so that it
// mirrors newDir, children and all, and rewrites the folder cache to match. The
// folder is the one cached for oldDir; with no cache entry, or a folder Drive
// cannot find (or that is trashed), it returns ErrFileNotFound and changes
// nothing. The files inside keep their Drive IDs; their rel_path tags are
// updated as each one is moved. Sign-in errors are as for Upload.
func (u *Uploader) MoveFolder(ctx context.Context, oldDir, newDir string) error {
	if err := errors.Join(validateRelPath(oldDir), validateRelPath(newDir)); err != nil {
		return err
	}
	return withRetry(ctx, func() error {
		if err := u.connect(ctx); err != nil {
			return err
		}
		if u.folders == nil {
			return fmt.Errorf("%w: no folder cache", ErrFileNotFound)
		}
		id, err := u.folders.GetFolder(ctx, oldDir)
		if err != nil {
			return fmt.Errorf("%w: folder %s", ErrFileNotFound, oldDir)
		}
		cur, err := u.svc.Files.Get(id).Fields("id,parents,trashed").Context(ctx).Do()
		var gerr *googleapi.Error
		if errors.As(err, &gerr) && gerr.Code == http.StatusNotFound || err == nil && cur.Trashed {
			return fmt.Errorf("%w: folder %s", ErrFileNotFound, oldDir)
		}
		if err != nil {
			return sessionError(fmt.Errorf("move folder %s: %w", newDir, err))
		}
		parentID, err := u.ensureDir(ctx, path.Dir(newDir))
		if err != nil {
			return sessionError(err)
		}
		call := u.svc.Files.Update(id, &drv.File{Name: path.Base(newDir)}).Fields("id").Context(ctx)
		if _, err := reparent(call, cur.Parents, parentID).Do(); err != nil {
			return sessionError(fmt.Errorf("move folder %s: %w", newDir, err))
		}
		_ = u.folders.RenameFolders(ctx, oldDir, newDir) // best effort, like the rest of the cache
		return nil
	})
}
