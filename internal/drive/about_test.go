package drive

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"
)

func TestEmail(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		status  int
		want    string
		wantErr bool
	}{
		{name: "ok", body: `{"user":{"emailAddress":"a@example.com"}}`, status: 200, want: "a@example.com"},
		{name: "no user", body: `{}`, status: 200, wantErr: true},
		{name: "api error", body: `{"error":{"code":401,"message":"nope"}}`, status: 401, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Query().Get("fields"); got != "user(emailAddress)" {
					t.Errorf("fields = %q", got)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			tok := &oauth2.Token{AccessToken: "x", TokenType: "Bearer"}
			got, err := Email(context.Background(), Client{ID: "id", Secret: "s"}, tok,
				option.WithEndpoint(srv.URL))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("email = %q, want %q", got, tt.want)
			}
		})
	}
}
