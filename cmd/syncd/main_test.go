package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/amitavroy/burrow/internal/drive"
	"github.com/amitavroy/burrow/internal/store"
	burrowsync "github.com/amitavroy/burrow/internal/sync"
	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "version prints Version",
			args:       []string{"version"},
			wantCode:   0,
			wantStdout: Version + "\n",
		},
		{
			name:       "no arguments prints usage",
			args:       nil,
			wantCode:   2,
			wantStderr: "usage: syncd <command>",
		},
		{
			name:       "login without client credentials fails",
			args:       []string{"login"},
			wantCode:   1,
			wantStderr: "BURROW_GOOGLE_CLIENT_ID",
		},
		{
			name:       "unknown command is an error",
			args:       []string{"bogus"},
			wantCode:   2,
			wantStderr: `unknown command "bogus"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BURROW_GOOGLE_CLIENT_ID", "")
			var stdout, stderr bytes.Buffer

			code := run(tt.args, &stdout, &stderr)

			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if stdout.String() != tt.wantStdout {
				t.Errorf("stdout = %q, want %q", stdout.String(), tt.wantStdout)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestPersistToken(t *testing.T) {
	tests := []struct {
		name    string
		tok     *oauth2.Token
		want    string
		wantErr string
	}{
		{name: "stores refresh token only", tok: &oauth2.Token{AccessToken: "access", RefreshToken: "refresh"}, want: "refresh"},
		{name: "no refresh token", tok: &oauth2.Token{AccessToken: "access"}, wantErr: "no refresh token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keyring.MockInit()
			store := drive.KeyringStore{}

			err := persistToken(store, tt.tok)

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				if _, err := store.Load(); !errors.Is(err, drive.ErrNotSignedIn) {
					t.Errorf("Load after failed save: err = %v, want ErrNotSignedIn", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("persistToken: %v", err)
			}
			got, err := store.Load()
			if err != nil || got != tt.want {
				t.Errorf("Load = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestLogout(t *testing.T) {
	keyring.MockInit()
	t.Setenv("BURROW_GOOGLE_CLIENT_ID", "id")
	t.Setenv("BURROW_GOOGLE_CLIENT_SECRET", "secret")
	store := drive.KeyringStore{}

	// Stand in for a completed login.
	if err := store.Save("refresh"); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"logout"}, &stdout, &stderr); code != 0 {
		t.Fatalf("logout exit code = %d, stderr = %q", code, stderr.String())
	}
	if stdout.String() != "Signed out\n" {
		t.Errorf("stdout = %q", stdout.String())
	}
	if _, err := store.Load(); !errors.Is(err, drive.ErrNotSignedIn) {
		t.Errorf("token still stored after logout: err = %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"whoami"}, &stdout, &stderr); code != 1 {
		t.Errorf("whoami after logout exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "run `syncd login`") {
		t.Errorf("stderr = %q, want the sign-in hint", stderr.String())
	}

	// Idempotent: logging out again is not an error.
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"logout"}, &stdout, &stderr); code != 0 {
		t.Errorf("second logout exit code = %d, stderr = %q", code, stderr.String())
	}
}

func TestRelPathFor(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name    string
		file    string
		root    string
		want    string
		wantErr bool
	}{
		{name: "no root uses the basename", file: "/some/where/notes.txt", want: "notes.txt"},
		{name: "file in root", file: filepath.Join(root, "notes.txt"), root: root, want: "notes.txt"},
		{name: "nested file uses slashes", file: filepath.Join(root, "a", "b", "notes.txt"), root: root, want: "a/b/notes.txt"},
		{name: "file outside root", file: filepath.Join(filepath.Dir(root), "other.txt"), root: root, wantErr: true},
		{name: "dot-dot escape", file: filepath.Join(root, "..", "other.txt"), root: root, wantErr: true},
		{name: "name starting with dots is fine", file: filepath.Join(root, "..hidden"), root: root, want: "..hidden"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := relPathFor(tt.file, tt.root)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("rel = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPut(t *testing.T) {
	dir := t.TempDir()
	oldDB := dbPath
	dbPath = func() string { return filepath.Join(dir, "state", "burrow.db") }
	t.Cleanup(func() { dbPath = oldDB })
	file := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		args       []string
		signedIn   bool
		wantCode   int
		wantStderr string
	}{
		{name: "no file is a usage error", args: []string{"put"}, wantCode: 2, wantStderr: "usage: syncd put"},
		{name: "two files is a usage error", args: []string{"put", file, file}, wantCode: 2, wantStderr: "usage: syncd put"},
		{name: "unknown flag", args: []string{"put", "--bogus", file}, wantCode: 2, wantStderr: "flag provided but not defined"},
		{name: "--watch is gone", args: []string{"put", "--watch", "demo", file}, wantCode: 2, wantStderr: "flag provided but not defined: -watch"},
		{name: "file outside root", args: []string{"put", "--root", filepath.Join(dir, "sub"), file}, wantCode: 2, wantStderr: "outside the root"},
		{name: "not signed in gives the login hint", args: []string{"put", file}, wantCode: 1, wantStderr: "run `syncd login`"},
		{name: "missing file exits 1", args: []string{"put", filepath.Join(dir, "nope.txt")}, signedIn: true, wantCode: 1, wantStderr: "put failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keyring.MockInit()
			t.Setenv("BURROW_GOOGLE_CLIENT_ID", "id")
			t.Setenv("BURROW_GOOGLE_CLIENT_SECRET", "secret")
			if tt.signedIn {
				if err := (drive.KeyringStore{}).Save("secret-refresh-token"); err != nil {
					t.Fatal(err)
				}
			}
			var stdout, stderr bytes.Buffer

			code := run(tt.args, &stdout, &stderr)

			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tt.wantCode, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty", stdout.String())
			}
			if strings.Contains(stderr.String(), "secret-refresh-token") {
				t.Errorf("stderr leaks the token: %q", stderr.String())
			}
		})
	}
}

func TestStat(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStderr string
	}{
		{name: "no argument is a usage error", args: []string{"stat"}, wantCode: 2, wantStderr: "usage: syncd stat"},
		{name: "two arguments is a usage error", args: []string{"stat", "a", "b"}, wantCode: 2, wantStderr: "usage: syncd stat"},
		{name: "--path without rel_path is a usage error", args: []string{"stat", "--path"}, wantCode: 2, wantStderr: "usage: syncd stat"},
		{name: "--watch is gone", args: []string{"stat", "--watch", "demo", "notes.txt"}, wantCode: 2, wantStderr: "flag provided but not defined: -watch"},
		{name: "unknown flag", args: []string{"stat", "--bogus", "id"}, wantCode: 2, wantStderr: "flag provided but not defined"},
		{name: "by ID, not signed in", args: []string{"stat", "file-id"}, wantCode: 1, wantStderr: "run `syncd login`"},
		{name: "by rel_path, not signed in", args: []string{"stat", "--path", "notes.txt"}, wantCode: 1, wantStderr: "run `syncd login`"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keyring.MockInit()
			t.Setenv("BURROW_GOOGLE_CLIENT_ID", "id")
			t.Setenv("BURROW_GOOGLE_CLIENT_SECRET", "secret")
			var stdout, stderr bytes.Buffer

			code := run(tt.args, &stdout, &stderr)

			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tt.wantCode, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty", stdout.String())
			}
		})
	}
}

func TestDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "burrow.db")
	orig := dbPath
	dbPath = func() string { return path }
	t.Cleanup(func() { dbPath = orig })

	t.Run("path prints the location and creates nothing", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := run([]string{"db", "path"}, &stdout, &stderr); code != 0 {
			t.Fatalf("code = %d, stderr = %q", code, stderr.String())
		}
		if got := strings.TrimSpace(stdout.String()); got != path {
			t.Errorf("stdout = %q, want %q", got, path)
		}
		if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
			t.Errorf("db path created %s (err = %v)", filepath.Dir(path), err)
		}
	})

	t.Run("status migrates and lists tables, twice", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			var stdout, stderr bytes.Buffer
			if code := run([]string{"db", "status"}, &stdout, &stderr); code != 0 {
				t.Fatalf("run #%d: code = %d, stderr = %q", i+1, code, stderr.String())
			}
			for _, want := range []string{"Path:    " + path, "Version: 6", "files", "folders", "jobs", "sync_errors", "goose_db_version"} {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("run #%d: stdout %q missing %q", i+1, stdout.String(), want)
				}
			}
		}
	})

	for name, args := range map[string][]string{
		"missing subcommand": {"db"},
		"unknown subcommand": {"db", "nope"},
		"extra argument":     {"db", "path", "x"},
	} {
		t.Run(name+" prints usage", func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(args, &stdout, &stderr); code != 2 {
				t.Errorf("code = %d, want 2", code)
			}
			if !strings.Contains(stderr.String(), "usage: syncd db") {
				t.Errorf("stderr = %q, want usage", stderr.String())
			}
		})
	}
}

func TestScan(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"b.txt", "a/c.txt"} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "MySync"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "MySync", "h.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := homeDir
	homeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { homeDir = old })

	file := filepath.Join(root, "b.txt")
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{"lists files sorted", []string{"scan", "--dry-run", "--root", root}, 0, "a/c.txt\nb.txt\n", "2 files, 0 ignored"},
		{"default root is ~/MySync", []string{"scan", "--dry-run"}, 0, "h.txt\n", "1 files, 0 ignored"},
		{"needs --dry-run", []string{"scan", "--root", root}, 2, "", "usage: syncd scan"},
		{"missing root", []string{"scan", "--dry-run", "--root", filepath.Join(root, "nope")}, 1, "", "scan failed"},
		{"file as root", []string{"scan", "--dry-run", "--root", file}, 1, "", "not a directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tt.wantCode, stderr.String())
			}
			if stdout.String() != tt.wantStdout {
				t.Errorf("stdout = %q, want %q", stdout.String(), tt.wantStdout)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestScanReportsIgnoredAndUnreadable(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"ok.txt", "x.tmp", "node_modules/i.js", "locked/s.txt"} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	locked := filepath.Join(root, "locked")
	canLock := runtime.GOOS != "windows" && os.Geteuid() != 0
	if canLock {
		if err := os.Chmod(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(locked, 0o755) })
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"scan", "--dry-run", "--root", root}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	want := "locked/s.txt\nok.txt\n"
	wantCount := "2 files, 2 ignored"
	if canLock {
		want = "ok.txt\n"
		wantCount = "1 files, 2 ignored"
		if !strings.Contains(stderr.String(), "skipped:") || !strings.Contains(stderr.String(), "locked") {
			t.Errorf("stderr = %q, want a skipped line naming locked", stderr.String())
		}
	}
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if !strings.Contains(stderr.String(), wantCount) {
		t.Errorf("stderr = %q, want it to contain %q", stderr.String(), wantCount)
	}
}

func TestPutWithoutStateDB(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A regular file where the database directory should be, so it cannot open.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	oldDB := dbPath
	dbPath = func() string { return filepath.Join(blocker, "burrow.db") }
	t.Cleanup(func() { dbPath = oldDB })
	keyring.MockInit()
	t.Setenv("BURROW_GOOGLE_CLIENT_ID", "id")
	t.Setenv("BURROW_GOOGLE_CLIENT_SECRET", "secret")
	var stdout, stderr bytes.Buffer

	code := run([]string{"put", file}, &stdout, &stderr)

	// The folder cache is optional: put warns and goes on to the sign-in check.
	if code != 1 || !strings.Contains(stderr.String(), "continuing without the folder cache") || !strings.Contains(stderr.String(), "run `syncd login`") {
		t.Errorf("code = %d, stderr = %q", code, stderr.String())
	}
}

func TestSync(t *testing.T) {
	root := t.TempDir()
	oldDB := dbPath
	stateDir := t.TempDir()
	dbPath = func() string { return filepath.Join(stateDir, "burrow.db") }
	t.Cleanup(func() { dbPath = oldDB })
	for _, rel := range []string{"a.txt", "d/b.txt", "skip.tmp"} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(root, "a.txt")
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStderr string
	}{
		{name: "extra argument is a usage error", args: []string{"sync", "x"}, wantCode: 2, wantStderr: "usage: syncd sync"},
		{name: "run extra argument", args: []string{"run", "x"}, wantCode: 2, wantStderr: "usage: syncd run"},
		{name: "run zero workers", args: []string{"run", "--workers", "0"}, wantCode: 2, wantStderr: "usage: syncd run"},
		{name: "run bad settle", args: []string{"run", "--settle", "0s"}, wantCode: 2, wantStderr: "usage: syncd run"},
		{name: "zero workers is a usage error", args: []string{"sync", "--workers", "0"}, wantCode: 2, wantStderr: "usage: syncd sync"},
		{name: "too many workers is a usage error", args: []string{"sync", "--workers", "17"}, wantCode: 2, wantStderr: "usage: syncd sync"},
		{name: "unknown flag", args: []string{"sync", "--bogus"}, wantCode: 2, wantStderr: "flag provided but not defined"},
		{name: "missing root exits 1", args: []string{"sync", "--root", filepath.Join(root, "nope")}, wantCode: 1, wantStderr: "sync failed"},
		{name: "a file as the root exits 1", args: []string{"sync", "--root", file}, wantCode: 1, wantStderr: "not a directory"},
		{name: "signed out gives the login hint", args: []string{"sync", "--root", root}, wantCode: 1, wantStderr: "run `syncd login`"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keyring.MockInit()
			t.Setenv("BURROW_GOOGLE_CLIENT_ID", "id")
			t.Setenv("BURROW_GOOGLE_CLIENT_SECRET", "secret")
			var stdout, stderr bytes.Buffer

			code := run(tt.args, &stdout, &stderr)

			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tt.wantCode, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
			if strings.Contains(stdout.String(), "uploaded") {
				t.Errorf("stdout = %q, nothing should have uploaded", stdout.String())
			}
		})
	}
}

func TestSyncDefaultRootIsMySync(t *testing.T) {
	home := t.TempDir() // no MySync inside
	oldDB := dbPath
	dbPath = func() string { return filepath.Join(home, "state", "burrow.db") }
	t.Cleanup(func() { dbPath = oldDB })
	old := homeDir
	homeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { homeDir = old })
	keyring.MockInit()
	t.Setenv("BURROW_GOOGLE_CLIENT_ID", "id")
	t.Setenv("BURROW_GOOGLE_CLIENT_SECRET", "secret")
	var stdout, stderr bytes.Buffer

	code := run([]string{"sync"}, &stdout, &stderr)

	if code != 1 || !strings.Contains(stderr.String(), "MySync") {
		t.Errorf("code = %d, stderr = %q; want exit 1 naming the default root", code, stderr.String())
	}
}

func TestSyncNeedsTheStateDatabase(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "tree")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(dir, "blocker") // a file where the database directory should be
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	oldDB := dbPath
	dbPath = func() string { return filepath.Join(blocker, "burrow.db") }
	t.Cleanup(func() { dbPath = oldDB })
	keyring.MockInit()
	t.Setenv("BURROW_GOOGLE_CLIENT_ID", "id")
	t.Setenv("BURROW_GOOGLE_CLIENT_SECRET", "secret")
	var stdout, stderr bytes.Buffer

	code := run([]string{"sync", "--root", root}, &stdout, &stderr)

	if code != 1 || !strings.Contains(stderr.String(), "sync failed") {
		t.Errorf("code = %d, stderr = %q; want exit 1 because the database cannot open", code, stderr.String())
	}
}

func TestHistory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "burrow.db")
	oldDB := dbPath
	dbPath = func() string { return path }
	t.Cleanup(func() { dbPath = oldDB })

	repo, err := store.OpenRepo(path)
	if err != nil {
		t.Fatal(err)
	}
	f := store.File{RelPath: "a/b.txt", DriveFileID: "id-1"}
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for i, rev := range []string{"r1", "r2"} {
		err := repo.RecordUpload(context.Background(), f, store.Revision{
			DriveFileID: "id-1", RevisionID: rev, MD5: "m-" + rev, Size: int64(10 + i),
			Time: at.Add(time.Duration(i) * time.Hour).UnixNano(), Source: "upload",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	repo.Close()

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "newest first", args: []string{"history", "a/b.txt"}, wantCode: 0,
			wantStdout: "2026-10-09T13:00:00Z  r2  m-r2  11  upload\n2026-10-09T12:00:00Z  r1  m-r1  10  upload\n"},
		{name: "unknown path", args: []string{"history", "nope.txt"}, wantCode: 1, wantStderr: "no revisions recorded"},
		{name: "no argument is a usage error", args: []string{"history"}, wantCode: 2, wantStderr: "usage: syncd history"},
		{name: "two arguments is a usage error", args: []string{"history", "a", "b"}, wantCode: 2, wantStderr: "usage: syncd history"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tt.args, &stdout, &stderr)
			if code != tt.wantCode || stdout.String() != tt.wantStdout || !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestErrorsCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "burrow.db")
	oldDB := dbPath
	dbPath = func() string { return path }
	t.Cleanup(func() { dbPath = oldDB })

	var stdout, stderr bytes.Buffer
	if code := run([]string{"errors"}, &stdout, &stderr); code != 0 || stdout.String() != "no errors\n" {
		t.Fatalf("empty: code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}

	repo, err := store.OpenRepo(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = repo.RecordError(context.Background(), "docs/a.pdf", "Drive storage quota exceeded", "raw")
	repo.Close()

	stdout.Reset()
	code := run([]string{"errors"}, &stdout, &stderr)
	re := regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ  docs/a\.pdf  Drive storage quota exceeded, will retry\n$`)
	if code != 0 || !re.MatchString(stdout.String()) {
		t.Errorf("code = %d, stdout = %q", code, stdout.String())
	}

	stderr.Reset()
	if code := run([]string{"errors", "x"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "usage: syncd errors") {
		t.Errorf("extra arg: code = %d, stderr = %q", code, stderr.String())
	}
}

