package drive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amitavroy/burrow/internal/store"
	"golang.org/x/oauth2"
	"google.golang.org/api/option"
)

// fakeDir is a folder the fake Drive holds.
type fakeDir struct {
	id, name, parent string
	trashed          bool
}

// uploadDrive fakes the Drive routes Upload touches: the token endpoint,
// files.get for the cached root, the tag lookup, folder lookup and creation,
// and resumable create/update uploads. existingID, when set, is what the tag
// lookup returns. dirs are the folders that already exist, oldest first.
type uploadDrive struct {
	mu         sync.Mutex
	url        string
	existingID string
	tokenBody  string // when set, the token endpoint answers 400 with it
	dirs       []fakeDir

	dirGets     []string         // IDs asked of files.get
	tokenReqs   int              // requests to the token endpoint
	rootGets    int              // files.get calls for the cached root
	failCreate  int              // when set, uploads answer this HTTP status
	folderQs    []string         // folder lookup queries
	createdDirs []map[string]any // folder create bodies, in order

	listQ    string
	meta     map[string]any
	method   string // POST (create) or PATCH (update)
	patchID  string
	ranges   []string
	received int
	authz    []string

	// onChunk, when set, runs for every resumable chunk after it is received.
	// With it set the chunk body is counted and discarded, not buffered.
	onChunk func(chunk int)
}

