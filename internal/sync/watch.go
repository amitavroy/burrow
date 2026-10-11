package sync

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	stdsync "sync"
	"sync/atomic"

	"github.com/fsnotify/fsnotify"
)

// WatchOp is what happened to a path.
type WatchOp string

const (
	OpCreate WatchOp = "create"
	OpWrite  WatchOp = "write"
	OpRename WatchOp = "rename"
	OpRemove WatchOp = "remove"
)

// WatchEvent is one change under the watched root. RelPath is slash-form,
// relative to the root. After a rename the old path gets OpRename and the new
// one OpCreate.
type WatchEvent struct {
	RelPath string
	Op      WatchOp
	IsDir   bool
}

// Watcher reports changes anywhere under a root. fsnotify is not recursive, so
// the Watcher registers every directory itself, including ones created later.
// Paths the ignore rules skip (see Matcher) produce no events and their
// directories are not watched. It does no debouncing: one save can produce
// several events, and a path can be reported twice (see watchNewDir).
type Watcher struct {
	fw     *fsnotify.Watcher
	root   string
	ignore *Matcher

	events chan WatchEvent
	errs   chan error

	dirs map[string]struct{} // watched directories (absolute); touched only before the loop starts and by the loop
	n    atomic.Int32        // len(dirs), readable from any goroutine

	done      chan struct{}
	exited    chan struct{}
	closeOnce stdsync.Once
}

// NewWatcher watches root and every directory below it, and starts reporting.
// It fails if the root cannot be read or a directory cannot be watched (the
// error names it). Call Close when done.
func NewWatcher(root string) (*Watcher, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("watch root: %w", err)
	}
	if st, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("watch root: %w", err)
	} else if !st.IsDir() {
		return nil, fmt.Errorf("watch root: %s is not a directory", abs)
	}
	ignore, err := NewMatcher(abs)
	if err != nil {
		return nil, err
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("watch: %w", err)
	}
	w := &Watcher{
		fw: fw, root: abs, ignore: ignore,
		events: make(chan WatchEvent, 256),
		errs:   make(chan error, 16),
		dirs:   map[string]struct{}{},
		done:   make(chan struct{}),
		exited: make(chan struct{}),
	}
	if err := w.addTree(abs, false); err != nil {
		fw.Close()
		return nil, err
	}
	go w.loop()
	return w, nil
}

// Events delivers changes until Close, then is closed.
func (w *Watcher) Events() <-chan WatchEvent { return w.events }

// Errors delivers problems reported by the OS watcher, such as a queue
// overflow (events may have been missed). It is closed by Close.
func (w *Watcher) Errors() <-chan error { return w.errs }

// Watching is the number of directories currently watched.
func (w *Watcher) Watching() int { return int(w.n.Load()) }

// Close stops the watcher and closes both channels. It is safe to call more
// than once.
func (w *Watcher) Close() error {
	var err error
	w.closeOnce.Do(func() {
		close(w.done)
		err = w.fw.Close()
	})
	<-w.exited
	return err
}

func (w *Watcher) loop() {
	defer close(w.exited)
	defer close(w.events)
	defer close(w.errs)
	for {
		select {
		case <-w.done:
			return
		case ev, ok := <-w.fw.Events:
			if !ok {
				return
			}
			w.handle(ev)
		case err, ok := <-w.fw.Errors:
			if !ok {
				return
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				slog.Warn("watcher queue overflowed, events may be missed", "err", err)
			}
			select {
			case w.errs <- err:
			case <-w.done:
				return
			default: // nobody reads errors; do not stall the events
			}
		}
	}
}

func (w *Watcher) rel(path string) (string, bool) {
	r, err := filepath.Rel(w.root, path)
	if err != nil || r == "." || strings.HasPrefix(r, "..") {
		return "", false
	}
	return filepath.ToSlash(r), true
}

func (w *Watcher) emit(e WatchEvent) {
	select {
	case w.events <- e:
	case <-w.done:
	}
}

func (w *Watcher) handle(ev fsnotify.Event) {
	rel, ok := w.rel(ev.Name)
	if !ok {
		return
	}
	_, known := w.dirs[ev.Name]
	isDir := known
	if !known && ev.Has(fsnotify.Create|fsnotify.Write) {
		if st, err := os.Lstat(ev.Name); err == nil {
			isDir = st.IsDir()
		}
	}
	if w.ignore.Ignored(rel, isDir) {
		return
	}
	if ev.Has(fsnotify.Create) {
		w.emit(WatchEvent{rel, OpCreate, isDir})
		if isDir && !known {
			w.watchNewDir(ev.Name)
		}
	}
	if ev.Has(fsnotify.Write) {
		w.emit(WatchEvent{rel, OpWrite, isDir})
	}
	if ev.Has(fsnotify.Remove) {
		w.emit(WatchEvent{rel, OpRemove, isDir})
		w.dropTree(ev.Name)
	}
	if ev.Has(fsnotify.Rename) {
		w.emit(WatchEvent{rel, OpRename, isDir})
		w.dropTree(ev.Name)
	}
}

// watchNewDir watches a directory that appeared while running. Nothing is
// reported for a file created in the gap between the directory appearing and
// the watch being added, so the walk reports what is already inside as create
// events. A path can then be reported twice (once by the walk, once by the
// OS); the debounce of ticket 20 collapses that.
func (w *Watcher) watchNewDir(dir string) {
	if err := w.addTree(dir, true); err != nil {
		slog.Warn("cannot watch new directory", "dir", dir, "err", err)
		select {
		case w.errs <- err:
		default:
		}
	}
}

// addTree watches dir and every directory below it, skipping ignored ones.
// With report set it also emits create events for the entries found below dir
// (not for dir itself). A directory that vanished meanwhile is skipped.
func (w *Watcher) addTree(dir string, report bool) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("watch %s: %w", path, err)
		}
		rel, ok := w.rel(path)
		if ok && w.ignore.Ignored(rel, d.IsDir()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if report && path != dir {
			w.emit(WatchEvent{rel, OpCreate, d.IsDir()})
		}
		if !d.IsDir() {
			return nil
		}
		if _, dup := w.dirs[path]; dup {
			return nil
		}
		if err := w.fw.Add(path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fs.SkipDir
			}
			return fmt.Errorf("watch %s: %w", path, err)
		}
		w.dirs[path] = struct{}{}
		w.n.Add(1)
		return nil
	})
}

// dropTree forgets dir and every watched directory below it. On Linux the
// kernel watch follows the directory itself, so after a rename its old watch
// would keep reporting the old name; the new name is watched afresh when its
// create event arrives.
func (w *Watcher) dropTree(dir string) {
	prefix := dir + string(filepath.Separator)
	for p := range w.dirs {
		if p == dir || strings.HasPrefix(p, prefix) {
			_ = w.fw.Remove(p) // already gone is fine
			delete(w.dirs, p)
			w.n.Add(-1)
		}
	}
}
