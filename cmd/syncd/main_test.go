package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/amitavroy/burrow/internal/drive"
	"github.com/amitavroy/burrow/internal/store"
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
			for _, want := range []string{"Path:    " + path, "Version: 3", "files", "folders", "goose_db_version"} {
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
