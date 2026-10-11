package drive

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"sync"
	"testing"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"
)

// treeDrive fakes files.list by parent: children[parentID] are the entries,
// served two per page to exercise paging.
type treeDrive struct {
	mu       sync.Mutex
	children map[string][]map[string]any
	queries  []string
}

var parentRe = regexp.MustCompile(`'([^']+)' in parents`)

func (d *treeDrive) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case r.URL.Path == "/token":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"a","token_type":"Bearer","expires_in":3600}`))
	case r.URL.Path == "/files/root-id":
		_, _ = w.Write([]byte(`{"id":"root-id","trashed":false}`))
	case r.URL.Path == "/files":
		q := r.URL.Query().Get("q")
		d.queries = append(d.queries, q)
		kids := d.children[parentRe.FindStringSubmatch(q)[1]]
		start := 0
		if tok := r.URL.Query().Get("pageToken"); tok != "" {
			start = 2
		}
		end := min(start+2, len(kids))
		resp := map[string]any{"files": kids[start:end]}
		if end < len(kids) {
			resp["nextPageToken"] = "p2"
		}
		_ = json.NewEncoder(w).Encode(resp)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func file(id, name, md5 string, props map[string]string) map[string]any {
	return map[string]any{"id": id, "name": name, "md5Checksum": md5, "size": "7", "headRevisionId": "rev-" + id, "appProperties": props}
}

func folder(id, name string) map[string]any {
	return map[string]any{"id": id, "name": name, "mimeType": folderMimeType}
}

func TestListTreeWalksFoldersAndSkipsConfig(t *testing.T) {
	d := &treeDrive{children: map[string][]map[string]any{
		"root-id": {
			file("f-z", "z.txt", "mz", map[string]string{"rel_path": "z.txt"}),
			folder("d-docs", "docs"),
			file("f-cfg", "config.json", "", map[string]string{"burrow_role": "config"}),
			file("f-a", "a.txt", "ma", map[string]string{"rel_path": "a.txt"}),
		},
		"d-docs": {
			folder("d-sub", "sub"),
			file("f-b", "b.txt", "mb", map[string]string{"rel_path": "docs/b.txt"}),
			file("f-stray", "stray.txt", "ms", nil), // not put there by syncd: path from the tree
		},
		"d-sub": {file("f-c", "c.txt", "mc", map[string]string{"rel_path": "docs/sub/c.txt"})},
	}}
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	client := Client{ID: "id", Secret: "secret", Endpoint: &oauth2.Endpoint{TokenURL: srv.URL + "/token"}}
	u := NewUploader(client, signedIn(t, "r"), cacheAt(t, "root-id"), nil, option.WithEndpoint(srv.URL))

	got, err := u.ListTree(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var rels []string
	for _, f := range got {
		rels = append(rels, f.RelPath)
	}
	want := []string{"a.txt", "docs/b.txt", "docs/stray.txt", "docs/sub/c.txt", "z.txt"}
	if !reflect.DeepEqual(rels, want) {
		t.Errorf("paths = %v, want %v", rels, want)
	}
	b := got[1]
	if b.ID != "f-b" || b.MD5 != "mb" || b.Size != 7 || b.RevisionID != "rev-f-b" || !b.Tagged {
		t.Errorf("docs/b.txt = %+v", b)
	}
	if got[2].Tagged {
		t.Errorf("stray file should not be Tagged: %+v", got[2])
	}
	if len(d.queries) != 5 { // root 2 pages, docs 2 pages, sub 1
		t.Errorf("list calls = %d (%v), want 5", len(d.queries), d.queries)
	}
}
