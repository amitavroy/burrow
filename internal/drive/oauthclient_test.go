package drive

import (
	"strings"
	"testing"
)

func TestLoadClient(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		secret  string
		want    Client
		wantErr string
	}{
		{name: "both set", id: "id-1", secret: "secret-1", want: Client{ID: "id-1", Secret: "secret-1"}},
		{name: "id missing", secret: "secret-1", wantErr: "BURROW_GOOGLE_CLIENT_ID"},
		{name: "secret missing", id: "id-1", wantErr: "BURROW_GOOGLE_CLIENT_SECRET"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BURROW_GOOGLE_CLIENT_ID", tt.id)
			t.Setenv("BURROW_GOOGLE_CLIENT_SECRET", tt.secret)

			got, err := LoadClient()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("LoadClient() error = %v, want it to mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadClient() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("LoadClient() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestScopeDriveFile(t *testing.T) {
	const want = "https://www.googleapis.com/auth/drive.file"
	if ScopeDriveFile != want {
		t.Fatalf("ScopeDriveFile = %q, want %q", ScopeDriveFile, want)
	}
}
