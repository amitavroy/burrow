package main

import (
	"bytes"
	"errors"
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
