package drive

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"golang.org/x/oauth2"
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
func EnsureRoot(ctx context.Context, client Client, tokens TokenStore, roots RootStore, extra ...option.ClientOption) (string, error) {
	return ensureRoot(ctx, client.OAuthConfig(""), tokens, roots, extra...)
}

func ensureRoot(ctx context.Context, cfg *oauth2.Config, tokens TokenStore, roots RootStore, extra ...option.ClientOption) (string, error) {
	rt, err := tokens.Load()
	if err != nil {
		return "", err
	}
	svc, err := newService(ctx, cfg, TokenFromRefresh(rt), extra...)
	if err != nil {
		return "", err
	}

	// The cache is disposable, so an unreadable one is the same as an empty one.
	if cached, err := roots.Load(); err == nil {
		ok, err := rootUsable(ctx, svc, cached)
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

// rootUsable reports whether the cached folder still exists and is not
// trashed. Gone (404, which drive.file also returns for files it cannot see)
// and trashed both mean "look it up again"; any other failure is an error.
func rootUsable(ctx context.Context, svc *drv.Service, id string) (bool, error) {
	f, err := svc.Files.Get(id).Fields("id,trashed").Context(ctx).Do()
	var gerr *googleapi.Error
	if errors.As(err, &gerr) && gerr.Code == http.StatusNotFound {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check cached %s folder: %w", RootFolderName, err)
	}
	return !f.Trashed, nil
}

// findOrCreateRoot looks for MySync under My Drive root and creates it when
// absent. Drive allows duplicate names, so when several match the oldest wins:
// the choice is stable and nothing is deleted or merged.
func findOrCreateRoot(ctx context.Context, svc *drv.Service) (string, error) {
	q := fmt.Sprintf("name = '%s' and mimeType = '%s' and 'root' in parents and trashed = false",
		RootFolderName, folderMimeType)
	list, err := svc.Files.List().
		Q(q).
		OrderBy("createdTime").
		PageSize(1).
		Fields("files(id)").
		Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("find %s folder: %w", RootFolderName, err)
	}
	if len(list.Files) > 0 {
		return list.Files[0].Id, nil
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
