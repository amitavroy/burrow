package sync

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amitavroy/burrow/internal/drive"
)

// panicOnce makes safely panic the first time it runs a place whose name
// matches, for the rest of the test.
func panicOnce(t *testing.T, where string) {
	t.Helper()
	var fired atomic.Bool
	f := func(w string) {
		if w == where && fired.CompareAndSwap(false, true) {
			panic("forced panic at " + w)
		}
	}
	testPanic.Store(&f)
	t.Cleanup(func() { testPanic.Store(nil) })
}

func TestSafelyRecoversAndLogs(t *testing.T) {
	logs := captureLogs(t)

	if !safely("unit", func() { panic("boom") }, "path", "a.txt", "job", 7) {
		t.Fatal("safely did not report the panic")
	}
	if safely("unit", func() {}) {
		t.Fatal("safely reported a panic that did not happen")
	}

	out := logs.String()
	for _, want := range []string{"panic recovered", "goroutine=unit", "panic=boom", "path=a.txt", "job=7", "safe_test.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
}

// The ticket checkpoint: a forced panic is logged and the daemon keeps running.
func TestRunSurvivesAPanickingUpload(t *testing.T) {
	logs := captureLogs(t)
	fd := &fakeDrive{before: func(_ context.Context, _ int, rel string) error {
		if rel == "bad.txt" {
			panic("nil pointer in the upload path")
		}
		return nil
	}}
	repo := openRepo(t)
	r := startRun(t, t.TempDir(), repo, fd, RunOptions{RetryEvery: time.Hour})

	r.file("bad.txt", "SECRETCONTENT")
	eventually(t, "the failure is recorded", func() bool { return errorsOf(t, repo)["bad.txt"].Message != "" })
	r.file("good.txt", "fine")

	eventually(t, "the next file uploads", func() bool { return fd.count("good.txt") == 1 })
	if e := errorsOf(t, repo)["bad.txt"]; e.Message != "internal error (see log)" {
		t.Errorf("error row = %+v", e)
	}
	if err := r.stop(); err != nil {
		t.Fatalf("Run = %v, want a normal stop", err)
	}
	out := logs.String()
	if !strings.Contains(out, "panic recovered") || !strings.Contains(out, "goroutine=worker") ||
		!strings.Contains(out, "path=bad.txt") || !strings.Contains(out, "nil pointer in the upload path") {
		t.Errorf("panic not logged as expected:\n%s", out)
	}
	if strings.Contains(out, "SECRETCONTENT") {
		t.Error("the log contains file content")
	}
}

func TestSyncSurvivesAPanickingUpload(t *testing.T) {
	root := syncTree(t)
	repo := openRepo(t)
	u := func(ctx context.Context, p, rel string) (drive.FileInfo, error) {
		if rel == "a/b.txt" {
			panic("boom")
		}
		return okUpload(ctx, p, rel)
	}
	var events []Event

	sum, err := Sync(context.Background(), root, repo, u, collect(&events))

	if err != nil || sum.Uploaded != 2 || sum.Failed != 1 {
		t.Fatalf("sum = %+v, err = %v; want the other two uploaded, one failed", sum, err)
	}
	if e := errorsOf(t, repo)["a/b.txt"]; e.Message != "internal error (see log)" {
		t.Errorf("error row = %+v", e)
	}
	if n, _ := repo.CountJobs(context.Background()); n != 0 {
		t.Errorf("%d jobs still queued; the panicked job must be marked failed", n)
	}
}

// A panic in a report callback must not kill the worker either.
func TestRunSurvivesAPanickingReport(t *testing.T) {
	captureLogs(t)
	root := t.TempDir()
	fd := &fakeDrive{}
	repo := openRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	first := true
	go func() {
		done <- Run(ctx, root, repo, fd.upload, func(Event) {
			if first {
				first = false
				panic("report exploded")
			}
		}, RunOptions{Settle: 40 * time.Millisecond, PollEvery: 20 * time.Millisecond})
	}()
	t.Cleanup(func() { cancel(); <-done })
	write(t, filepath.Join(root, "a.txt"), "1")
	eventually(t, "first upload", func() bool { return fd.count("a.txt") >= 1 })

	write(t, filepath.Join(root, "b.txt"), "2")

	eventually(t, "second file still uploads", func() bool { return fd.count("b.txt") == 1 })
}

// A panic forced in each long-lived loop leaves Run working.
func TestRunSurvivesPanicsInItsLoops(t *testing.T) {
	for _, where := range []string{"watcher.event", "debouncer.event", "debouncer.fire", "run.feeder", "run.retry", "run.watch-errors"} {
		t.Run(where, func(t *testing.T) {
			logs := captureLogs(t)
			panicOnce(t, where)
			fd := &fakeDrive{}
			r := startRun(t, t.TempDir(), openRepo(t), fd, RunOptions{RetryEvery: 50 * time.Millisecond})

			// Each loop sees traffic: the first event or tick may be lost to the
			// panic, so keep saving until a file gets through.
			deadline := time.Now().Add(3 * time.Second)
			for i := 0; fd.total() == 0 && time.Now().Before(deadline); i++ {
				r.file("f"+string(rune('a'+i%26))+".txt", "x")
				time.Sleep(150 * time.Millisecond)
			}

			if fd.total() == 0 {
				t.Fatal("nothing uploaded after the panic")
			}
			if where != "run.watch-errors" && !strings.Contains(logs.String(), "goroutine="+where) {
				t.Errorf("panic at %s not logged:\n%s", where, logs.String())
			}
			if err := r.stop(); err != nil {
				t.Fatalf("Run = %v", err)
			}
		})
	}
}
