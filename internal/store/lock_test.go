package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLockIsExclusiveUntilUnlocked(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state", "burrow.db")
	first, err := Lock(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(db); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Lock = %v, want ErrLocked", err)
	}

	if err := first.Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := first.Unlock(); err != nil {
		t.Fatalf("second Unlock = %v, want nil", err)
	}
	second, err := Lock(db)
	if err != nil {
		t.Fatalf("Lock after Unlock = %v", err)
	}
	second.Unlock()
}

func TestLockFileSitsBesideTheDatabase(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "burrow.db")
	l, err := Lock(db)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Unlock()
	if _, err := os.Stat(db + ".lock"); err != nil {
		t.Errorf("lock file: %v", err)
	}
}

func TestLockOfDifferentDatabasesDoNotClash(t *testing.T) {
	dir := t.TempDir()
	a, err := Lock(filepath.Join(dir, "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Unlock()
	b, err := Lock(filepath.Join(dir, "b.db"))
	if err != nil {
		t.Fatalf("a different database must lock independently: %v", err)
	}
	b.Unlock()
}
