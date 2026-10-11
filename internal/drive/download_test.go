package drive

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"
)

// mediaDrive serves f1's content on files.get?alt=media, failing the first
// `fail` requests with 503 and answering 404 for any other ID.
type mediaDrive struct {
	mu      sync.Mutex
	content string
	fail    int
	gets    int
}

func (d *mediaDrive) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case r.URL.Path == "/token":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"a","token_type":"Bearer","expires_in":3600}`))
	case r.URL.Path == "/files/root-id":
		_, _ = w.Write([]byte(`{"id":"root-id","trashed":false}`))
	case r.URL.Path == "/files/f1" && r.URL.Query().Get("alt") == "media":
		d.gets++
		if d.fail > 0 {
			d.fail--
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(d.content))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"File not found"}}`))
	}
}

func sumOf(s string) string { h := md5.Sum([]byte(s)); return hex.EncodeToString(h[:]) }

func downloadUploader(t *testing.T, d *mediaDrive) *Uploader {
	t.Helper()
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	client := Client{ID: "id", Secret: "secret", Endpoint: &oauth2.Endpoint{TokenURL: srv.URL + "/token"}}
	return NewUploader(client, signedIn(t, "r"), cacheAt(t, "root-id"), nil, option.WithEndpoint(srv.URL))
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range des {
		names = append(names, e.Name())
	}
	return names
}

func TestDownloadWritesVerifiedFileAtomically(t *testing.T) {
	d := &mediaDrive{content: "hello from drive"}
	u := downloadUploader(t, d)
	dst := filepath.Join(t.TempDir(), "docs", "sub", "a.txt") // parents do not exist yet

	if err := u.Download(context.Background(), "f1", sumOf(d.content), dst); err != nil {
		t.Fatal(err)
	}

	if b, _ := os.ReadFile(dst); string(b) != d.content {
		t.Errorf("content = %q", b)
	}
	if st, _ := os.Stat(dst); st.Mode().Perm() != 0o644 && filepath.Separator == '/' {
		t.Errorf("mode = %v, want 0644", st.Mode().Perm())
	}
	if got := entries(t, filepath.Dir(dst)); len(got) != 1 || got[0] != "a.txt" {
		t.Errorf("directory holds %v, want only a.txt (no temp file left)", got)
	}
}

func TestDownloadMismatchLeavesTheTargetAndNoTempFile(t *testing.T) {
	d := &mediaDrive{content: "new content"}
	u := downloadUploader(t, d)
	dir := t.TempDir()
	dst := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(dst, []byte("old content"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := u.Download(context.Background(), "f1", sumOf("something else"), dst)

	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("err = %v, want ErrChecksumMismatch", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "old content" {
		t.Errorf("target = %q, want the old content untouched", b)
	}
	if got := entries(t, dir); len(got) != 1 {
		t.Errorf("directory holds %v, want only a.txt", got)
	}
}

func TestDownloadReplacesAnExistingFile(t *testing.T) {
	d := &mediaDrive{content: "v2"}
	u := downloadUploader(t, d)
	dst := filepath.Join(t.TempDir(), "a.txt")
	_ = os.WriteFile(dst, []byte("v1"), 0o644)

	if err := u.Download(context.Background(), "f1", sumOf("v2"), dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "v2" {
		t.Errorf("target = %q", b)
	}
}

func TestDownloadUnknownFileAndMissingMD5(t *testing.T) {
	u := downloadUploader(t, &mediaDrive{})
	dir := t.TempDir()
	if err := u.Download(context.Background(), "nope", "abc", filepath.Join(dir, "a")); !errors.Is(err, ErrFileNotFound) {
		t.Errorf("unknown: err = %v", err)
	}
	if err := u.Download(context.Background(), "f1", "", filepath.Join(dir, "a")); err == nil || !strings.Contains(err.Error(), "no MD5") {
		t.Errorf("no md5: err = %v", err)
	}
	if got := entries(t, dir); len(got) != 0 {
		t.Errorf("directory holds %v", got)
	}
}

func TestDownloadRetriesATransientError(t *testing.T) {
	d := &mediaDrive{content: "ok", fail: 2}
	u := downloadUploader(t, d)
	sleeps := stubSleep(t)
	dst := filepath.Join(t.TempDir(), "a.txt")

	if err := u.Download(context.Background(), "f1", sumOf("ok"), dst); err != nil {
		t.Fatal(err)
	}
	if *sleeps != 2 || d.gets != 3 {
		t.Errorf("sleeps = %d, gets = %d; want two retries then success", *sleeps, d.gets)
	}
}
