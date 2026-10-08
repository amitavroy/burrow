package drive

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/amitavroy/burrow/internal/store"
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

	// Key of the appProperty written on every upload. Files are tracked by
	// Drive ID; this tag lets the local state be rebuilt from Drive alone.
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
	RelPath    string
	WebLink    string
}

// Upload sends one local file to Drive. It is NewUploader(...).Upload(...) for
// a single call; use an Uploader to send many files, so the Drive service and
// the MySync lookup are built once.
func Upload(ctx context.Context, client Client, tokens KeyringStore, roots FileStore, folders *store.Repo, localPath, relPath string, extra ...option.ClientOption) (FileInfo, error) {
	return NewUploader(client, tokens, roots, folders, extra...).Upload(ctx, localPath, relPath)
}

// Uploader sends local files to Drive. The Drive service, its token source
// and the MySync folder ID are built on the first upload and reused after
// that, so a whole tree costs one token refresh and one root check. It is not
// safe for concurrent use.
type Uploader struct {
	client  Client
	tokens  KeyringStore
	roots   FileStore
	folders *store.Repo
	extra   []option.ClientOption

	svc    *drv.Service
	rootID string
}

// NewUploader returns an Uploader. folders caches folder IDs (nil means no
// cache). Nothing is sent to Drive until the first Upload. extra options are
// for tests.
func NewUploader(client Client, tokens KeyringStore, roots FileStore, folders *store.Repo, extra ...option.ClientOption) *Uploader {
	return &Uploader{client: client, tokens: tokens, roots: roots, folders: folders, extra: extra}
}

// connect builds the Drive service and finds the MySync folder, once.
func (u *Uploader) connect(ctx context.Context) error {
	if u.svc != nil {
		return nil
	}
	svc, err := serviceFromStore(ctx, u.client, u.tokens, u.extra...)
	if err != nil {
		return err
	}
	rootID, err := ensureRootWith(ctx, svc, u.roots)
	if err != nil {
		return sessionError(err)
	}
	u.svc, u.rootID = svc, rootID
	return nil
}

