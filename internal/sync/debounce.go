package sync

import (
	"os"
	"path/filepath"
	stdsync "sync"
	"time"
)

// Debouncer turns the raw event stream of a Watcher into one signal per
// settled file. An editor save is many events (temp file, create, several
// writes, rename); a copy of a big file is a stream of writes. A path is
// reported on Ready only after it has been quiet for a settle period and two
// samples of its size and mtime, one settle period apart, are equal. A new
// event for the path starts the wait again.
type Debouncer struct {
	root   string
	settle time.Duration

	in    <-chan WatchEvent
	fires chan fire
	ready chan string
	// removed gets a signal (never blocking) for each rename or remove event,
	// file or folder: something may be gone, see Removed.
	removed chan struct{}

	pending map[string]*pendingFile // touched only by the loop

	done      chan struct{}
	exited    chan struct{}
	closeOnce stdsync.Once
}

// fileSample is what "stable" compares. mtime is Unix nanoseconds.
type fileSample struct {
	size  int64
	mtime int64
}

type pendingFile struct {
	gen     int // bumped by every event, so a timer that fires late can be told apart
	timer   *time.Timer
	sample  fileSample
	sampled bool
}

type fire struct {
	path string
	gen  int
}

// NewDebouncer starts reading in. root is where the rel_paths in the events
// live. Call Close when done; closing in (the Watcher was closed) also stops it.
func NewDebouncer(in <-chan WatchEvent, root string, settle time.Duration) *Debouncer {
	d := &Debouncer{
		root: root, settle: settle,
		in:      in,
		fires:   make(chan fire),
		ready:   make(chan string, 64),
		removed: make(chan struct{}, 1),
		pending: map[string]*pendingFile{},
		done:    make(chan struct{}),
		exited:  make(chan struct{}),
	}
	go d.loop()
	return d
}

// Ready delivers the rel_path of each file whose size and mtime have stopped
// changing. It is closed when the Debouncer stops. Paths still waiting at that
// moment are dropped; the startup scan finds them next time.
func (d *Debouncer) Ready() <-chan string { return d.ready }

// Removed signals that a rename or remove event was seen. Signals pile up into
// one, so it says "something may be gone since you last looked", not what.
func (d *Debouncer) Removed() <-chan struct{} { return d.removed }

// Close stops the Debouncer and closes Ready. It is safe to call more than once.
func (d *Debouncer) Close() {
	d.closeOnce.Do(func() { close(d.done) })
	<-d.exited
}

func (d *Debouncer) loop() {
	defer close(d.exited)
	defer close(d.ready)
	defer func() {
		for _, p := range d.pending {
			p.timer.Stop()
		}
	}()
	for {
		select {
		case <-d.done:
			return
		case e, ok := <-d.in:
			if !ok {
				return
			}
			safely("debouncer.event", func() { d.onEvent(e) })
		case f := <-d.fires:
			if safely("debouncer.fire", func() { d.onFire(f) }) {
				delete(d.pending, f.path) // its timer has fired; a later event starts it again
			}
		}
	}
}

func (d *Debouncer) onEvent(e WatchEvent) {
	if e.Op == OpRename || e.Op == OpRemove {
		select {
		case d.removed <- struct{}{}:
		default:
		}
	}
	if e.IsDir { // jobs are per file; the watcher reports files in new folders itself
		return
	}
	switch e.Op {
	case OpCreate, OpWrite:
		p := d.pending[e.RelPath]
		if p == nil {
			p = &pendingFile{}
			d.pending[e.RelPath] = p
		} else {
			p.timer.Stop()
		}
		p.gen++
		p.sampled = false
		d.arm(e.RelPath, p)
	case OpRename, OpRemove: // the content that changed is no longer at this path
		if p := d.pending[e.RelPath]; p != nil {
			p.timer.Stop()
			delete(d.pending, e.RelPath)
		}
	}
}

func (d *Debouncer) arm(path string, p *pendingFile) {
	gen := p.gen
	p.timer = time.AfterFunc(d.settle, func() {
		select {
		case d.fires <- fire{path, gen}:
		case <-d.done:
		}
	})
}

func (d *Debouncer) onFire(f fire) {
	p := d.pending[f.path]
	if p == nil || p.gen != f.gen { // superseded by a newer event
		return
	}
	st, err := os.Lstat(filepath.Join(d.root, filepath.FromSlash(f.path)))
	if err != nil || !st.Mode().IsRegular() { // gone, or no longer a file
		delete(d.pending, f.path)
		return
	}
	s := fileSample{size: st.Size(), mtime: st.ModTime().UnixNano()}
	if p.sampled && s == p.sample {
		delete(d.pending, f.path)
		select {
		case d.ready <- f.path:
		case <-d.done:
		}
		return
	}
	p.sample, p.sampled = s, true
	d.arm(f.path, p)
}
