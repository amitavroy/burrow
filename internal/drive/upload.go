package drive

import (
	"context"
	"fmt"
	"os"
	"strings"

	"golang.org/x/oauth2"
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
func Upload(ctx context.Context, client Client, tokens TokenStore, roots RootStore, path, watchID, relPath string, extra ...option.ClientOption) (FileInfo, error) {
	return uploadFile(ctx, client.OAuthConfig(""), tokens, roots, path, watchID, relPath, extra...)
}

func uploadFile(ctx context.Context, cfg *oauth2.Config, tokens TokenStore, roots RootStore, path, watchID, relPath string, extra ...option.ClientOption) (FileInfo, error) {
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

	svc, err := serviceFromStore(ctx, cfg, tokens, extra...)
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

// findByTags returns the live file in parent carrying the given tags, or nil.
// Drive allows duplicate names, so the tags (not the name) identify the file;
// if several match the oldest wins.
func findByTags(ctx context.Context, svc *drv.Service, parent, watchID, relPath string) (*drv.File, error) {
	q := fmt.Sprintf("appProperties has { key='%s' and value='%s' } and appProperties has { key='%s' and value='%s' } and trashed = false and '%s' in parents",
		tagWatchID, escapeQuery(watchID), tagRelPath, escapeQuery(relPath), parent)
	list, err := svc.Files.List().
		Q(q).
		OrderBy("createdTime").
		PageSize(1).
		Fields("files(" + uploadFields + ")").
		Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("find %s: %w", relPath, err)
	}
	if len(list.Files) == 0 {
		return nil, nil
	}
	return list.Files[0], nil
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