func TestFormatWatchEvent(t *testing.T) {
	for _, tt := range []struct {
		e    burrowsync.WatchEvent
		want string
	}{
		{burrowsync.WatchEvent{RelPath: "a/b.txt", Op: burrowsync.OpCreate}, "create a/b.txt"},
		{burrowsync.WatchEvent{RelPath: "old", Op: burrowsync.OpRemove, IsDir: true}, "remove old/"},
		{burrowsync.WatchEvent{RelPath: "n.txt", Op: burrowsync.OpWrite}, "write n.txt"},
		{burrowsync.WatchEvent{RelPath: "n.txt", Op: burrowsync.OpRename}, "rename n.txt"},
	} {
		if got := formatWatchEvent(tt.e); got != tt.want {
			t.Errorf("got %q, want %q", got, tt.want)
		}
	}
}

func TestWatchCommandErrors(t *testing.T) {
	for _, tt := range []struct {
		name     string
		args     []string
		wantCode int
		wantErr  string
	}{
		{"extra argument", []string{"watch", "x"}, 2, "usage: syncd watch"},
		{"zero settle", []string{"watch", "--settle", "0s"}, 2, "usage: syncd watch"},
		{"bad settle", []string{"watch", "--settle", "soon"}, 2, "invalid value"},
		{"missing root", []string{"watch", "--root", filepath.Join(t.TempDir(), "nope")}, 1, "watch failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(tt.args, &stdout, &stderr); code != tt.wantCode || !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("code = %d, stderr = %q", code, stderr.String())
			}
		})
	}
}

