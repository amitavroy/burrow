package drive

import (
	"fmt"
	"os"

	"golang.org/x/oauth2"
)

// ScopeDriveFile is the only scope the app requests.
const ScopeDriveFile = "https://www.googleapis.com/auth/drive.file"

// Client holds the OAuth client credentials that ship with the app.
type Client struct {
	ID     string
	Secret string
	// Endpoint overrides Google's OAuth endpoint (tests). Nil means Google.
	Endpoint *oauth2.Endpoint
}

// LoadClient reads the OAuth client credentials from the environment.
func LoadClient() (Client, error) {
	id := os.Getenv("BURROW_GOOGLE_CLIENT_ID")
	if id == "" {
		return Client{}, fmt.Errorf("BURROW_GOOGLE_CLIENT_ID is not set")
	}
	secret := os.Getenv("BURROW_GOOGLE_CLIENT_SECRET")
	if secret == "" {
		return Client{}, fmt.Errorf("BURROW_GOOGLE_CLIENT_SECRET is not set")
	}
	return Client{ID: id, Secret: secret}, nil
}
