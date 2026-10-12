package drive

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	drv "google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

// ErrPageTokenExpired means Drive no longer accepts the stored changes page
// token. Take a new one with StartPageToken; changes before it are not reported.
var ErrPageTokenExpired = errors.New("drive: changes page token expired")

// Change is the latest state of one file that changed on Drive.
type Change struct {
	FileID     string
	Name       string
	RelPath    string // the rel_path tag; the file name for a file without one
	Tagged     bool
	Removed    bool // gone for good: only FileID is known
	Trashed    bool
	MD5        string
	RevisionID string
	Time       time.Time
}

const changeFields = "nextPageToken, newStartPageToken, changes(fileId, removed, time, file(id,name,mimeType,trashed,md5Checksum,headRevisionId,appProperties))"

// StartPageToken returns a token that marks "now" in the Drive changes feed.
func (u *Uploader) StartPageToken(ctx context.Context) (string, error) {
	var tok string
	err := withRetry(ctx, func() error {
		if err := u.connect(ctx); err != nil {
			return err
		}
		r, err := u.svc.Changes.GetStartPageToken().Context(ctx).Do()
		if err != nil {
			return sessionError(fmt.Errorf("start page token: %w", err))
		}
		tok = r.StartPageToken
		return nil
	})
	return tok, err
}

// Changes returns the files that changed since token (oldest first, one entry
// per file with its latest state) and the token to use next time. Folders and
// the shared settings file are left out. An expired or malformed token gives
// ErrPageTokenExpired.
func (u *Uploader) Changes(ctx context.Context, token string) ([]Change, string, error) {
	var out []Change
	var next string
	err := withRetry(ctx, func() error {
		if err := u.connect(ctx); err != nil {
			return err
		}
		out, next = out[:0], "" // a retry starts over
		for page := token; page != ""; {
			r, err := u.svc.Changes.List(page).PageSize(1000).IncludeRemoved(true).
				Fields(googleapi.Field(changeFields)).Context(ctx).Do()
			if err != nil {
				var ge *googleapi.Error
				if errors.As(err, &ge) && (ge.Code == http.StatusGone || ge.Code == http.StatusBadRequest) {
					return fmt.Errorf("%w: %v", ErrPageTokenExpired, err)
				}
				return sessionError(fmt.Errorf("list changes: %w", err))
			}
			for _, c := range r.Changes {
				if ch, ok := toChange(c); ok {
					out = append(out, ch)
				}
			}
			if r.NewStartPageToken != "" {
				next = r.NewStartPageToken
			}
			page = r.NextPageToken
		}
		if next == "" {
			return errors.New("list changes: Drive returned no new page token")
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return collapse(out), next, nil
}

// toChange converts one feed entry; false means it is not about a file we track
// (a folder, or the shared settings file).
func toChange(c *drv.Change) (Change, bool) {
	ch := Change{FileID: c.FileId, Removed: c.Removed}
	if t, err := time.Parse(time.RFC3339, c.Time); err == nil {
		ch.Time = t
	}
	f := c.File
	if c.Removed || f == nil {
		ch.Removed = true
		return ch, true
	}
	if f.MimeType == folderMimeType || f.AppProperties[tagRole] == roleConfig {
		return Change{}, false
	}
	ch.Name, ch.RelPath, ch.Trashed = f.Name, f.Name, f.Trashed
	ch.MD5, ch.RevisionID = f.Md5Checksum, f.HeadRevisionId
	if tag := f.AppProperties[tagRelPath]; tag != "" {
		ch.RelPath, ch.Tagged = tag, true
	}
	return ch, true
}

// collapse keeps the last change of each file, at the place it happened: only
// the final state matters.
func collapse(in []Change) []Change {
	last := make(map[string]int, len(in))
	for i, c := range in {
		last[c.FileID] = i
	}
	out := in[:0:0]
	for i, c := range in {
		if last[c.FileID] == i {
			out = append(out, c)
		}
	}
	return out
}
