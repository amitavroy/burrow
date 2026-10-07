package drive

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"
)

type memStore struct{ token string }

func (m *memStore) Load() (string, error) {
	if m.token == "" {
		return "", ErrNotSignedIn
	}
	return m.token, nil
}
func (m *memStore) Save(t string) error { m.token = t; return nil }
func (m *memStore) Delete() error       { m.token = ""; return nil }

func TestResume(t *testing.T) {
	tests := []struct {
		name         string
		stored       string
		tokenStatus  int
		tokenBody    string
		wantEmail    string
		wantErr      error
		wantRefresh  bool // token endpoint should be called
		wantAboutHit bool
	}{
		{
			name:         "refreshes the stored token and returns the email",
			stored:       "stored-refresh",
			tokenStatus:  200,
			tokenBody:    `{"access_token":"fresh-access","token_type":"Bearer","expires_in":3600}`,
			wantEmail:    "a@example.com",
			wantRefresh:  true,
			wantAboutHit: true,
		},
		{
			name:    "not signed in",
			wantErr: ErrNotSignedIn,
		},
		{
			name:        "revoked token is a session expiry",
			stored:      "stored-refresh",
			tokenStatus: 400,
			tokenBody:   `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`,
			wantErr:     ErrSessionExpired,
			wantRefresh: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var refreshForm map[string]string
			var aboutAuth string
			var refreshed, aboutHit bool

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				mu.Lock()
				defer mu.Unlock()
				if r.URL.Path == "/token" {
					_ = r.ParseForm()
					refreshed = true
					refreshForm = map[string]string{
						"grant_type":    r.PostForm.Get("grant_type"),
						"refresh_token": r.PostForm.Get("refresh_token"),
					}
					w.WriteHeader(tt.tokenStatus)
					_, _ = w.Write([]byte(tt.tokenBody))
					return
				}
				aboutHit = true
				aboutAuth = r.Header.Get("Authorization")
				_, _ = w.Write([]byte(`{"user":{"emailAddress":"a@example.com"}}`))
			}))
			defer srv.Close()

			client := Client{ID: "id", Secret: "secret", Endpoint: &oauth2.Endpoint{TokenURL: srv.URL + "/token"}}

			got, err := Resume(context.Background(), client, &memStore{token: tt.stored}, option.WithEndpoint(srv.URL))

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("resume: %v", err)
			}
			if got != tt.wantEmail {
				t.Errorf("email = %q, want %q", got, tt.wantEmail)
			}
			mu.Lock()
			defer mu.Unlock()
			if refreshed != tt.wantRefresh {
				t.Errorf("token endpoint called = %v, want %v", refreshed, tt.wantRefresh)
			}
			if aboutHit != tt.wantAboutHit {
				t.Errorf("about called = %v, want %v", aboutHit, tt.wantAboutHit)
			}
			if tt.wantRefresh {
				if refreshForm["grant_type"] != "refresh_token" || refreshForm["refresh_token"] != tt.stored {
					t.Errorf("refresh request = %v", refreshForm)
				}
			}
			if tt.wantAboutHit && aboutAuth != "Bearer fresh-access" {
				t.Errorf("about Authorization = %q, want the refreshed access token", aboutAuth)
			}
		})
	}
}

func TestTokenFromRefreshIsExpired(t *testing.T) {
	tok := TokenFromRefresh("r")
	if tok.RefreshToken != "r" || tok.Valid() {
		t.Errorf("token = %+v; want refresh token set and not Valid so oauth2 refreshes", tok)
	}
}
