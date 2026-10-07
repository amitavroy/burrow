package drive

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	drv "google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

const (
	// uploadChunkSize is the resumable-upload chunk size. The file is streamed
	// from disk in chunks this big, never read whole.
	uploadChunkSize = 8 << 20

	// uploadFields is what Drive returns for an uploaded or looked-up file.
	uploadFields = "id,name,md5Checksum,size,headRevisionId,appProperties,webViewLink"

	// Keys of the appProperties written on every upload. Files are tracked by
	// Drive ID; these tags let the local state be rebuilt from Drive alone.
	tagWatchID = "watch_id"
	tagRelPath = "rel_path"
)

// ErrFileNotFound means Drive has no such file that this app can see. With the
// drive.file scope a file created by something else looks the same as a
// missing one.
var ErrFileNotFound = errors.New("file not found")

// FileInfo is what Drive reports about an uploaded file.
type FileInfo struct {
	ID         string
	Name       string
	Size       int64
	MD5        string
	RevisionID string
	WatchID    string
	RelPath    string
	WebLink    string
}

// Upload sends the local file at path into the MySync folder, tagged with
// watchID and relPath. If a live file with the same tags is already there its
// content is replaced (same Drive ID), otherwise a new file is created. It
// returns ErrNotSignedIn or ErrSessionExpired like EnsureRoot. extra options
// are for tests.
func Upload(ctx context.Context, client Client, tokens KeyringStore, roots FileStore, path, watchID, relPath string, extra ...option.ClientOption) (FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return FileInfo{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil {
		return FileInfo{}, fmt.Errorf("stat %s: %w", path, err)
	} else if st.IsDir() {
		return FileInfo{}, fmt.Errorf("%s is a directory", path)
	}

	svc, err := serviceFromStore(ctx, client, tokens, extra...)
	if err != nil {
		return FileInfo{}, err
	}
	rootID, err := ensureRootWith(ctx, svc, roots)
	if err != nil {
		return FileInfo{}, sessionError(err)
	}

	existing, err := findByTags(ctx, svc, rootID, watchID, relPath)
	if err != nil {
		return FileInfo{}, sessionError(err)
	}

	tags := map[string]string{tagWatchID: watchID, tagRelPath: relPath}
	media := googleapi.ChunkSize(uploadChunkSize)
	var out *drv.File
	if existing != nil {
		out, err = svc.Files.Update(existing.Id, &drv.File{AppProperties: tags}).
			Media(f, media).Fields(uploadFields).Context(ctx).Do()
	} else {
		name := relPath
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		out, err = svc.Files.Create(&drv.File{
			Name:          name,
			Parents:       []string{rootID},
			AppProperties: tags,
		}).Media(f, media).Fields(uploadFields).Context(ctx).Do()
	}
	if err != nil {
		return FileInfo{}, sessionError(fmt.Errorf("upload %s: %w", relPath, err))
	}
	return fileInfo(out), nil
}

// Stat returns what Drive reports about the file with the given ID, or
// ErrFileNotFound. Sign-in errors are as for Upload. extra options are for
// tests.
func Stat(ctx context.Context, client Client, tokens KeyringStore, id string, extra ...option.ClientOption) (FileInfo, error) {
	svc, err := serviceFromStore(ctx, client, tokens, extra...)
	if err != nil {
		return FileInfo{}, err
	}
	f, err := svc.Files.Get(id).Fields(uploadFields).Context(ctx).Do()
	var gerr *googleapi.Error
	if errors.As(err, &gerr) && gerr.Code == http.StatusNotFound {
		return FileInfo{}, fmt.Errorf("%w: %s", ErrFileNotFound, id)
	}
	if err != nil {
		return FileInfo{}, sessionError(fmt.Errorf("stat %s: %w", id, err))
	}
	return fileInfo(f), nil
}

// FindByTags returns the live file in MySync tagged with watchID and relPath,
// or ErrFileNotFound. It is the same lookup Upload uses to decide between
// create and update. Sign-in errors are as for Upload. extra options are for
// tests.
func FindByTags(ctx context.Context, client Client, tokens KeyringStore, roots FileStore, watchID, relPath string, extra ...option.ClientOption) (FileInfo, error) {
	svc, err := serviceFromStore(ctx, client, tokens, extra...)
	if err != nil {
		return FileInfo{}, err
	}
	rootID, err := ensureRootWith(ctx, svc, roots)
	if err != nil {
		return FileInfo{}, sessionError(err)
	}
	f, err := findByTags(ctx, svc, rootID, watchID, relPath)
	if err != nil {
		return FileInfo{}, sessionError(err)
	}
	if f == nil {
		return FileInfo{}, fmt.Errorf("%w: watch %q, path %q", ErrFileNotFound, watchID, relPath)
	}
	return fileInfo(f), nil
}

// findByTags returns the live file in parent carrying the given tags, or nil.
// Drive allows duplicate names, so the tags (not the name) identify the file;
// if several match the oldest wins.
func findByTags(ctx context.Context, svc *drv.Service, parent, watchID, relPath string) (*drv.File, error) {
	q := fmt.Sprintf("appProperties has { key='%s' and value='%s' } and appProperties has { key='%s' and value='%s' } and trashed = false and '%s' in parents",
		tagWatchID, escapeQuery(watchID), tagRelPath, escapeQuery(relPath), parent)
	f, err := oldestMatch(ctx, svc, q, uploadFields)
	if err != nil {
		return nil, fmt.Errorf("find %s: %w", relPath, err)
	}
	return f, nil
}

// escapeQuery escapes a value for use inside a single-quoted Drive query string.
func escapeQuery(s string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s)
}

func fileInfo(f *drv.File) FileInfo {
	return FileInfo{
		ID:         f.Id,
		Name:       f.Name,
		Size:       f.Size,
		MD5:        f.Md5Checksum,
		RevisionID: f.HeadRevisionId,
		WatchID:    f.AppProperties[tagWatchID],
		RelPath:    f.AppProperties[tagRelPath],
		WebLink:    f.WebViewLink,
	}
}

// oldestMatch returns the oldest live file matching the Drive query q, with
// only the given fields, or nil when there is none. Drive allows duplicate
// names, so when several match the oldest wins: the choice is stable and
// nothing is deleted or merged.
func oldestMatch(ctx context.Context, svc *drv.Service, q, fields string) (*drv.File, error) {
	list, err := svc.Files.List().
		Q(q).
		OrderBy("createdTime").
		PageSize(1).
		Fields(googleapi.Field("files(" + fields + ")")).
		Context(ctx).Do()
	if err != nil || len(list.Files) == 0 {
		return nil, err
	}
	return list.Files[0], nil
}
