package drive

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"
)

type memRoots struct {
	id    string
	saves int
}

func (m *memRoots) Load() (string, error) {
	if m.id == "" {
		return "", ErrNoRoot
	}
	return m.id, nil
}
func (m *memRoots) Save(id string) error { m.id = id; m.saves++; return nil }

type fakeFolder struct {
	id      string
	trashed bool
}

// fakeDrive answers files.list, files.get and files.create plus the token
// endpoint. listed holds the matching folders oldest first, as Drive would
// return them for orderBy=createdTime.
type fakeDrive struct {
	mu       sync.Mutex
	listed   []fakeFolder
	known    map[string]fakeFolder // answers files.get
	lists    int
	gets     int
	creates  int
	listQ    string
	listSort string
	created  map[string]any
}

func (f *fakeDrive) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/token":
		_, _ = w.Write([]byte(`{"access_token":"fresh-access","token_type":"Bearer","expires_in":3600}`))
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/files"):
		f.creates++
		_ = json.NewDecoder(r.Body).Decode(&f.created)
		_, _ = w.Write([]byte(`{"id":"new-folder"}`))
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/files"):
		f.lists++
		f.listQ = r.URL.Query().Get("q")
		f.listSort = r.URL.Query().Get("orderBy")
		files := []map[string]string{}
		for _, fo := range f.listed {
			files = append(files, map[string]string{"id": fo.id})
		}
		if r.URL.Query().Get("pageSize") == "1" && len(files) > 1 {
			files = files[:1]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"files": files})
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/files/"):
		f.gets++
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		fo, ok := f.known[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"File not found"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": fo.id, "trashed": fo.trashed})
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func TestEnsureRoot(t *testing.T) {
	tests := []struct {
		name        string
		listed      []fakeFolder
		known       map[string]fakeFolder
		cached      string
		wantID      string
		wantLists   int
		wantGets    int
		wantCreates int
		wantSaves   int
	}{
		{
			name:        "creates when absent",
			wantID:      "new-folder",
			wantLists:   1,
			wantCreates: 1,
			wantSaves:   1,
		},
		{
			name:      "reuses an existing folder",
			listed:    []fakeFolder{{id: "existing"}},
			wantID:    "existing",
			wantLists: 1,
			wantSaves: 1,
		},
		{
			name:      "oldest of duplicates wins",
			listed:    []fakeFolder{{id: "oldest"}, {id: "newer"}, {id: "newest"}},
			wantID:    "oldest",
			wantLists: 1,
			wantSaves: 1,
		},
		{
			name:     "cache hit skips the list",
			cached:   "cached",
			known:    map[string]fakeFolder{"cached": {id: "cached"}},
			wantID:   "cached",
			wantGets: 1,
		},
		{
			name:      "stale cache (404) recovers and rewrites",
			cached:    "gone",
			listed:    []fakeFolder{{id: "existing"}},
			wantID:    "existing",
			wantGets:  1,
			wantLists: 1,
			wantSaves: 1,
		},
		{
			name:        "trashed cache recovers by creating",
			cached:      "trashed",
			known:       map[string]fakeFolder{"trashed": {id: "trashed", trashed: true}},
			wantID:      "new-folder",
			wantGets:    1,
			wantLists:   1,
			wantCreates: 1,
			wantSaves:   1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fd := &fakeDrive{listed: tt.listed, known: tt.known}
			srv := httptest.NewServer(fd)
			defer srv.Close()

			cfg := Client{ID: "id", Secret: "secret"}.OAuthConfig("")
			cfg.Endpoint = oauth2.Endpoint{TokenURL: srv.URL + "/token"}
			roots := &memRoots{id: tt.cached}

			got, err := ensureRoot(context.Background(), cfg, &memStore{token: "stored-refresh"}, roots, option.WithEndpoint(srv.URL))
			if err != nil {
				t.Fatalf("ensureRoot: %v", err)
			}

			fd.mu.Lock()
			defer fd.mu.Unlock()
			if got != tt.wantID {
				t.Errorf("id = %q, want %q", got, tt.wantID)
			}
			if roots.id != tt.wantID {
				t.Errorf("cached id = %q, want %q", roots.id, tt.wantID)
			}
			if fd.lists != tt.wantLists || fd.gets != tt.wantGets || fd.creates != tt.wantCreates {
				t.Errorf("lists/gets/creates = %d/%d/%d, want %d/%d/%d",
					fd.lists, fd.gets, fd.creates, tt.wantLists, tt.wantGets, tt.wantCreates)
			}
			if roots.saves != tt.wantSaves {
				t.Errorf("saves = %d, want %d", roots.saves, tt.wantSaves)
			}
			if tt.wantLists > 0 {
				if fd.listSort != "createdTime" {
					t.Errorf("orderBy = %q, want createdTime", fd.listSort)
				}
				for _, part := range []string{"name = 'MySync'", folderMimeType, "'root' in parents", "trashed = false"} {
					if !strings.Contains(fd.listQ, part) {
						t.Errorf("list query %q missing %q", fd.listQ, part)
					}
				}
			}
			if tt.wantCreates > 0 {
				if fd.created["name"] != RootFolderName || fd.created["mimeType"] != folderMimeType {
					t.Errorf("create body = %v", fd.created)
				}
				parents, _ := fd.created["parents"].([]any)
				if len(parents) != 1 || parents[0] != "root" {
					t.Errorf("create parents = %v, want [root]", fd.created["parents"])
				}
			}
		})
	}
}

func TestEnsureRootNotSignedIn(t *testing.T) {
	cfg := Client{ID: "id", Secret: "secret"}.OAuthConfig("")
	_, err := ensureRoot(context.Background(), cfg, &memStore{}, &memRoots{})
	if err == nil || !strings.Contains(err.Error(), ErrNotSignedIn.Error()) {
		t.Fatalf("err = %v, want ErrNotSignedIn", err)
	}
}