func TestScanDiff(t *testing.T) {
	root := t.TempDir()
	dbFile := filepath.Join(t.TempDir(), "burrow.db")
	oldDB := dbPath
	dbPath = func() string { return dbFile }
	t.Cleanup(func() { dbPath = oldDB })
	mk := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("same.txt", "same")
	mk("edit.txt", "v1")
	repo, err := store.OpenRepo(dbFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"same.txt", "edit.txt"} {
		st, _ := os.Stat(filepath.Join(root, rel))
		if err := repo.Upsert(context.Background(), store.File{RelPath: rel, DriveFileID: "id-" + rel, Size: st.Size(), MTime: st.ModTime().UnixNano()}); err != nil {
			t.Fatal(err)
		}
	}
	_ = repo.Upsert(context.Background(), store.File{RelPath: "gone.txt", DriveFileID: "id2", Size: 1, MTime: 1})
	repo.Close()
	mk("edit.txt", "v2, longer")
	mk("b/new.txt", "n")
	mk("a.tmp", "ignored")

	var stdout, stderr bytes.Buffer
	code := run([]string{"scan", "--dry-run", "--diff", "--root", root}, &stdout, &stderr)

	if code != 0 || stdout.String() != "new b/new.txt\nchanged edit.txt\nmissing gone.txt\n" {
		t.Errorf("code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "1 new, 1 changed, 0 moved, 1 unchanged, 1 missing, 1 ignored") {
		t.Errorf("stderr = %q", stderr.String())
	}

	// Looking changed nothing: no jobs, and plain --dry-run still works without the database.
	repo, _ = store.OpenRepo(dbFile)
	defer repo.Close()
	if n, _ := repo.CountJobs(context.Background()); n != 0 {
		t.Errorf("--diff queued %d jobs", n)
	}
	stderr.Reset()
	if code := run([]string{"scan", "--diff", "--root", root}, &stdout, &stderr); code != 2 {
		t.Errorf("--diff without --dry-run: code = %d, want 2", code)
	}
}

func TestReportReconcile(t *testing.T) {
	var out bytes.Buffer
	p := burrowsync.Plan{New: []string{"a"}, Changed: []string{"b", "c"}, Unchanged: 7}
	for i := range 12 {
		p.Missing = append(p.Missing, fmt.Sprintf("m%02d", i))
	}
	reportReconcile(&out, p)
	got := out.String()
	if !strings.HasPrefix(got, "reconciled: 1 new, 2 changed, 0 moved, 7 unchanged, 12 missing locally\n") ||
		!strings.Contains(got, "  missing: m09\n") || strings.Contains(got, "m10") || !strings.Contains(got, "  ...and 2 more\n") {
		t.Errorf("output = %q", got)
	}
}

func TestGuardedTurnsAPanicIntoExit1(t *testing.T) {
	var stderr bytes.Buffer
	if code := guarded(&stderr, func() int { panic("kaboom") }); code != 1 || !strings.Contains(stderr.String(), "internal error: kaboom") {
		t.Errorf("code = %d, stderr = %q", code, stderr.String())
	}
	if code := guarded(&stderr, func() int { return 3 }); code != 3 {
		t.Errorf("a normal return must pass through, got %d", code)
	}
}

// run and sync refuse to start while another syncd holds the database lock,
// before they touch Drive.
func TestRunAndSyncRefuseWhenTheDatabaseIsLocked(t *testing.T) {
	dbFile := filepath.Join(t.TempDir(), "burrow.db")
	oldDB := dbPath
	dbPath = func() string { return dbFile }
	t.Cleanup(func() { dbPath = oldDB })
	t.Setenv("BURROW_GOOGLE_CLIENT_ID", "id")
	t.Setenv("BURROW_GOOGLE_CLIENT_SECRET", "secret")
	held, err := store.Lock(dbFile)
	if err != nil {
		t.Fatal(err)
	}

	for _, cmd := range []string{"run", "sync"} {
		var stdout, stderr bytes.Buffer
		code := run([]string{cmd, "--root", t.TempDir()}, &stdout, &stderr)
		if code != 1 || !strings.Contains(stderr.String(), "another syncd is using this database") {
			t.Errorf("%s: code = %d, stderr = %q", cmd, code, stderr.String())
		}
	}

	// Read-only commands are not blocked.
	var stdout, stderr bytes.Buffer
	if code := run([]string{"errors"}, &stdout, &stderr); code != 0 {
		t.Errorf("errors while locked: code = %d, stderr = %q", code, stderr.String())
	}
	held.Unlock()
}

func TestStatLocal(t *testing.T) {
	dbFile := filepath.Join(t.TempDir(), "burrow.db")
	oldDB := dbPath
	dbPath = func() string { return dbFile }
	t.Cleanup(func() { dbPath = oldDB })
	repo, err := store.OpenRepo(dbFile)
	if err != nil {
		t.Fatal(err)
	}
	mt := time.Date(2026, 10, 11, 10, 2, 11, 123456789, time.UTC).UnixNano()
	_ = repo.Upsert(context.Background(), store.File{RelPath: "notes/todo.txt", DriveFileID: "drv-1", Size: 2048, MTime: mt, Inode: 1048713, SyncedMD5: "abc"})
	_ = repo.Upsert(context.Background(), store.File{RelPath: "old.txt", DriveFileID: "drv-2", Size: 1, MTime: mt}) // row from before inodes
	repo.Close()

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout []string
		wantStderr string
	}{
		{"by path", []string{"stat", "--local", "--path", "notes/todo.txt"}, 0,
			[]string{"Local:\n", "  Inode:      1048713\n", "  Size:       2048\n", "  MTime:      2026-10-11T10:02:11.123456789Z\n", "  Synced MD5: abc\n"}, ""},
		{"by Drive ID", []string{"stat", "--local", "drv-1"}, 0, []string{"Inode:      1048713"}, ""},
		{"unknown inode", []string{"stat", "--local", "--path", "old.txt"}, 0, []string{"Inode:      unknown"}, ""},
		{"not tracked", []string{"stat", "--local", "--path", "nope.txt"}, 1, nil, "no tracked file"},
		{"no argument", []string{"stat", "--local"}, 2, nil, "usage: syncd stat"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tt.args, &stdout, &stderr)
			if code != tt.wantCode || !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Fatalf("code = %d, stderr = %q", code, stderr.String())
			}
			for _, want := range tt.wantStdout {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("stdout missing %q:\n%s", want, stdout.String())
				}
			}
		})
	}
}