func (f *uploadDrive) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	if r.URL.Path != "/token" {
		f.authz = append(f.authz, r.Header.Get("Authorization"))
	}
	switch {
	case path == "/token":
		f.tokenReqs++
		if f.tokenBody != "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(f.tokenBody))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"fresh-access","token_type":"Bearer","expires_in":3600}`))
	case strings.HasPrefix(path, "/session/"):
		f.session(w, r)
	case strings.HasPrefix(path, "/upload/") && r.URL.Query().Get("uploadType") == "multipart":
		f.multipart(w, r)
	case strings.HasPrefix(path, "/upload/") && r.URL.Query().Get("uploadType") == "resumable":
		f.method = r.Method
		if r.Method == http.MethodPatch {
			f.patchID = path[strings.LastIndex(path, "/")+1:]
		}
		_ = json.NewDecoder(r.Body).Decode(&f.meta)
		w.Header().Set("Location", f.url+"/session/x")
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/files"):
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.createdDirs = append(f.createdDirs, body)
		id := fmt.Sprintf("dir-%d", len(f.createdDirs))
		parents, _ := body["parents"].([]any)
		parent, _ := parents[0].(string)
		f.dirs = append(f.dirs, fakeDir{id: id, name: body["name"].(string), parent: parent})
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/files") && strings.Contains(r.URL.Query().Get("q"), "mimeType = '"+folderMimeType+"'"):
		q := r.URL.Query().Get("q")
		f.folderQs = append(f.folderQs, q)
		name := unescapeQuery(folderNameRe.FindStringSubmatch(q)[1])
		parent := folderParentRe.FindStringSubmatch(q)[1]
		files := []map[string]any{}
		for _, d := range f.dirs {
			if d.name == name && d.parent == parent && !d.trashed {
				files = append(files, map[string]any{"id": d.id})
			}
		}
		if len(files) > 1 && r.URL.Query().Get("pageSize") == "1" {
			files = files[:1] // Drive returns only a page; the client asks for one
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"files": files})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/files"):
		f.listQ = r.URL.Query().Get("q")
		files := []map[string]any{}
		if f.existingID != "" {
			files = append(files, existingFile(f.existingID))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"files": files})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/files/root-id"):
		f.rootGets++
		_, _ = w.Write([]byte(`{"id":"root-id","trashed":false}`))
	case r.Method == http.MethodGet && strings.Contains(path, "/files/"):
		id := path[strings.LastIndex(path, "/")+1:]
		for _, d := range f.dirs {
			if d.id == id {
				f.dirGets = append(f.dirGets, id)
				_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "trashed": d.trashed})
				return
			}
		}
		if id != f.existingID {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"File not found"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(existingFile(id))
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

var (
	folderNameRe   = regexp.MustCompile(`name = '((?:[^'\\]|\\.)*)'`)
	folderParentRe = regexp.MustCompile(`'([^']+)' in parents`)
)

// unescapeQuery undoes escapeQuery.
func unescapeQuery(s string) string {
	return strings.NewReplacer(`\\`, `\`, `\'`, `'`).Replace(s)
}

// existingFile is what the fake reports for the file that already exists.
func existingFile(id string) map[string]any {
	return map[string]any{
		"id": id, "name": "notes.txt", "size": "42", "md5Checksum": "abc", "headRevisionId": "rev9",
		"appProperties": map[string]string{"watch_id": "default", "rel_path": "notes.txt"}, // watch_id: legacy tag from earlier versions
	}
}

// multipart takes a file that fits in one chunk: metadata JSON, then content,
// in a single multipart/related request.
func (f *uploadDrive) multipart(w http.ResponseWriter, r *http.Request) {
	if f.failCreate != 0 {
		w.WriteHeader(f.failCreate)
		_, _ = w.Write([]byte(`{"error":{"code":` + strconv.Itoa(f.failCreate) + `,"message":"nope"}}`))
		return
	}
	f.method = r.Method
	if r.Method == http.MethodPatch {
		f.patchID = r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	}
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	meta, err := mr.NextPart()
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	_ = json.NewDecoder(meta).Decode(&f.meta)
	content, err := mr.NextPart()
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	data, _ := io.ReadAll(content)
	f.received += len(data)
	f.respondFile(w, len(data))
}

// session accepts upload chunks. A chunk that does not reach the total size
// gets the 308-override reply like Drive's resumable protocol; the last one gets the file.
func (f *uploadDrive) session(w http.ResponseWriter, r *http.Request) {
	cr := r.Header.Get("Content-Range")
	f.ranges = append(f.ranges, cr)
	if f.onChunk != nil {
		n, _ := io.Copy(io.Discard, r.Body)
		f.received += int(n)
		f.onChunk(len(f.ranges))
	} else {
		body, _ := io.ReadAll(r.Body)
		f.received += len(body)
	}
	// "bytes 0-8388607/9437184"
	var from, to, total int
	// A file that is an exact multiple of the chunk size ends with an empty
	// "bytes */total" request, which finalizes the upload.
	if _, err := fmt.Sscanf(cr, "bytes */%d", &total); err == nil {
		f.respondFile(w, total)
		return
	}
	if _, err := fmt.Sscanf(cr, "bytes %d-%d/%d", &from, &to, &total); err != nil || to+1 < total {
		w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", to))
		// The client sends X-GUploader-No-308, so Drive signals "incomplete"
		// with 200 plus this override header instead of a real 308.
		w.Header().Set("X-Http-Status-Code-Override", "308")
		w.WriteHeader(http.StatusOK)
		return
	}
	f.respondFile(w, total)
}

func (f *uploadDrive) respondFile(w http.ResponseWriter, size int) {
	id := "new-file"
	if f.method == http.MethodPatch {
		id = f.patchID
	}
	props, _ := f.meta["appProperties"].(map[string]any)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": id, "name": f.meta["name"], "size": strconv.Itoa(size),
		"md5Checksum": "abc", "headRevisionId": "rev1", "appProperties": props,
		"webViewLink": "https://drive.google.com/file/d/" + id + "/view",
	})
}

func writeFile(t *testing.T, size int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(p, []byte(strings.Repeat("x", size)), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func setup(t *testing.T, fd *uploadDrive) (Client, []option.ClientOption) {
	t.Helper()
	srv := httptest.NewServer(fd)
	t.Cleanup(srv.Close)
	fd.url = srv.URL
	client := Client{ID: "id", Secret: "secret", Endpoint: &oauth2.Endpoint{TokenURL: srv.URL + "/token"}}
	return client, []option.ClientOption{option.WithEndpoint(srv.URL)}
}

func TestUpload(t *testing.T) {
	tests := []struct {
		name       string
		size       int
		existingID string
		relPath    string
		wantMethod string
		wantID     string
		wantChunks int // resumable chunk requests; 0 = sent as one multipart request
		wantName   string
		wantParent string // parent of a created file; "" means root-id
	}{
		{name: "creates a new file", size: 100, relPath: "notes.txt", wantMethod: http.MethodPost, wantID: "new-file", wantChunks: 0, wantName: "notes.txt"},
		{name: "second put updates the same ID", size: 100, existingID: "old-id", relPath: "notes.txt", wantMethod: http.MethodPatch, wantID: "old-id", wantChunks: 0},
		{name: "nested rel_path names the file by basename", size: 100, relPath: "a/b/notes.txt", wantMethod: http.MethodPost, wantID: "new-file", wantChunks: 0, wantName: "notes.txt", wantParent: "dir-2"},
		{name: "file over 8 MB is sent in chunks", size: 8<<20 + 1000, relPath: "notes.txt", wantMethod: http.MethodPost, wantID: "new-file", wantChunks: 2, wantName: "notes.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fd := &uploadDrive{existingID: tt.existingID}
			client, extra := setup(t, fd)
			path := writeFile(t, tt.size)

			info, err := Upload(context.Background(), client, signedIn(t, "stored-refresh"), cacheAt(t, "root-id"), nil, path, tt.relPath, extra...)
			if err != nil {
				t.Fatalf("Upload: %v", err)
			}

			fd.mu.Lock()
			defer fd.mu.Unlock()
			if info.ID != tt.wantID || info.RelPath != tt.relPath || info.RevisionID != "rev1" || info.WebLink == "" {
				t.Errorf("info = %+v", info)
			}
			if fd.method != tt.wantMethod {
				t.Errorf("method = %s, want %s", fd.method, tt.wantMethod)
			}
			props, _ := fd.meta["appProperties"].(map[string]any)
			if props["rel_path"] != tt.relPath || len(props) != 1 {
				t.Errorf("appProperties = %v, want only rel_path", props)
			}
			if tt.wantMethod == http.MethodPost {
				if fd.meta["name"] != tt.wantName {
					t.Errorf("name = %v, want %s", fd.meta["name"], tt.wantName)
				}
				wantParent := tt.wantParent
				if wantParent == "" {
					wantParent = "root-id"
				}
				parents, _ := fd.meta["parents"].([]any)
				if len(parents) != 1 || parents[0] != wantParent {
					t.Errorf("parents = %v, want [%s]", fd.meta["parents"], wantParent)
				}
			}
			for _, part := range []string{"key='rel_path' and value='" + tt.relPath + "'", "trashed = false", "mimeType != '" + folderMimeType + "'"} {
				if !strings.Contains(fd.listQ, part) {
					t.Errorf("tag query %q missing %q", fd.listQ, part)
				}
			}
			if strings.Contains(fd.listQ, "in parents") {
				t.Errorf("tag query %q is restricted to a parent", fd.listQ)
			}
			if strings.Contains(fd.listQ, "watch_id") {
				t.Errorf("tag query %q still mentions watch_id", fd.listQ)
			}
			if len(fd.ranges) != tt.wantChunks {
				t.Errorf("chunks = %d (%v), want %d", len(fd.ranges), fd.ranges, tt.wantChunks)
			}
			if fd.received != tt.size {
				t.Errorf("received %d bytes, want %d", fd.received, tt.size)
			}
		})
	}
}

func TestUploadEscapesQuery(t *testing.T) {
	fd := &uploadDrive{}
	client, extra := setup(t, fd)
	path := writeFile(t, 10)

	if _, err := Upload(context.Background(), client, signedIn(t, "r"), cacheAt(t, "root-id"), nil, path, `it's a\b.txt`, extra...); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if !strings.Contains(fd.listQ, `value='it\'s a\\b.txt'`) {
		t.Errorf("query not escaped: %q", fd.listQ)
	}
}

func TestUploadErrors(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name      string
		stored    string
		tokenBody string
		path      string
		wantErr   error
		wantText  string
	}{
		{name: "not signed in", path: writeFile(t, 1), wantErr: ErrNotSignedIn},
		{
			name: "revoked token is a session expiry", stored: "r", path: writeFile(t, 1),
			tokenBody: `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`,
			wantErr:   ErrSessionExpired,
		},
		{name: "missing file", stored: "r", path: filepath.Join(dir, "nope.txt"), wantText: "open"},
		{name: "directory", stored: "r", path: dir, wantText: "is a directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fd := &uploadDrive{tokenBody: tt.tokenBody}
			client, extra := setup(t, fd)

			_, err := Upload(context.Background(), client, signedIn(t, tt.stored), cacheAt(t, "root-id"), nil, tt.path, "x.txt", extra...)

			if err == nil {
				t.Fatal("err = nil, want an error")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantText != "" && !strings.Contains(err.Error(), tt.wantText) {
				t.Errorf("err = %v, want it to contain %q", err, tt.wantText)
			}
			if strings.Contains(err.Error(), "fresh-access") || strings.Contains(err.Error(), "stored-refresh") {
				t.Errorf("error leaks a token: %v", err)
			}
		})
	}
}

func TestStatAndFindByTags(t *testing.T) {
	tests := []struct {
		name    string
		stored  string
		find    bool // use FindByTags instead of Stat by ID
		wantErr error
	}{
		{name: "stat by ID"},
		{name: "stat by ID, unknown file", wantErr: ErrFileNotFound},
		{name: "find by tags", find: true},
		{name: "find by tags, none match", find: true, wantErr: ErrFileNotFound},
		{name: "not signed in", stored: "-", wantErr: ErrNotSignedIn},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fd := &uploadDrive{existingID: "file-1"}
			// The "unknown" and "none match" cases have nothing on Drive.
			if tt.wantErr == ErrFileNotFound {
				fd.existingID = ""
				if !tt.find {
					fd.existingID = "other"
				}
			}
			client, extra := setup(t, fd)
			token := "stored-refresh"
			if tt.stored == "-" {
				token = ""
			}

			var info FileInfo
			var err error
			if tt.find {
				info, err = FindByTags(context.Background(), client, signedIn(t, token), "notes.txt", extra...)
			} else {
				info, err = Stat(context.Background(), client, signedIn(t, token), "file-1", extra...)
			}

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("case %d: err = %v, want %v", i, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			want := FileInfo{ID: "file-1", Name: "notes.txt", Size: 42, MD5: "abc", RevisionID: "rev9", RelPath: "notes.txt"}
			if info != want {
				t.Errorf("info = %+v, want %+v", info, want)
			}
		})
	}
}

func TestUploadFolders(t *testing.T) {
	tests := []struct {
		name        string
		relPath     string
		dirs        []fakeDir
		existingID  string
		wantCreated []string // "name@parent", in order
		wantFolderQ int      // folder lookup queries
		wantParent  string   // parent of the created file
		wantMethod  string
	}{
		{
			name: "no directory means no folder calls", relPath: "c.txt",
			wantFolderQ: 0, wantParent: "root-id", wantMethod: http.MethodPost,
		},
		{
			name: "creates one folder per missing level", relPath: "a/b/c.txt",
			wantCreated: []string{"a@root-id", "b@dir-1"}, wantFolderQ: 2, wantParent: "dir-2", wantMethod: http.MethodPost,
		},
		{
			name: "reuses folders that exist", relPath: "a/b/c.txt",
			dirs:        []fakeDir{{id: "A", name: "a", parent: "root-id"}, {id: "B", name: "b", parent: "A"}},
			wantFolderQ: 2, wantParent: "B", wantMethod: http.MethodPost,
		},
		{
			name: "creates only the missing level", relPath: "a/b/c.txt",
			dirs:        []fakeDir{{id: "A", name: "a", parent: "root-id"}},
			wantCreated: []string{"b@A"}, wantFolderQ: 2, wantParent: "dir-1", wantMethod: http.MethodPost,
		},
		{
			name: "a folder of the same name under another parent is not reused", relPath: "a/c.txt",
			dirs:        []fakeDir{{id: "X", name: "a", parent: "elsewhere"}},
			wantCreated: []string{"a@root-id"}, wantFolderQ: 1, wantParent: "dir-1", wantMethod: http.MethodPost,
		},
		{
			name: "duplicate folders: the oldest wins", relPath: "a/c.txt",
			dirs:        []fakeDir{{id: "old", name: "a", parent: "root-id"}, {id: "new", name: "a", parent: "root-id"}},
			wantFolderQ: 1, wantParent: "old", wantMethod: http.MethodPost,
		},
		{
			name: "folder names with a quote and a backslash", relPath: `it's\x/c.txt`,
			wantCreated: []string{`it's\x@root-id`}, wantFolderQ: 1, wantParent: "dir-1", wantMethod: http.MethodPost,
		},
		{
			name: "an update makes no folder calls", relPath: "a/b/c.txt", existingID: "old-id",
			wantFolderQ: 0, wantMethod: http.MethodPatch,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fd := &uploadDrive{existingID: tt.existingID, dirs: tt.dirs}
			client, extra := setup(t, fd)

			_, err := Upload(context.Background(), client, signedIn(t, "r"), cacheAt(t, "root-id"), nil, writeFile(t, 10), tt.relPath, extra...)
			if err != nil {
				t.Fatalf("Upload: %v", err)
			}

			fd.mu.Lock()
			defer fd.mu.Unlock()
			var created []string
			for _, c := range fd.createdDirs {
				parents, _ := c["parents"].([]any)
				created = append(created, fmt.Sprintf("%v@%v", c["name"], parents[0]))
				if c["mimeType"] != folderMimeType {
					t.Errorf("folder create mimeType = %v", c["mimeType"])
				}
			}
			if strings.Join(created, ",") != strings.Join(tt.wantCreated, ",") {
				t.Errorf("created folders = %v, want %v", created, tt.wantCreated)
			}
			if len(fd.folderQs) != tt.wantFolderQ {
				t.Errorf("folder lookups = %d (%v), want %d", len(fd.folderQs), fd.folderQs, tt.wantFolderQ)
			}
			if fd.method != tt.wantMethod {
				t.Errorf("method = %s, want %s", fd.method, tt.wantMethod)
			}
			if tt.wantMethod == http.MethodPost {
				parents, _ := fd.meta["parents"].([]any)
				if len(parents) != 1 || parents[0] != tt.wantParent {
					t.Errorf("file parents = %v, want [%s]", fd.meta["parents"], tt.wantParent)
				}
				if fd.meta["name"] != "c.txt" {
					t.Errorf("file name = %v, want c.txt", fd.meta["name"])
				}
			}
		})
	}
}

