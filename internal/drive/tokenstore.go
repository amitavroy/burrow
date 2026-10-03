package drive

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

const (
	keyringService = "burrow"
	keyringUser    = "google-refresh-token"
)

// ErrNotSignedIn means no refresh token is stored.
var ErrNotSignedIn = errors.New("not signed in")

// TokenStore persists the OAuth refresh token. Only the refresh token is
// stored; access tokens are re-derived by refreshing.
type TokenStore interface {
	// Load returns the stored refresh token, or ErrNotSignedIn.
	Load() (string, error)
	Save(refreshToken string) error
	// Delete removes the token. Deleting a missing token is not an error.
	Delete() error
}

// KeyringStore keeps the refresh token in the OS keychain. It is the only
// place the token may live: never the DB, config or logs.
type KeyringStore struct{}

var _ TokenStore = KeyringStore{}

func (KeyringStore) Load() (string, error) {
	tok, err := keyring.Get(keyringService, keyringUser)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotSignedIn
	}
	if err != nil {
		return "", fmt.Errorf("read keychain: %w", err)
	}
	return tok, nil
}

func (KeyringStore) Save(refreshToken string) error {
	if refreshToken == "" {
		return errors.New("empty refresh token")
	}
	if err := keyring.Set(keyringService, keyringUser, refreshToken); err != nil {
		return fmt.Errorf("write keychain: %w", err)
	}
	return nil
}

func (KeyringStore) Delete() error {
	err := keyring.Delete(keyringService, keyringUser)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("clear keychain: %w", err)
	}
	return nil
}