func TestScanDiffShowsLikelyMoves(t *testing.T) {
	root := t.TempDir()
	dbFile := filepath.Join(t.TempDir(), "burrow.db")
	oldDB := dbPath
	dbPath = func() string { return dbFile }
	t.Cleanup(func() { dbPath = oldDB })
	if err := os.WriteFile(filepath.Join(root, "new-name.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(root, "new-name.txt"))
	if err != nil {
		t.Fatal(err)
	}
	ino := inodeForTest(st)
	if ino == 0 {
		t.Skip("no inodes on this platform")
	}
	repo, err := store.OpenRepo(dbFile)
	if err != nil {
		t.Fatal(err)
	}
	_ = repo.Upsert(context.Background(), store.File{RelPath: "old-name.txt", DriveFileID: "d1", Size: st.Size(), MTime: 1, Inode: ino})
	repo.Close()

	var stdout, stderr bytes.Buffer
	code := run([]string{"scan", "--dry-run", "--diff", "--root", root}, &stdout, &stderr)

	if code != 0 || stdout.String() != "moved old-name.txt -> new-name.txt\n" {
		t.Errorf("code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "0 new, 0 changed, 1 moved, 0 unchanged, 0 missing") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestConfigCommandUsage(t *testing.T) {
	for _, args := range [][]string{
		{"config"},
		{"config", "show", "x"},
		{"config", "ignore"},
		{"config", "ignore", "add"},
		{"config", "ignore", "list", "*.log"},
		{"config", "ignore", "add", "a", "b"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "usage: syncd config") {
			t.Errorf("%v: code = %d, stderr = %q", args, code, stderr.String())
		}
	}
}

func TestEditIgnore(t *testing.T) {
	base := drive.Config{Version: 1, Ignore: []string{"*.log"}}
	add, changed, err := editIgnore(base, "add", " build/ ")
	if err != nil || !changed || strings.Join(add.Ignore, ",") != "*.log,build/" {
		t.Fatalf("add = %+v, %v, %v", add, changed, err)
	}
	if len(base.Ignore) != 1 {
		t.Error("the input config was modified")
	}
	if _, changed, err := editIgnore(add, "add", "*.log"); err != nil || changed {
		t.Errorf("duplicate add: changed = %v, err = %v", changed, err)
	}
	rm, changed, err := editIgnore(add, "rm", "*.log")
	if err != nil || !changed || strings.Join(rm.Ignore, ",") != "build/" {
		t.Errorf("rm = %+v, %v, %v", rm, changed, err)
	}
	if _, _, err := editIgnore(rm, "rm", "nope"); err == nil {
		t.Error("removing an absent rule should fail")
	}
	for _, bad := range []string{"", "  ", "# comment", "a\nb"} {
		if _, _, err := editIgnore(base, "add", bad); err == nil {
			t.Errorf("add %q: want an error", bad)
		}
	}
}

func TestRemoteCommand(t *testing.T) {
	for _, args := range [][]string{{"remote"}, {"remote", "ls", "x"}, {"remote", "rm"}, {"remote", "get"}, {"remote", "get", "a", "b", "c"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "usage: syncd remote ls | syncd remote get") {
			t.Errorf("%v: code = %d, stderr = %q", args, code, stderr.String())
		}
	}
	if got := formatRemote(drive.RemoteFile{ID: "f1", RelPath: "a/b.txt", Size: 12, MD5: "abc", Tagged: true}); got != "a/b.txt  12  abc  f1" {
		t.Errorf("line = %q", got)
	}
	if got := formatRemote(drive.RemoteFile{ID: "f2", RelPath: "x.txt"}); !strings.HasSuffix(got, "(no rel_path tag)") {
		t.Errorf("untagged line = %q", got)
	}
}

func TestRestoreCommandUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"restore", "extra"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "usage: syncd restore") {
		t.Errorf("code = %d, stderr = %q", code, stderr.String())
	}
}

func TestReportRestore(t *testing.T) {
	var out, errb bytes.Buffer
	rep := reportRestore(&out, &errb)
	rep(burrowsync.RestoreEvent{RelPath: "a.txt", Action: burrowsync.RestoreDownloaded})
	rep(burrowsync.RestoreEvent{RelPath: "b.txt", Action: burrowsync.RestoreMatched})
	rep(burrowsync.RestoreEvent{RelPath: "c.txt", Action: burrowsync.RestoreConflict, ConflictCopy: "c (conflicted copy).txt"})
	rep(burrowsync.RestoreEvent{RelPath: "d.txt", Action: burrowsync.RestoreFailed, Err: errors.New("boom")})
	want := "downloaded a.txt\nmatched b.txt\nconflict c.txt (your version kept as c (conflicted copy).txt)\n"
	if out.String() != want || errb.String() != "failed d.txt: boom\n" {
		t.Errorf("stdout = %q, stderr = %q", out.String(), errb.String())
	}
}