func TestUploadFolderQueryEscaped(t *testing.T) {
	fd := &uploadDrive{}
	client, extra := setup(t, fd)

	if _, err := Upload(context.Background(), client, signedIn(t, "r"), cacheAt(t, "root-id"), nil, writeFile(t, 1), `it's\x/c.txt`, extra...); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if len(fd.folderQs) != 1 || !strings.Contains(fd.folderQs[0], `name = 'it\'s\\x'`) {
		t.Errorf("folder query not escaped: %v", fd.folderQs)
	}
}

func TestUploadRejectsBadRelPath(t *testing.T) {
	for _, rel := range []string{"", "/a.txt", "a/../b.txt", "../b.txt", "./a.txt", "a//b.txt", "a/", ".", ".."} {
		t.Run(rel, func(t *testing.T) {
			fd := &uploadDrive{}
			client, extra := setup(t, fd)

			_, err := Upload(context.Background(), client, signedIn(t, "r"), cacheAt(t, "root-id"), nil, writeFile(t, 1), rel, extra...)

			if err == nil || !strings.Contains(err.Error(), "invalid rel_path") {
				t.Fatalf("err = %v, want invalid rel_path", err)
			}
			fd.mu.Lock()
			defer fd.mu.Unlock()
			if len(fd.authz) != 0 {
				t.Errorf("made %d Drive requests before rejecting the path", len(fd.authz))
			}
		})
	}
}

