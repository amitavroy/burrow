package drive

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"
)

// changesDrive fakes the changes feed. pages maps a page token to the reply
// for it; failures[token] are HTTP statuses returned (once each, in order)
// before the real reply.
type changesDrive struct {
	mu       sync.Mutex
	pages    map[string]map[string]any
	failures map[string][]int
	asked    []string
}

func (d *changesDrive) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch r.URL.Path {
	case "/token":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"a","token_type":"Bearer","expires_in":3600}`))
	case "/files/root-id":
		_, _ = w.Write([]byte(`{"id":"root-id","trashed":false}`))
	case "/changes/startPageToken":
		_, _ = w.Write([]byte(`{"startPageToken":"t0"}`))
	case "/changes":
		tok := r.URL.Query().Get("pageToken")
		d.asked = append(d.asked, tok)
		if f := d.failures[tok]; len(f) > 0 {
			d.failures[tok] = f[1:]
			w.WriteHeader(f[0])
			_, _ = w.Write([]byte(`{"error":{"message":"fake failure"}}`))
			return
		}
		page, ok := d.pages[tok]
		if !ok {
			w.WriteHeader(http.StatusGone)
			return
		}
		_ = json.NewEncoder(w).Encode(page)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func change(id, name string, props map[string]string, extra map[string]any) map[string]any {
	f := map[string]any{"id": id, "name": name, "md5Checksum": "m-" + id, "headRevisionId": "rev-" + id, "appProperties": props}
	for k, v := range extra {
		f[k] = v
	}
	return map[string]any{"fileId": id, "time": "2026-10-12T10:00:00Z", "file": f}
}

func changesUploader(t *testing.T, d *changesDrive) *Uploader {
	t.Helper()
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	client := Client{ID: "id", Secret: "secret", Endpoint: &oauth2.Endpoint{TokenURL: srv.URL + "/token"}}
	return NewUploader(client, signedIn(t, "r"), cacheAt(t, "root-id"), nil, option.WithEndpoint(srv.URL))
}

func TestStartPageToken(t *testing.T) {
	u := changesUploader(t, &changesDrive{})
	if tok, err := u.StartPageToken(context.Background()); err != nil || tok != "t0" {
		t.Fatalf("token = %q, %v", tok, err)
	}
}

func TestChangesPagesFiltersAndCollapses(t *testing.T) {
	d := &changesDrive{pages: map[string]map[string]any{
		"t0": {"nextPageToken": "p2", "changes": []any{
			change("f-a", "a.txt", map[string]string{"rel_path": "docs/a.txt"}, nil),
			change("d-1", "docs", nil, map[string]any{"mimeType": folderMimeType}),
			change("f-cfg", "config.json", map[string]string{"burrow_role": "config"}, nil),
			change("f-b", "b.txt", nil, nil), // no tag: the name stands in
		}},
		"p2": {"nextPageToken": "p3", "changes": []any{
			change("f-a", "a.txt", map[string]string{"rel_path": "docs/a.txt"}, map[string]any{"headRevisionId": "rev-2"}),
		}},
		"p3": {"newStartPageToken": "t1", "changes": []any{
			map[string]any{"fileId": "f-gone", "removed": true, "time": "2026-10-12T10:01:00Z"},
			change("f-c", "c.txt", map[string]string{"rel_path": "c.txt"}, map[string]any{"trashed": true}),
		}},
	}}
	u := changesUploader(t, d)

	got, next, err := u.Changes(context.Background(), "t0")
	if err != nil {
		t.Fatal(err)
	}
	if next != "t1" || !reflect.DeepEqual(d.asked, []string{"t0", "p2", "p3"}) {
		t.Errorf("next = %q, asked %v", next, d.asked)
	}
	type row struct {
		id, rel, rev             string
		tagged, removed, trashed bool
	}
	var rows []row
	for _, c := range got {
		rows = append(rows, row{c.FileID, c.RelPath, c.RevisionID, c.Tagged, c.Removed, c.Trashed})
	}
	want := []row{
		{"f-b", "b.txt", "rev-f-b", false, false, false},
		{"f-a", "docs/a.txt", "rev-2", true, false, false}, // two edits, the last one, in its place
		{"f-gone", "", "", false, true, false},
		{"f-c", "c.txt", "rev-f-c", true, false, true},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("changes = %+v\nwant      %+v", rows, want)
	}
}

func TestChangesExpiredToken(t *testing.T) {
	u := changesUploader(t, &changesDrive{pages: map[string]map[string]any{}})
	if _, _, err := u.Changes(context.Background(), "old"); !errors.Is(err, ErrPageTokenExpired) {
		t.Fatalf("err = %v, want ErrPageTokenExpired", err)
	}
}

func TestChangesRetriesA429(t *testing.T) {
	d := &changesDrive{
		pages:    map[string]map[string]any{"t0": {"newStartPageToken": "t1", "changes": []any{}}},
		failures: map[string][]int{"t0": {429, 503}},
	}
	old := retrySleep
	t.Cleanup(func() { retrySleep = old })
	retrySleep = func(context.Context, time.Duration) error { return nil }
	u := changesUploader(t, d)
	if _, next, err := u.Changes(context.Background(), "t0"); err != nil || next != "t1" {
		t.Fatalf("next = %q, %v", next, err)
	}
	if len(d.asked) != 3 {
		t.Errorf("asked %v, want 3 attempts", d.asked)
	}
}
