package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// seen collects what a Watcher reports so a test can look for an event that
// has already gone by. The OS may add extra or repeated events, so tests ask
// "was this seen", not "was exactly this seen".
type seen struct {
	t   *testing.T
	w   *Watcher
	all []WatchEvent
}

func newWatcher(t *testing.T, root string) *seen {
	t.Helper()
	w, err := NewWatcher(root)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	t.Cleanup(func() { w.Close() })
	return &seen{t: t, w: w}
}

func (s *seen) has(e WatchEvent) bool {
	for _, got := range s.all {
		if got == e {
			return true
		}
	}
	return false
}

// expect waits up to 3 s for e.
func (s *seen) expect(e WatchEvent) {
	s.t.Helper()
	if s.has(e) {
		return
	}
	timeout := time.After(3 * time.Second)
	for {
		select {
		case got, ok := <-s.w.Events():
			if !ok {
				s.t.Fatalf("channel closed before %+v; saw %+v", e, s.all)
			}
			s.all = append(s.all, got)
			if got == e {
				return
			}
		case <-timeout:
			s.t.Fatalf("did not see %+v; saw %+v", e, s.all)
		}
	}
}

// quiet drains events for a short while and fails if any path matches bad.
func (s *seen) quiet(bad func(WatchEvent) bool) {
	s.t.Helper()
	timeout := time.After(300 * time.Millisecond)
	for {
		select {
		case got := <-s.w.Events():
			s.all = append(s.all, got)
		case <-timeout:
			for _, e := range s.all {
				if bad(e) {
					s.t.Errorf("unexpected event %+v; saw %+v", e, s.all)
				}
			}
			return
		}
	}
}

func ev(op WatchOp, rel string) WatchEvent    { return WatchEvent{rel, op, false} }
func evDir(op WatchOp, rel string) WatchEvent { return WatchEvent{rel, op, true} }

func mkdirs(t *testing.T, root string, rels ...string) {
	t.Helper()
	for _, r := range rels {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(r)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWatchFileOpsInRootAndNestedFolder(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "a/b")
	s := newWatcher(t, root)

	for _, dir := range []string{"", "a/b/"} {
		p := filepath.Join(root, filepath.FromSlash(dir), "f.txt")
		write(t, p, "x")
		s.expect(ev(OpCreate, dir+"f.txt"))
		s.expect(ev(OpWrite, dir+"f.txt"))

		moved := filepath.Join(root, filepath.FromSlash(dir), "g.txt")
		if err := os.Rename(p, moved); err != nil {
			t.Fatal(err)
		}
		s.expect(ev(OpRename, dir+"f.txt"))
		s.expect(ev(OpCreate, dir+"g.txt"))

		if err := os.Remove(moved); err != nil {
			t.Fatal(err)
		}
		s.expect(ev(OpRemove, dir+"g.txt"))
	}
}

func TestWatchIgnoredPathsGiveNoEvents(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "node_modules/x", ".git", "src")
	write(t, filepath.Join(root, ".syncignore"), "secret.txt\n")
	s := newWatcher(t, root)

	write(t, filepath.Join(root, "x.tmp"), "1")
	write(t, filepath.Join(root, "node_modules", "x", "a.js"), "1")
	write(t, filepath.Join(root, ".git", "HEAD"), "1")
	write(t, filepath.Join(root, "secret.txt"), "1")
	write(t, filepath.Join(root, "src", "real.txt"), "1") // proves the watcher is alive
	s.expect(ev(OpCreate, "src/real.txt"))

	s.quiet(func(e WatchEvent) bool { return e.RelPath != "src/real.txt" && e.RelPath != "src" })
}

func TestWatchingCountsDirectoriesButNotIgnoredOnes(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "a/b", "c", "node_modules/deep", ".git/objects")
	s := newWatcher(t, root)
	if got := s.w.Watching(); got != 4 { // root, a, a/b, c
		t.Errorf("Watching = %d, want 4", got)
	}
}

