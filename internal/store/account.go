package store

import (
	"os"
	"path/filepath"
	"strings"
)

// AccountPath is the file that caches the signed-in account's email, next to
// the database. It is a convenience for the tray (no keychain or network); the
// refresh token stays in the keychain only.
func AccountPath() string { return filepath.Join(filepath.Dir(DefaultPath()), "account") }

// LoadEmail returns the cached email, or "" when signed out or unreadable.
func LoadEmail() string {
	b, _ := os.ReadFile(AccountPath())
	return strings.TrimSpace(string(b))
}

// SaveEmail caches email for LoadEmail.
func SaveEmail(email string) error {
	p := AccountPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(email+"\n"), 0o600)
}

// ClearEmail forgets the cached email. A missing file is fine.
func ClearEmail() error {
	if err := os.Remove(AccountPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
