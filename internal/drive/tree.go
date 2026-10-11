package drive

import (
	"context"
	"fmt"
	"path"
	"sort"

	drv "google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

// RemoteFile is one file under MySync as Drive reports it.
type RemoteFile struct {
	ID         string
	RelPath    string // the rel_path tag; the folder path plus name for a file without one
	Tagged     bool   // RelPath came from the tag
	Size       int64
	MD5        string
	RevisionID string
}

const treeFields = "nextPageToken, files(id,name,mimeType,md5Checksum,size,headRevisionId,appProperties)"

// ListTree returns every live file under MySync, sorted by RelPath, found by
// walking the folders from the root (one list call per folder, paged). With
// the drive.file scope that is exactly what this app created. Folders are not
// listed, and the shared settings file (config.json) is skipped. A file
// without a rel_path tag, which something other than syncd put there, gets its
// path from the folders it sits in. Sign-in errors are as for Upload.
func (u *Uploader) ListTree(ctx context.Context) ([]RemoteFile, error) {
	var out []RemoteFile
	err := withRetry(ctx, func() error {
		if err := u.connect(ctx); err != nil {
			return err
		}
		out = out[:0] // a retry starts over
		type dir struct{ id, rel string }
		queue := []dir{{u.rootID, ""}}
		for len(queue) > 0 {
			d := queue[0]
			queue = queue[1:]
			q := fmt.Sprintf("'%s' in parents and trashed = false", d.id)
			err := u.svc.Files.List().Q(q).PageSize(1000).Fields(googleapi.Field(treeFields)).Context(ctx).
				Pages(ctx, func(page *drv.FileList) error {
					for _, f := range page.Files {
						rel := path.Join(d.rel, f.Name)
						switch {
						case f.MimeType == folderMimeType:
							queue = append(queue, dir{f.Id, rel})
						case f.AppProperties[tagRole] == roleConfig:
						default:
							rf := RemoteFile{ID: f.Id, RelPath: rel, Size: f.Size, MD5: f.Md5Checksum, RevisionID: f.HeadRevisionId}
							if tag := f.AppProperties[tagRelPath]; tag != "" {
								rf.RelPath, rf.Tagged = tag, true
							}
							out = append(out, rf)
						}
					}
					return nil
				})
			if err != nil {
				return sessionError(fmt.Errorf("list %s: %w", d.rel, err))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RelPath != out[j].RelPath {
			return out[i].RelPath < out[j].RelPath
		}
		return out[i].ID < out[j].ID // duplicates keep a stable order
	})
	return out, nil
}
