package drive

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	drv "google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

const (
	// RootFolderName is the app-owned folder at the top of My Drive. With the
	// drive.file scope the app can only see a folder it created itself.
	RootFolderName = "MySync"

	folderMimeType = "application/vnd.google-apps.folder"
)

// EnsureRoot returns the Drive ID of the app-owned MySync folder, creating it
// if this app has not made one yet. It signs in from the stored refresh token,
// so it returns ErrNotSignedIn when nothing is stored and ErrSessionExpired
// when Google rejects the token. A cached ID from roots is reused only after
// Drive confirms the folder still exists and is not in the trash; otherwise the
// folder is looked up (or created) and the cache rewritten. extra options are
// for tests.
func EnsureRoot(ctx context.Context, client Client, tokens KeyringStore, roots FileStore, extra ...option.ClientOption) (string, error) {
	svc, err := serviceFromStore(ctx, client, tokens, extra...)
	if err != nil {
		return "", err
	}
	return ensureRootWith(ctx, svc, roots)
}

// serviceFromStore builds a Drive service from the stored refresh token. It
// returns ErrNotSignedIn when nothing is stored.
func serviceFromStore(ctx context.Context, client Client, tokens KeyringStore, extra ...option.ClientOption) (*drv.Service, error) {
	rt, err := tokens.Load()
	if err != nil {
		return nil, err
	}
	return newService(ctx, client, TokenFromRefresh(rt), extra...)
}

// ensureRootWith is ensureRoot on an already-built service.
func ensureRootWith(ctx context.Context, svc *drv.Service, roots FileStore) (string, error) {
	// The cache is disposable, so an unreadable one is the same as an empty one.
	if cached, err := roots.Load(); err == nil {
		ok, err := folderUsable(ctx, svc, cached)
		if err != nil {
			return "", sessionError(err)
		}
		if ok {
			return cached, nil
		}
	}

	id, err := findOrCreateRoot(ctx, svc)
	if err != nil {
		return "", sessionError(err)
	}
	if err := roots.Save(id); err != nil {
		return "", err
	}
	return id, nil
}

// folderUsable reports whether a cached folder ID still exists and is not
// trashed. Gone (404, which drive.file also returns for files it cannot see)
// and trashed both mean "look it up again"; any other failure is an error.
// Trashing a folder trashes everything inside it, so checking the deepest
// cached folder is enough.
func folderUsable(ctx context.Context, svc *drv.Service, id string) (bool, error) {
	f, err := svc.Files.Get(id).Fields("id,trashed").Context(ctx).Do()
	var gerr *googleapi.Error
	if errors.As(err, &gerr) && gerr.Code == http.StatusNotFound {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check cached folder: %w", err)
	}
	return !f.Trashed, nil
}

// findOrCreateRoot looks for MySync under My Drive root and creates it when
// absent. Drive allows duplicate names, so when several match the oldest wins:
// the choice is stable and nothing is deleted or merged.
func findOrCreateRoot(ctx context.Context, svc *drv.Service) (string, error) {
	q := fmt.Sprintf("name = '%s' and mimeType = '%s' and 'root' in parents and trashed = false",
		RootFolderName, folderMimeType)
	found, err := oldestMatch(ctx, svc, q, "id")
	if err != nil {
		return "", fmt.Errorf("find %s folder: %w", RootFolderName, err)
	}
	if found != nil {
		return found.Id, nil
	}

	folder, err := svc.Files.Create(&drv.File{
		Name:     RootFolderName,
		MimeType: folderMimeType,
		Parents:  []string{"root"},
	}).Fields("id").Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("create %s folder: %w", RootFolderName, err)
	}
	return folder.Id, nil
}