// repoAt opens a folder cache in a temp dir.
func repoAt(t *testing.T) *store.Repo {
	t.Helper()
	r, err := store.OpenRepo(filepath.Join(t.TempDir(), "burrow.db"))
	if err != nil {
		t.Fatalf("OpenRepo: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func cachedFolder(t *testing.T, r *store.Repo, relDir string) string {
	t.Helper()
	id, err := r.GetFolder(context.Background(), relDir)
	if err != nil {
		t.Fatalf("GetFolder(%s): %v", relDir, err)
	}
	return id
}

func TestUploadFolderCache(t *testing.T) {
	ctx := context.Background()

	t.Run("a second file in the same directory makes no folder lookups", func(t *testing.T) {
		fd := &uploadDrive{}
		client, extra := setup(t, fd)
		cache := repoAt(t)

		for _, name := range []string{"a/b/one.txt", "a/b/two.txt"} {
			if _, err := Upload(ctx, client, signedIn(t, "r"), cacheAt(t, "root-id"), cache, writeFile(t, 5), name, extra...); err != nil {
				t.Fatalf("Upload %s: %v", name, err)
			}
		}

		fd.mu.Lock()
		defer fd.mu.Unlock()
		if len(fd.createdDirs) != 2 || len(fd.folderQs) != 2 {
			t.Errorf("creates/lookups = %d/%d, want 2/2 (all from the first file)", len(fd.createdDirs), len(fd.folderQs))
		}
		// Only the deepest cached folder is checked against Drive.
		if len(fd.dirGets) != 1 || fd.dirGets[0] != "dir-2" {
			t.Errorf("files.get calls = %v, want [dir-2]", fd.dirGets)
		}
		parents, _ := fd.meta["parents"].([]any)
		if len(parents) != 1 || parents[0] != "dir-2" {
			t.Errorf("second file parents = %v, want [dir-2]", fd.meta["parents"])
		}
		if cachedFolder(t, cache, "a") != "dir-1" || cachedFolder(t, cache, "a/b") != "dir-2" {
			t.Error("both levels should be cached")
		}
	})

	t.Run("a trashed cached folder is recreated and re-cached", func(t *testing.T) {
		fd := &uploadDrive{dirs: []fakeDir{{id: "A", name: "a", parent: "root-id", trashed: true}}}
		client, extra := setup(t, fd)
		cache := repoAt(t)
		if err := cache.PutFolder(ctx, "a", "A"); err != nil {
			t.Fatal(err)
		}

		if _, err := Upload(ctx, client, signedIn(t, "r"), cacheAt(t, "root-id"), cache, writeFile(t, 5), "a/c.txt", extra...); err != nil {
			t.Fatalf("Upload: %v", err)
		}

		fd.mu.Lock()
		defer fd.mu.Unlock()
		if len(fd.createdDirs) != 1 {
			t.Fatalf("created folders = %d, want 1", len(fd.createdDirs))
		}
		if got := cachedFolder(t, cache, "a"); got != "dir-1" {
			t.Errorf("cached id = %s, want the new folder dir-1", got)
		}
		parents, _ := fd.meta["parents"].([]any)
		if len(parents) != 1 || parents[0] != "dir-1" {
			t.Errorf("file parents = %v, want [dir-1]", fd.meta["parents"])
		}
	})

	t.Run("a cached folder that is gone is looked up again", func(t *testing.T) {
		fd := &uploadDrive{dirs: []fakeDir{{id: "A", name: "a", parent: "root-id"}}}
		client, extra := setup(t, fd)
		cache := repoAt(t)
		if err := cache.PutFolder(ctx, "a", "GONE"); err != nil { // files.get answers 404
			t.Fatal(err)
		}

		if _, err := Upload(ctx, client, signedIn(t, "r"), cacheAt(t, "root-id"), cache, writeFile(t, 5), "a/c.txt", extra...); err != nil {
			t.Fatalf("Upload: %v", err)
		}

		fd.mu.Lock()
		defer fd.mu.Unlock()
		if len(fd.createdDirs) != 0 {
			t.Errorf("created %d folders, want none (the folder exists)", len(fd.createdDirs))
		}
		if got := cachedFolder(t, cache, "a"); got != "A" {
			t.Errorf("cached id = %s, want A", got)
		}
	})

	t.Run("a lost cache re-finds the existing folders without duplicating them", func(t *testing.T) {
		fd := &uploadDrive{dirs: []fakeDir{
			{id: "A", name: "a", parent: "root-id"},
			{id: "B", name: "b", parent: "A"},
		}}
		client, extra := setup(t, fd)
		cache := repoAt(t) // empty, as after deleting burrow.db

		if _, err := Upload(ctx, client, signedIn(t, "r"), cacheAt(t, "root-id"), cache, writeFile(t, 5), "a/b/c.txt", extra...); err != nil {
			t.Fatalf("Upload: %v", err)
		}

		fd.mu.Lock()
		defer fd.mu.Unlock()
		if len(fd.createdDirs) != 0 {
			t.Errorf("created %d folders, want none", len(fd.createdDirs))
		}
		if cachedFolder(t, cache, "a") != "A" || cachedFolder(t, cache, "a/b") != "B" {
			t.Error("the found folders should be cached")
		}
	})

	t.Run("a closed cache never fails the upload", func(t *testing.T) {
		fd := &uploadDrive{}
		client, extra := setup(t, fd)
		cache := repoAt(t)
		if err := cache.Close(); err != nil {
			t.Fatal(err)
		}

		if _, err := Upload(ctx, client, signedIn(t, "r"), cacheAt(t, "root-id"), cache, writeFile(t, 5), "a/c.txt", extra...); err != nil {
			t.Fatalf("Upload with a closed cache: %v", err)
		}
	})
}

func TestUploaderBuildsTheServiceOnce(t *testing.T) {
	fd := &uploadDrive{}
	client, extra := setup(t, fd)
	u := NewUploader(client, signedIn(t, "r"), cacheAt(t, "root-id"), nil, extra...)

	for _, rel := range []string{"x/a.txt", "x/b.txt", "c.txt"} {
		if _, err := u.Upload(context.Background(), writeFile(t, 5), rel); err != nil {
			t.Fatalf("Upload %s: %v", rel, err)
		}
	}

	fd.mu.Lock()
	defer fd.mu.Unlock()
	if fd.tokenReqs != 1 {
		t.Errorf("token requests = %d, want 1 for three files", fd.tokenReqs)
	}
	if fd.rootGets != 1 {
		t.Errorf("MySync root checks = %d, want 1 for three files", fd.rootGets)
	}
}

func TestUploaderNotSignedIn(t *testing.T) {
	fd := &uploadDrive{}
	client, extra := setup(t, fd)
	u := NewUploader(client, signedIn(t, ""), cacheAt(t, "root-id"), nil, extra...)

	for i := 0; i < 2; i++ { // still reports it on a later call
		if _, err := u.Upload(context.Background(), writeFile(t, 5), "a.txt"); !errors.Is(err, ErrNotSignedIn) {
			t.Fatalf("call %d: err = %v, want ErrNotSignedIn", i, err)
		}
	}
}

func TestUploadCreateFailureIsAnError(t *testing.T) {
	fd := &uploadDrive{failCreate: http.StatusBadRequest}
	client, extra := setup(t, fd)

	for _, rel := range []string{"a.txt", "d/a.txt"} { // without and with a folder to create first
		_, err := Upload(context.Background(), client, signedIn(t, "r"), cacheAt(t, "root-id"), nil, writeFile(t, 5), rel, extra...)
		if err == nil || !strings.Contains(err.Error(), "upload "+rel) {
			t.Errorf("%s: err = %v, want an upload error", rel, err)
		}
	}
}

// sparseFile makes a file of the given size without writing its bytes.
func sparseFile(t *testing.T, size int64) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "big.bin")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

// A large upload is streamed in chunks: live heap while it runs stays within a
// few chunks of where it started, however big the file is.
func TestUploadLargeFileKeepsMemoryFlat(t *testing.T) {
	if testing.Short() {
		t.Skip("uploads 128 MB")
	}
	const size = 128 << 20
	var peak uint64
	var base uint64
	fd := &uploadDrive{}
	fd.onChunk = func(int) {
		// Collect first, so only live data counts, not garbage waiting for GC.
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		if m.HeapAlloc > peak {
			peak = m.HeapAlloc
		}
	}
	client, extra := setup(t, fd)
	path := sparseFile(t, size)
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	base = m.HeapAlloc

	if _, err := Upload(context.Background(), client, signedIn(t, "stored-refresh"), cacheAt(t, "root-id"), nil, path, "big.bin", extra...); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	fd.mu.Lock()
	defer fd.mu.Unlock()
	// 16 full chunks, then the empty request that finalizes an exact multiple.
	if fd.received != size || len(fd.ranges) != size/uploadChunkSize+1 {
		t.Errorf("received %d bytes in %d requests, want %d in %d", fd.received, len(fd.ranges), size, size/uploadChunkSize+1)
	}
	if fd.ranges[0] != "bytes 0-8388607/*" || fd.ranges[len(fd.ranges)-1] != "bytes */134217728" {
		t.Errorf("Content-Range sequence = %v", fd.ranges)
	}
	grew, limit := int64(peak)-int64(base), int64(3*uploadChunkSize)
	t.Logf("live heap grew by %d bytes (limit %d) uploading %d", grew, limit, size)
	if grew > limit {
		t.Errorf("live heap grew by %d bytes during the upload, want at most %d (file is %d)", grew, limit, size)
	}
}

// Cancelling during a multi-chunk upload stops it: the call returns the
// context error and no further chunks are sent.
func TestUploadCancelledBetweenChunks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fd := &uploadDrive{}
	fd.onChunk = func(chunk int) {
		if chunk == 2 {
			cancel()
		}
	}
	client, extra := setup(t, fd)
	path := sparseFile(t, 5*uploadChunkSize)

	start := time.Now()
	_, err := Upload(ctx, client, signedIn(t, "stored-refresh"), cacheAt(t, "root-id"), nil, path, "big.bin", extra...)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v to stop", d)
	}
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if len(fd.ranges) > 3 {
		t.Errorf("%d chunks sent, want the upload to stop right after the cancel", len(fd.ranges))
	}
}
