package drive

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// fakeProvider is a stand-in OAuth server; it records the token request form.
type fakeProvider struct {
	srv  *httptest.Server
	mu   sync.Mutex
	form url.Values
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	p := &fakeProvider{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		p.mu.Lock()
		p.form = r.PostForm
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access",
			"refresh_token": "refresh",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakeProvider) endpoint() *oauth2.Endpoint {
	return &oauth2.Endpoint{AuthURL: p.srv.URL + "/auth", TokenURL: p.srv.URL + "/token"}
}

func TestLogin(t *testing.T) {
	tests := []struct {
		name    string
		browser func(t *testing.T, authURL string) error
		timeout time.Duration
		wantErr string
	}{
		{
			name: "success",
			browser: func(t *testing.T, authURL string) error {
				u, _ := url.Parse(authURL)
				q := u.Query()
				if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
					t.Errorf("missing PKCE challenge: %v", q)
				}
				if q.Get("access_type") != "offline" || q.Get("prompt") != "consent" {
					t.Errorf("missing offline/consent: %v", q)
				}
				if q.Get("scope") != ScopeDriveFile {
					t.Errorf("scope = %q, want %q", q.Get("scope"), ScopeDriveFile)
				}
				if !strings.HasPrefix(q.Get("redirect_uri"), "http://127.0.0.1:") {
					t.Errorf("redirect_uri = %q", q.Get("redirect_uri"))
				}
				return hitCallback(q.Get("redirect_uri"), url.Values{"code": {"abc"}, "state": {q.Get("state")}})
			},
		},
		{
			name: "state mismatch",
			browser: func(t *testing.T, authURL string) error {
				u, _ := url.Parse(authURL)
				return hitCallback(u.Query().Get("redirect_uri"), url.Values{"code": {"abc"}, "state": {"wrong"}})
			},
			wantErr: "state mismatch",
		},
		{
			name: "provider error",
			browser: func(t *testing.T, authURL string) error {
				u, _ := url.Parse(authURL)
				q := u.Query()
				return hitCallback(q.Get("redirect_uri"), url.Values{"error": {"access_denied"}, "state": {q.Get("state")}})
			},
			wantErr: "access_denied",
		},
		{
			name:    "timeout",
			browser: func(*testing.T, string) error { return nil },
			timeout: 50 * time.Millisecond,
			wantErr: "not completed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newFakeProvider(t)
			opts := LoginOptions{
				Endpoint: p.endpoint(),
				Timeout:  tt.timeout,
				OpenBrowser: func(authURL string) error {
					// Run the "browser" asynchronously, like a real one.
					go func() { _ = tt.browser(t, authURL) }()
					return nil
				},
			}
			tok, err := Login(context.Background(), Client{ID: "id", Secret: "secret"}, opts)

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Login: %v", err)
			}
			if tok.RefreshToken != "refresh" {
				t.Errorf("refresh token = %q", tok.RefreshToken)
			}
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.form.Get("code_verifier") == "" {
				t.Error("token request had no code_verifier")
			}
			if p.form.Get("code") != "abc" {
				t.Errorf("code = %q", p.form.Get("code"))
			}
		})
	}
}

func hitCallback(redirect string, q url.Values) error {
	resp, err := http.Get(redirect + "?" + q.Encode())
	if err != nil {
		return err
	}
	return resp.Body.Close()
}
