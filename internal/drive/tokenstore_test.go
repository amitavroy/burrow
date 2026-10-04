package drive

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestKeyringStore(t *testing.T) {
	keyring.MockInit()
	s := KeyringStore{}

	if _, err := s.Load(); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("Load on empty keychain: err = %v, want ErrNotSignedIn", err)
	}
	if err := s.Delete(); err != nil {
		t.Errorf("Delete on empty keychain: %v", err)
	}
	if err := s.Save(""); err == nil {
		t.Error("Save of empty token should fail")
	}
	if err := s.Save("refresh"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got, err := s.Load(); err != nil || got != "refresh" {
		t.Errorf("Load = %q, %v; want refresh", got, err)
	}
	if err := s.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Delete(); err != nil {
		t.Errorf("second Delete: %v", err)
	}
	if _, err := s.Load(); !errors.Is(err, ErrNotSignedIn) {
		t.Errorf("Load after Delete: err = %v, want ErrNotSignedIn", err)
	}
}