func TestWatcherCloseIsIdempotentAndClosesChannels(t *testing.T) {
	w, err := NewWatcher(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	if _, ok := <-w.Events(); ok {
		t.Error("Events still open after Close")
	}
	if _, ok := <-w.Errors(); ok {
		t.Error("Errors still open after Close")
	}
}

func TestNewWatcherBadRoot(t *testing.T) {
	if _, err := NewWatcher(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("missing root: want an error")
	}
	f := filepath.Join(t.TempDir(), "file")
	write(t, f, "x")
	if _, err := NewWatcher(f); err == nil {
		t.Error("file as root: want an error")
	}
}

// A file made right after its folder is not lost to the gap before the new
// folder is watched.
func TestWatchNewFolderThenFile(t *testing.T) {
	root := t.TempDir()
	s := newWatcher(t, root)

	write(t, filepath.Join(root, "x", "y", "z", "f.txt"), "hi") // MkdirAll + file at once
	s.expect(evDir(OpCreate, "x"))
	s.expect(ev(OpCreate, "x/y/z/f.txt"))

	write(t, filepath.Join(root, "x", "y", "z", "later.txt"), "hi")
	s.expect(ev(OpCreate, "x/y/z/later.txt"))
	if got := s.w.Watching(); got != 4 {
		t.Errorf("Watching = %d, want 4 (root, x, x/y, x/y/z)", got)
	}
}

func TestWatchRemoveThenRecreateFolder(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "d/sub")
	s := newWatcher(t, root)

	if err := os.RemoveAll(filepath.Join(root, "d")); err != nil {
		t.Fatal(err)
	}
	s.expect(evDir(OpRemove, "d"))
	waitFor(t, func() bool { return s.w.Watching() == 1 })

	write(t, filepath.Join(root, "d", "f.txt"), "x")
	s.expect(ev(OpCreate, "d/f.txt"))
	// The write may have happened before the new watch existed, so only the
	// create is certain. A later write proves the recreated folder is watched.
	write(t, filepath.Join(root, "d", "f.txt"), "y")
	s.expect(ev(OpWrite, "d/f.txt"))
}

// After a folder is renamed, events inside it carry the new name.
func TestWatchRenamedFolderReportsNewPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("renaming a watched directory is not allowed on Windows")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "x", "y", "f.txt"), "1")
	s := newWatcher(t, root)

	if err := os.Rename(filepath.Join(root, "x"), filepath.Join(root, "w")); err != nil {
		t.Fatal(err)
	}
	s.expect(evDir(OpRename, "x"))
	s.expect(evDir(OpCreate, "w"))
	s.expect(ev(OpCreate, "w/y/f.txt")) // found by the walk of the new name

	write(t, filepath.Join(root, "w", "y", "f.txt"), "2")
	s.expect(ev(OpWrite, "w/y/f.txt"))
	for _, e := range s.all {
		if e.Op == OpWrite && len(e.RelPath) > 0 && e.RelPath[0] == 'x' {
			t.Errorf("event under the old name: %+v", e)
		}
	}
	waitFor(t, func() bool { return s.w.Watching() == 3 }) // root, w, w/y
}

// A folder moved in from outside the root is watched like any new one.
func TestWatchFolderMovedInFromOutside(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	write(t, filepath.Join(outside, "in", "deep", "f.txt"), "1")
	s := newWatcher(t, root)

	if err := os.Rename(filepath.Join(outside, "in"), filepath.Join(root, "in")); err != nil {
		t.Skip("cannot move across filesystems:", err)
	}
	s.expect(ev(OpCreate, "in/deep/f.txt"))
	write(t, filepath.Join(root, "in", "deep", "g.txt"), "1")
	s.expect(ev(OpCreate, "in/deep/g.txt"))
}

func TestWatchBurstOfFilesInNewFolder(t *testing.T) {
	root := t.TempDir()
	s := newWatcher(t, root)

	dir := filepath.Join(root, "burst")
	mkdirs(t, root, "burst")
	for i := range 200 {
		write(t, filepath.Join(dir, fmt.Sprintf("f%03d.txt", i)), "x")
	}
	for i := range 200 {
		s.expect(ev(OpCreate, fmt.Sprintf("burst/f%03d.txt", i)))
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
