package sync

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const testSettle = 40 * time.Millisecond

// debounceRig feeds synthetic events to a Debouncer over a temp root.
type debounceRig struct {
	t    *testing.T
	root string
	in   chan WatchEvent
	d    *Debouncer
}

func newRig(t *testing.T, settle time.Duration) *debounceRig {
	t.Helper()
	r := &debounceRig{t: t, root: t.TempDir(), in: make(chan WatchEvent, 256)}
	r.d = NewDebouncer(r.in, r.root, settle)
	t.Cleanup(r.d.Close)
	return r
}

func (r *debounceRig) send(op WatchOp, rel string) { r.in <- WatchEvent{rel, op, false} }

func (r *debounceRig) file(rel, content string) {
	write(r.t, filepath.Join(r.root, filepath.FromSlash(rel)), content)
}

func (r *debounceRig) expectReady(rel string) time.Time {
	r.t.Helper()
	select {
	case got, ok := <-r.d.Ready():
		if !ok {
			r.t.Fatalf("Ready closed, wanted %q", rel)
		}
		if got != rel {
			r.t.Fatalf("ready %q, want %q", got, rel)
		}
		return time.Now()
	case <-time.After(3 * time.Second):
		r.t.Fatalf("no ready for %q", rel)
	}
	return time.Time{}
}

// expectNothing waits long enough for several settle periods to pass.
func (r *debounceRig) expectNothing(settle time.Duration) {
	r.t.Helper()
	select {
	case got, ok := <-r.d.Ready():
		if ok {
			r.t.Fatalf("unexpected ready %q", got)
		}
	case <-time.After(6 * settle):
	}
}

func TestDebounceBurstOfWritesIsOneReady(t *testing.T) {
	r := newRig(t, testSettle)
	r.file("a.txt", "x")
	for range 20 {
		r.send(OpWrite, "a.txt")
		time.Sleep(5 * time.Millisecond)
	}
	r.expectReady("a.txt")
	r.expectNothing(testSettle)
}

func TestDebounceSeparateSavesAreSeparateReadies(t *testing.T) {
	r := newRig(t, testSettle)
	r.file("a.txt", "one")
	r.send(OpCreate, "a.txt")
	r.expectReady("a.txt")

	r.file("a.txt", "two, longer")
	r.send(OpWrite, "a.txt")
	r.expectReady("a.txt")
}

func TestDebounceRenameAndRemoveCancel(t *testing.T) {
	for _, op := range []WatchOp{OpRename, OpRemove} {
		t.Run(string(op), func(t *testing.T) {
			r := newRig(t, testSettle)
			r.file("a.txt", "x")
			r.send(OpWrite, "a.txt")
			r.send(op, "a.txt")
			r.expectNothing(testSettle)
		})
	}
}

func TestDebounceIgnoresDirectories(t *testing.T) {
	r := newRig(t, testSettle)
	if err := os.Mkdir(filepath.Join(r.root, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.in <- WatchEvent{"d", OpCreate, true}
	r.expectNothing(testSettle)
}

func TestDebounceDropsAPathThatIsNoLongerAFile(t *testing.T) {
	r := newRig(t, testSettle)
	r.send(OpWrite, "never-existed.txt")
	r.file("was-file.txt", "x")
	r.send(OpWrite, "was-file.txt")
	if err := os.Remove(filepath.Join(r.root, "was-file.txt")); err != nil {
		t.Fatal(err)
	}
	r.expectNothing(testSettle)
}

func TestDebounceInputClosedClosesReady(t *testing.T) {
	r := newRig(t, testSettle)
	r.file("a.txt", "x")
	r.send(OpWrite, "a.txt") // pending when the input closes: dropped
	close(r.in)
	select {
	case got, ok := <-r.d.Ready():
		if ok {
			t.Fatalf("unexpected ready %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Ready not closed")
	}
}

func TestDebounceCloseIsIdempotent(t *testing.T) {
	r := newRig(t, testSettle)
	r.d.Close()
	r.d.Close()
	if _, ok := <-r.d.Ready(); ok {
		t.Error("Ready open after Close")
	}
}

// A file still being appended to is reported once, after the last append.
func TestDebounceGrowingFileIsReportedOnceAfterItStops(t *testing.T) {
	const settle = 100 * time.Millisecond
	r := newRig(t, settle)
	p := filepath.Join(r.root, "grow.log")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r.send(OpCreate, "grow.log")
	var last time.Time
	for range 30 {
		if _, err := f.WriteString("line\n"); err != nil {
			t.Fatal(err)
		}
		last = time.Now()
		r.send(OpWrite, "grow.log")
		time.Sleep(10 * time.Millisecond)
	}
	at := r.expectReady("grow.log")
	if at.Sub(last) < settle {
		t.Errorf("reported %v after the last append, want at least one settle period", at.Sub(last))
	}
	r.expectNothing(settle)
}

// Silent growth (no events at all) still defers the report: the second sample differs.
func TestDebounceChangedBetweenSamplesWaitsAgain(t *testing.T) {
	const settle = 100 * time.Millisecond
	r := newRig(t, settle)
	r.file("a.txt", "x")
	start := time.Now()
	r.send(OpWrite, "a.txt")
	time.Sleep(settle * 3 / 2) // the first sample is taken at 1 settle
	if err := os.Chtimes(filepath.Join(r.root, "a.txt"), time.Now(), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	at := r.expectReady("a.txt")
	if at.Sub(start) < settle*5/2 {
		t.Errorf("reported after %v, want at least 2.5 settle periods (sample 2 differed)", at.Sub(start))
	}
}

func TestDebounceDeletedBetweenSamplesIsDropped(t *testing.T) {
	const settle = 100 * time.Millisecond
	r := newRig(t, settle)
	r.file("a.txt", "x")
	r.send(OpWrite, "a.txt")
	time.Sleep(settle * 3 / 2)
	if err := os.Remove(filepath.Join(r.root, "a.txt")); err != nil {
		t.Fatal(err)
	}
	r.expectNothing(settle)
}

func TestDebounceRenamedBetweenSamplesReportsOnlyTheNewName(t *testing.T) {
	const settle = 100 * time.Millisecond
	r := newRig(t, settle)
	r.file("a.txt", "x")
	r.send(OpWrite, "a.txt")
	time.Sleep(settle * 3 / 2)
	if err := os.Rename(filepath.Join(r.root, "a.txt"), filepath.Join(r.root, "b.txt")); err != nil {
		t.Fatal(err)
	}
	r.send(OpRename, "a.txt")
	r.send(OpCreate, "b.txt")
	r.expectReady("b.txt")
	r.expectNothing(settle)
}

func TestDebounceLargeFileWrittenInChunksIsOneReady(t *testing.T) {
	const settle = 100 * time.Millisecond
	r := newRig(t, settle)
	f, err := os.Create(filepath.Join(r.root, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	chunk := make([]byte, 1<<20)
	r.send(OpCreate, "big.bin")
	for range 5 {
		if _, err := f.Write(chunk); err != nil {
			t.Fatal(err)
		}
		r.send(OpWrite, "big.bin")
		time.Sleep(20 * time.Millisecond)
	}
	r.expectReady("big.bin")
	r.expectNothing(settle)
}
