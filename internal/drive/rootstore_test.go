package drive

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileStoreRoundTrip(t *testing.T) {
	s := FileStore{Path: filepath.Join(t.TempDir(), "nested", "state.json")}

	if _, err := s.Load(); !errors.Is(err, ErrNoRoot) {
		t.Fatalf("Load on missing file: err = %v, want ErrNoRoot", err)
	}
	if err := s.Save("folder-1"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got, err := s.Load(); err != nil || got != "folder-1" {
		t.Fatalf("Load = %q, %v; want folder-1", got, err)
	}
	if err := s.Save("folder-2"); err != nil {
		t.Fatalf("Save overwrite: %v", err)
	}
	if got, _ := s.Load(); got != "folder-2" {
		t.Errorf("Load after overwrite = %q, want folder-2", got)
	}
	entries, _ := os.ReadDir(filepath.Dir(s.Path))
	if len(entries) != 1 {
		t.Errorf("state dir has %d entries, want only state.json (no temp files left)", len(entries))
	}
}

func TestFileStoreDamagedCacheIsEmpty(t *testing.T) {
	for name, content := range map[string]string{
		"corrupt json": "{not json",
		"empty file":   "",
		"no id":        `{"root_folder_id":""}`,
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := (FileStore{Path: p}).Load(); !errors.Is(err, ErrNoRoot) {
				t.Errorf("err = %v, want ErrNoRoot", err)
			}
		})
	}
}
