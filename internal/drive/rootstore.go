package drive

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/adrg/xdg"
)

// stateFile is the cache location under the app data dir (Local, not Roaming,
// on Windows). It is never inside the sync root.
const stateFile = "burrow/state.json"

// ErrNoRoot means no MySync folder ID is cached.
var ErrNoRoot = errors.New("no cached root folder")

// RootStore caches the Drive ID of the MySync folder. The cache is disposable:
// losing it only costs one lookup.
type RootStore interface {
	// Load returns the cached folder ID, or ErrNoRoot.
	Load() (string, error)
	Save(id string) error
}

// FileStore keeps the folder ID in a small JSON file. The zero value uses the
// xdg data dir; set Path to override it (tests).
type FileStore struct {
	Path string
}

var _ RootStore = FileStore{}

type state struct {
	RootFolderID string `json:"root_folder_id"`
}

func (s FileStore) path() (string, error) {
	if s.Path != "" {
		return s.Path, nil
	}
	p, err := xdg.DataFile(stateFile)
	if err != nil {
		return "", fmt.Errorf("locate state file: %w", err)
	}
	return p, nil
}

// Load returns ErrNoRoot when the file is missing, unreadable as JSON or holds
// no ID: a damaged cache is treated as empty so the caller just looks it up.
func (s FileStore) Load() (string, error) {
	p, err := s.path()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNoRoot
	}
	if err != nil {
		return "", fmt.Errorf("read state file: %w", err)
	}
	var st state
	if json.Unmarshal(b, &st) != nil || st.RootFolderID == "" {
		return "", ErrNoRoot
	}
	return st.RootFolderID, nil
}

// Save writes the ID through a temp file and rename, so a crash never leaves a
// half-written cache.
func (s FileStore) Save(id string) error {
	p, err := s.path()
	if err != nil {
		return err
	}
	b, err := json.Marshal(state{RootFolderID: id})
	if err != nil {
		return fmt.Errorf("encode state file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "state-*.tmp")
	if err != nil {
		return fmt.Errorf("write state file: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("write state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write state file: %w", err)
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return fmt.Errorf("write state file: %w", err)
	}
	return nil
}
