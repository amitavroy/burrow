package store

import (
	"testing"

	"github.com/adrg/xdg"
)

func TestEmailCache(t *testing.T) {
	old := xdg.DataHome
	xdg.DataHome = t.TempDir()
	defer func() { xdg.DataHome = old }()

	if got := LoadEmail(); got != "" {
		t.Fatalf("empty cache = %q", got)
	}
	if err := SaveEmail("me@example.com"); err != nil {
		t.Fatal(err)
	}
	if got := LoadEmail(); got != "me@example.com" {
		t.Errorf("LoadEmail = %q", got)
	}
	if err := ClearEmail(); err != nil || LoadEmail() != "" {
		t.Errorf("ClearEmail: err %v, email %q", err, LoadEmail())
	}
	if err := ClearEmail(); err != nil {
		t.Errorf("clearing twice: %v", err)
	}
}
