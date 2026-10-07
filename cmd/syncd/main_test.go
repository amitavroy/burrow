package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amitavroy/burrow/internal/drive"
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
		{name: "watch without rel_path is a usage error", args: []string{"stat", "--watch", "demo"}, wantCode: 2, wantStderr: "usage: syncd stat"},
		{name: "unknown flag", args: []string{"stat", "--bogus", "id"}, wantCode: 2, wantStderr: "flag provided but not defined"},
		{name: "by ID, not signed in", args: []string{"stat", "file-id"}, wantCode: 1, wantStderr: "run `syncd login`"},
		{name: "by tags, not signed in", args: []string{"stat", "--watch", "demo", "notes.txt"}, wantCode: 1, wantStderr: "run `syncd login`"},
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
			for _, want := range []string{"Path:    " + path, "Version: 1", "files", "goose_db_version"} {
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
		{"lists files sorted", []string{"scan", "--dry-run", "--root", root}, 0, "a/c.txt\nb.txt\n", ""},
		{"default root is ~/MySync", []string{"scan", "--dry-run"}, 0, "h.txt\n", ""},
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
