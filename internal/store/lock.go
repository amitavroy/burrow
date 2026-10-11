package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/gofrs/flock"
)

// ErrLocked means another syncd process holds the database lock.
var ErrLocked = errors.New("store: database is in use by another syncd")

// FileLock is an exclusive advisory lock on a database, held for the life of a
// process that runs jobs. The operating system drops it when the process
// exits or crashes, so it is never stale.
type FileLock struct {
	mu sync.Mutex
	fl *flock.Flock
}

// Lock takes the lock for the database at dbPath, without waiting. The lock
// file is dbPath + ".lock", beside the database in the data directory and never
// in the sync root. It returns ErrLocked when another process holds it.
func Lock(dbPath string) (*FileLock, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	fl := flock.New(dbPath + ".lock")
	ok, err := fl.TryLock()
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", dbPath, err)
	}
	if !ok {
		return nil, ErrLocked
	}
	return &FileLock{fl: fl}, nil
}

// Unlock releases the lock. It is safe to call more than once.
func (l *FileLock) Unlock() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fl == nil {
		return nil
	}
	err := l.fl.Close() // unlocks and closes the file
	l.fl = nil
	return err
}