// Upload sends the local file at localPath to Drive, tagged with relPath. If a
// live file with the same tag exists its content is replaced (same Drive ID),
// otherwise a new file is created in the folder that mirrors relPath's
// directory under MySync, creating any missing folders on the way. The folder
// cache is best effort, so a cache error never fails an upload. relPath must
// be slash-form with no empty, "." or ".." component. It returns
// ErrNotSignedIn or ErrSessionExpired like EnsureRoot.
func (u *Uploader) Upload(ctx context.Context, localPath, relPath string) (FileInfo, error) {
	if err := validateRelPath(relPath); err != nil {
		return FileInfo{}, err
	}
	f, err := os.Open(localPath)
	if err != nil {
		return FileInfo{}, fmt.Errorf("open %s: %w", localPath, err)
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil {
		return FileInfo{}, fmt.Errorf("stat %s: %w", localPath, err)
	} else if st.IsDir() {
		return FileInfo{}, fmt.Errorf("%s is a directory", localPath)
	}

	if err := u.connect(ctx); err != nil {
		return FileInfo{}, err
	}
	existing, err := findByTags(ctx, u.svc, relPath)
	if err != nil {
		return FileInfo{}, sessionError(err)
	}

	tags := map[string]string{tagRelPath: relPath}
	media := googleapi.ChunkSize(uploadChunkSize)
	var out *drv.File
	if existing != nil {
		out, err = u.svc.Files.Update(existing.Id, &drv.File{AppProperties: tags}).
			Media(f, media).Fields(uploadFields).Context(ctx).Do()
	} else {
		var dirID string
		dirID, err = ensureDir(ctx, u.svc, u.folders, u.rootID, path.Dir(relPath))
		if err != nil {
			return FileInfo{}, sessionError(err)
		}
		out, err = u.svc.Files.Create(&drv.File{
			Name:          path.Base(relPath),
			Parents:       []string{dirID},
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

// FindByTags returns the live file tagged with relPath, wherever it sits in
// Drive, or ErrFileNotFound. It creates nothing. It is the same lookup Upload uses to decide between
// create and update. Sign-in errors are as for Upload. extra options are for
// tests.
func FindByTags(ctx context.Context, client Client, tokens KeyringStore, relPath string, extra ...option.ClientOption) (FileInfo, error) {
	svc, err := serviceFromStore(ctx, client, tokens, extra...)
	if err != nil {
		return FileInfo{}, err
	}
	f, err := findByTags(ctx, svc, relPath)
	if err != nil {
		return FileInfo{}, sessionError(err)
	}
	if f == nil {
		return FileInfo{}, fmt.Errorf("%w: path %q", ErrFileNotFound, relPath)
	}
	return fileInfo(f), nil
}

// findByTags returns the live file carrying the rel_path tag, wherever it
// lives, or nil. Folders are skipped. Drive allows duplicate names, so the tag
// (not the name or the parent) identifies the file; if several match the
// oldest wins. Other tags, such as the watch_id that earlier versions wrote,
// are ignored.
func findByTags(ctx context.Context, svc *drv.Service, relPath string) (*drv.File, error) {
	q := fmt.Sprintf("appProperties has { key='%s' and value='%s' } and trashed = false and mimeType != '%s'",
		tagRelPath, escapeQuery(relPath), folderMimeType)
	f, err := oldestMatch(ctx, svc, q, uploadFields)
	if err != nil {
		return nil, fmt.Errorf("find %s: %w", relPath, err)
	}
	return f, nil
}

// validateRelPath rejects a rel_path that is empty, absolute or has an empty,
// "." or ".." component. Only a slash-form path relative to the root is valid.
func validateRelPath(relPath string) error {
	if relPath == "" || strings.HasPrefix(relPath, "/") {
		return fmt.Errorf("invalid rel_path %q: must be relative and non-empty", relPath)
	}
	for _, part := range strings.Split(relPath, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("invalid rel_path %q: empty, %q or %q component", relPath, ".", "..")
		}
	}
	return nil
}

// ensureDir returns the Drive ID of the folder that mirrors relDir under the
// MySync folder rootID, creating missing folders from the top down. "." is the
// root itself. A folder is found by name and parent (oldest wins when Drive
// holds duplicates); folders carry no tags, because the tree already implies
// the path.
//
// folders, when not nil, caches relDir to folder ID. A cached ID is used only
// after Drive confirms it exists and is not trashed; otherwise the folder is
// looked up (or created) again and the cache rewritten, so a stale entry heals
// itself. The cache is disposable, so its errors are treated as a miss.
func ensureDir(ctx context.Context, svc *drv.Service, folders *store.Repo, rootID, relDir string) (string, error) {
	if relDir == "." || relDir == "" {
		return rootID, nil
	}
	if folders != nil {
		if id, err := folders.GetFolder(ctx, relDir); err == nil {
			ok, err := folderUsable(ctx, svc, id)
			if err != nil {
				return "", err
			}
			if ok {
				return id, nil
			}
		}
	}
	parentID, err := ensureDir(ctx, svc, folders, rootID, path.Dir(relDir))
	if err != nil {
		return "", err
	}
	id, err := findOrCreateFolder(ctx, svc, parentID, path.Base(relDir))
	if err != nil {
		return "", fmt.Errorf("folder %s: %w", relDir, err)
	}
	if folders != nil {
		_ = folders.PutFolder(ctx, relDir, id) // best effort
	}
	return id, nil
}

// findOrCreateFolder returns the oldest folder called name under parentID,
// creating it when there is none.
func findOrCreateFolder(ctx context.Context, svc *drv.Service, parentID, name string) (string, error) {
	q := fmt.Sprintf("name = '%s' and mimeType = '%s' and '%s' in parents and trashed = false",
		escapeQuery(name), folderMimeType, parentID)
	found, err := oldestMatch(ctx, svc, q, "id")
	if err != nil {
		return "", fmt.Errorf("find: %w", err)
	}
	if found != nil {
		return found.Id, nil
	}
	created, err := svc.Files.Create(&drv.File{
		Name:     name,
		MimeType: folderMimeType,
		Parents:  []string{parentID},
	}).Fields("id").Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("create: %w", err)
	}
	return created.Id, nil
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
