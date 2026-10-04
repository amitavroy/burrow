package drive

import (
	"context"
	"fmt"

	"golang.org/x/oauth2"
	drv "google.golang.org/api/drive/v3"
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
// when Google rejects the token. extra options are for tests.
func EnsureRoot(ctx context.Context, client Client, store TokenStore, extra ...option.ClientOption) (string, error) {
	return ensureRoot(ctx, client.OAuthConfig(""), store, extra...)
}

func ensureRoot(ctx context.Context, cfg *oauth2.Config, store TokenStore, extra ...option.ClientOption) (string, error) {
	rt, err := store.Load()
	if err != nil {
		return "", err
	}
	svc, err := newService(ctx, cfg, TokenFromRefresh(rt), extra...)
	if err != nil {
		return "", err
	}
	id, err := findOrCreateRoot(ctx, svc)
	if err != nil {
		return "", sessionError(err)
	}
	return id, nil
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
