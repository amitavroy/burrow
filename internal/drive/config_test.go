package drive

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"
)

// configDrive fakes the routes the settings file touches: the root check, the
// tag lookup, the download, and create/update through upload.
type configDrive struct {
	mu      sync.Mutex
	content string // "" = no file yet
	exists  bool
	props   map[string]string
	parents []string
	creates int
	updates int
	listQ   string
}

func (c *configDrive) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	path := r.URL.Path
	switch {
	case path == "/token":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"a","token_type":"Bearer","expires_in":3600}`))
	case strings.HasSuffix(path, "/files/root-id"):
		_, _ = w.Write([]byte(`{"id":"root-id","trashed":false}`))
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/files"):
		c.listQ = r.URL.Query().Get("q")
		files := []map[string]any{}
		if c.exists {
			files = append(files, map[string]any{"id": "cfg-1"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"files": files})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/files/cfg-1"):
		if r.URL.Query().Get("alt") != "media" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(c.content))
	case strings.HasPrefix(path, "/upload/"):
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		meta, _ := mr.NextPart()
		var m struct {
			Name          string            `json:"name"`
			Parents       []string          `json:"parents"`
			AppProperties map[string]string `json:"appProperties"`
		}
		_ = json.NewDecoder(meta).Decode(&m)
		part, _ := mr.NextPart()
		data, _ := io.ReadAll(part)
		c.content, c.exists = string(data), true
		if r.Method == http.MethodPatch {
			c.updates++
		} else {
			c.creates++
			c.props, c.parents = m.AppProperties, m.Parents
		}
		_, _ = w.Write([]byte(`{"id":"cfg-1"}`))
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func configUploader(t *testing.T, c *configDrive) *Uploader {
	t.Helper()
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	client := Client{ID: "id", Secret: "secret", Endpoint: &oauth2.Endpoint{TokenURL: srv.URL + "/token"}}
	return NewUploader(client, signedIn(t, "r"), cacheAt(t, "root-id"), nil, option.WithEndpoint(srv.URL))
}

func TestConfigRoundTrip(t *testing.T) {
	ctx := context.Background()
	c := &configDrive{}
	u := configUploader(t, c)

	got, err := u.ReadConfig(ctx)
	if err != nil || got.Version != ConfigVersion || len(got.Ignore) != 0 || c.creates != 0 {
		t.Fatalf("empty read = %+v, %v (creates %d); want the empty config and nothing created", got, err, c.creates)
	}

	if err := u.WriteConfig(ctx, Config{Ignore: []string{"*.log", "build/"}}); err != nil {
		t.Fatal(err)
	}
	if c.creates != 1 || c.props[tagRole] != roleConfig || !reflect.DeepEqual(c.parents, []string{"root-id"}) {
		t.Errorf("created %d, props %v, parents %v", c.creates, c.props, c.parents)
	}
	if !strings.Contains(c.listQ, tagRole) || !strings.Contains(c.listQ, "'root-id' in parents") {
		t.Errorf("lookup query = %q", c.listQ)
	}

	got, err = u.ReadConfig(ctx)
	if err != nil || !reflect.DeepEqual(got, Config{Version: ConfigVersion, Ignore: []string{"*.log", "build/"}}) {
		t.Fatalf("read back = %+v, %v", got, err)
	}

	if err := u.WriteConfig(ctx, Config{Ignore: []string{"*.log"}}); err != nil {
		t.Fatal(err)
	}
	if c.creates != 1 || c.updates != 1 {
		t.Errorf("second write: creates %d, updates %d; want the same file updated", c.creates, c.updates)
	}
}

func TestReadConfigRejectsANewerVersionAndGarbage(t *testing.T) {
	ctx := context.Background()
	c := &configDrive{exists: true, content: `{"version": 99, "ignore": []}`}
	u := configUploader(t, c)
	if _, err := u.ReadConfig(ctx); !errors.Is(err, ErrConfigTooNew) {
		t.Errorf("newer version: err = %v", err)
	}
	c.content = `not json`
	if _, err := u.ReadConfig(ctx); err == nil {
		t.Error("garbage: want an error")
	}
}
