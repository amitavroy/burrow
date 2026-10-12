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

// ClientID and ClientSecret are the credentials baked into a release build with
//
//	-ldflags "-X github.com/amitavroy/burrow/internal/drive.ClientID=..."
//
// They are empty in a plain `go build`. The environment variables win over them.
var ClientID, ClientSecret string

// LoadClient reads the OAuth client credentials from the environment, falling
// back to the values baked in at release build time.
func LoadClient() (Client, error) {
	id := firstSet(os.Getenv("BURROW_GOOGLE_CLIENT_ID"), ClientID)
	if id == "" {
		return Client{}, fmt.Errorf("BURROW_GOOGLE_CLIENT_ID is not set")
	}
	secret := firstSet(os.Getenv("BURROW_GOOGLE_CLIENT_SECRET"), ClientSecret)
	if secret == "" {
		return Client{}, fmt.Errorf("BURROW_GOOGLE_CLIENT_SECRET is not set")
	}
	return Client{ID: id, Secret: secret}, nil
}

func firstSet(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
