package sync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	stdsync "sync"
	"testing"
	"time"

	"github.com/amitavroy/burrow/internal/drive"
	"github.com/amitavroy/burrow/internal/store"
)

// fakeDrive records uploads for Run tests. before, when set, runs at the start
// of every upload and may block or fail it.
type fakeDrive struct {
	mu     stdsync.Mutex
	calls  []string
	before func(ctx context.Context, call int, rel string) error
}

func (f *fakeDrive) upload(ctx context.Context, _, rel string) (drive.FileInfo, error) {
	f.mu.Lock()
	f.calls = append(f.calls, rel)
	n := len(f.calls)
	before := f.before
	f.mu.Unlock()
	if before != nil {
		if err := before(ctx, n, rel); err != nil {
			return drive.FileInfo{}, err
		}
	}
	return drive.FileInfo{ID: "id-" + rel, MD5: "m", RevisionID: "r"}, nil
}

func (f *fakeDrive) count(rel string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == rel {
			n++
		}
	}
	return n
}

func (f *fakeDrive) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// runRig runs Run in the background over a temp root.
type runRig struct {
	t      *testing.T
	root   string
	repo   *store.Repo
	fd     *fakeDrive
	cancel context.CancelFunc
	done   chan error
}

func startRun(t *testing.T, root string, repo *store.Repo, fd *fakeDrive, opts RunOptions) *runRig {
	t.Helper()
	if opts.Settle == 0 {
		opts.Settle = 40 * time.Millisecond
	}
	if opts.PollEvery == 0 {
		opts.PollEvery = 20 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &runRig{t: t, root: root, repo: repo, fd: fd, cancel: cancel, done: make(chan error, 1)}
	started := make(chan struct{})
	opts.OnStart = func(int) { close(started) }
	go func() { r.done <- Run(ctx, root, repo, fd.upload, func(Event) {}, opts) }()
	select {
	case <-started:
	case err := <-r.done:
		t.Fatalf("Run ended early: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not start")
	}
	t.Cleanup(func() { cancel(); <-r.done })
	return r
}

// stop cancels and returns what Run returned, failing if it takes too long.
func (r *runRig) stop() error {
	r.t.Helper()
	r.cancel()
	select {
	case err := <-r.done:
		r.done <- err // for Cleanup
		return err
	case <-time.After(3 * time.Second):
		r.t.Fatal("Run did not stop")
	}
	return nil
}

func (r *runRig) file(rel, content string) {
	write(r.t, filepath.Join(r.root, filepath.FromSlash(rel)), content)
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// stays asserts cond holds for a while, e.g. "no second upload appears".
func stays(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if !cond() {
			t.Fatalf("%s did not hold", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunUploadsAFileSavedAfterStart(t *testing.T) {
	fd := &fakeDrive{}
	r := startRun(t, t.TempDir(), openRepo(t), fd, RunOptions{})

	r.file("notes.txt", "hello")

	eventually(t, "upload", func() bool { return fd.count("notes.txt") == 1 })
	stays(t, "a single upload", 400*time.Millisecond, func() bool { return fd.count("notes.txt") == 1 })
	eventually(t, "job done", func() bool { n, _ := r.repo.CountJobs(context.Background()); return n == 0 })
	if err := r.stop(); err != nil {
		t.Fatalf("Run = %v, want nil on cancel", err)
	}
}

func TestRunBurstOfWritesIsOneUpload(t *testing.T) {
	fd := &fakeDrive{}
	r := startRun(t, t.TempDir(), openRepo(t), fd, RunOptions{})

	r.file("a.txt", "x")
	for range 20 {
		r.file("a.txt", "xx more")
		time.Sleep(5 * time.Millisecond)
	}

	eventually(t, "upload", func() bool { return fd.count("a.txt") >= 1 })
	stays(t, "one upload for the burst", 500*time.Millisecond, func() bool { return fd.count("a.txt") == 1 })
}

func TestRunEditorStyleSaveIsOneUpload(t *testing.T) {
	root := t.TempDir()
	fd := &fakeDrive{}
	r := startRun(t, root, openRepo(t), fd, RunOptions{})

	tmp, final := filepath.Join(root, "doc.txt.tmp"), filepath.Join(root, "doc.txt")
	write(t, tmp, "half")
	write(t, tmp, "the whole document")
	if err := os.Rename(tmp, final); err != nil {
		t.Fatal(err)
	}

	eventually(t, "upload", func() bool { return fd.count("doc.txt") == 1 })
	stays(t, "one upload, none for the temp file", 500*time.Millisecond, func() bool { return fd.total() == 1 })
	_ = r
}

func TestRunUploadsAFileInABrandNewFolder(t *testing.T) {
	fd := &fakeDrive{}
	r := startRun(t, t.TempDir(), openRepo(t), fd, RunOptions{})

	r.file("x/y/z/f.txt", "deep")

	eventually(t, "upload", func() bool { return fd.count("x/y/z/f.txt") == 1 })
}

func TestRunProcessesLeftoverJobsAtStart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	write(t, filepath.Join(root, "left.txt"), "1")
	write(t, filepath.Join(root, "ran.txt"), "1")
	repo := openRepo(t)
	_ = repo.EnqueueJobs(ctx, []string{"left.txt", "ran.txt", "ghost.txt"})
	_, _ = repo.ClaimJob(ctx) // left.txt was running when the process died
	fd := &fakeDrive{}

	startRun(t, root, repo, fd, RunOptions{})

	eventually(t, "leftover uploads", func() bool { return fd.count("left.txt") == 1 && fd.count("ran.txt") == 1 })
	eventually(t, "queue empty", func() bool { n, _ := repo.CountJobs(ctx); return n == 0 })
	if fd.count("ghost.txt") != 0 {
		t.Error("the job for a file that does not exist was uploaded")
	}
}

func TestRunIgnoresIgnoredFiles(t *testing.T) {
	fd := &fakeDrive{}
	r := startRun(t, t.TempDir(), openRepo(t), fd, RunOptions{})

	r.file("cache.tmp", "x")
	r.file("real.txt", "x")

	eventually(t, "real upload", func() bool { return fd.count("real.txt") == 1 })
	if fd.count("cache.tmp") != 0 {
		t.Error("an ignored file was uploaded")
	}
}

// Ctrl+C: Run returns promptly with nil, the upload in flight goes back in the queue.
func TestRunCancelRequeuesTheJobInFlight(t *testing.T) {
	fd := &fakeDrive{before: func(ctx context.Context, _ int, _ string) error {
		<-ctx.Done() // a slow upload
		return ctx.Err()
	}}
	repo := openRepo(t)
	r := startRun(t, t.TempDir(), repo, fd, RunOptions{})
	r.file("big.bin", "x")
	eventually(t, "upload started", func() bool { return fd.total() == 1 })

	start := time.Now()
	if err := r.stop(); err != nil {
		t.Fatalf("Run = %v, want nil on cancel", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v to stop", d)
	}
	if n, _ := repo.CountJobs(context.Background()); n != 1 {
		t.Errorf("queued = %d, want the cancelled job back in the queue", n)
	}
	if fd.total() != 1 {
		t.Errorf("%d uploads, want no more after the cancel", fd.total())
	}
}

// A file saved again while its upload runs is uploaded again afterwards, and no more than that.
func TestRunResaveDuringUploadIsUploadedAgain(t *testing.T) {
	release := make(chan struct{})
	fd := &fakeDrive{before: func(ctx context.Context, call int, _ string) error {
		if call == 1 {
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}}
	r := startRun(t, t.TempDir(), openRepo(t), fd, RunOptions{})
	r.file("a.txt", "one")
	eventually(t, "first upload started", func() bool { return fd.total() == 1 })

	r.file("a.txt", "two, saved while the first upload runs")
	time.Sleep(400 * time.Millisecond) // long enough to be debounced and queued
	close(release)

	eventually(t, "second upload", func() bool { return fd.count("a.txt") == 2 })
	stays(t, "exactly two uploads", 500*time.Millisecond, func() bool { return fd.count("a.txt") == 2 })
}

// A failed job is queued again while running, and its error clears on success.
func TestRunRetriesFailedJobs(t *testing.T) {
	fd := &fakeDrive{before: func(_ context.Context, call int, _ string) error {
		if call == 1 {
			return errors.New("network problem")
		}
		return nil
	}}
	repo := openRepo(t)
	r := startRun(t, t.TempDir(), repo, fd, RunOptions{RetryEvery: 100 * time.Millisecond})

	r.file("a.txt", "x")

	eventually(t, "second attempt", func() bool { return fd.count("a.txt") == 2 })
	eventually(t, "error cleared", func() bool { l, _ := repo.ListErrors(context.Background()); return len(l) == 0 })
	eventually(t, "queue empty", func() bool { n, _ := repo.CountJobs(context.Background()); return n == 0 })
}

// A lost sign-in stops the run with that error and keeps the job.
func TestRunSignInLostStopsAndKeepsTheJob(t *testing.T) {
	fd := &fakeDrive{before: func(context.Context, int, string) error { return drive.ErrSessionExpired }}
	repo := openRepo(t)
	r := startRun(t, t.TempDir(), repo, fd, RunOptions{})
	r.file("a.txt", "x")

	select {
	case err := <-r.done:
		r.done <- err
		if !errors.Is(err, drive.ErrSessionExpired) {
			t.Fatalf("Run = %v, want ErrSessionExpired", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop on a lost sign-in")
	}
	if n, _ := repo.CountJobs(context.Background()); n != 1 {
		t.Errorf("queued = %d, want the job kept", n)
	}
}

func TestRunMissingRootIsAnError(t *testing.T) {
	err := Run(context.Background(), filepath.Join(t.TempDir(), "nope"), openRepo(t), (&fakeDrive{}).upload, func(Event) {}, RunOptions{})
	if err == nil {
		t.Fatal("want an error for a missing root")
	}
}

// The ticket checkpoint: files changed while stopped sync on the next start.
func TestRunReconcilesChangesMadeWhileStopped(t *testing.T) {
	root := t.TempDir()
	repo := openRepo(t)
	write(t, filepath.Join(root, "keep.txt"), "keep")
	write(t, filepath.Join(root, "edit.txt"), "v1")
	synced(t, repo, root, "keep.txt")
	synced(t, repo, root, "edit.txt")

	// "Stopped": change things with no run going.
	write(t, filepath.Join(root, "edit.txt"), "v2, edited while stopped")
	write(t, filepath.Join(root, "added/new.txt"), "added while stopped")

	fd := &fakeDrive{}
	var plan Plan
	started := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, root, repo, fd.upload, func(Event) {}, RunOptions{
			Settle: 40 * time.Millisecond, PollEvery: 20 * time.Millisecond,
			OnReconcile: func(p Plan) { plan = p; close(started) },
		})
	}()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("no reconcile")
	}

	eventually(t, "both changes uploaded", func() bool { return fd.count("edit.txt") == 1 && fd.count("added/new.txt") == 1 })
	stays(t, "nothing else is uploaded", 400*time.Millisecond, func() bool { return fd.total() == 2 })
	if len(plan.New) != 1 || len(plan.Changed) != 1 || plan.Unchanged != 1 {
		t.Errorf("plan = %+v", plan)
	}
}

func TestRunRestartWithNoChangesUploadsNothing(t *testing.T) {
	root := t.TempDir()
	repo := openRepo(t)
	write(t, filepath.Join(root, "a.txt"), "a")
	synced(t, repo, root, "a.txt")
	fd := &fakeDrive{}

	r := startRun(t, root, repo, fd, RunOptions{})

	stays(t, "no uploads", 400*time.Millisecond, func() bool { return fd.total() == 0 })
	_ = r
}

// A file whose last upload failed is retried on the next start: its row is missing, so it is new.
func TestRunStartRetriesAFileThatFailedLastTime(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := openRepo(t)
	write(t, filepath.Join(root, "a.txt"), "a")
	_ = repo.EnqueueJobs(ctx, []string{"a.txt"})
	j, _ := repo.ClaimJob(ctx)
	_ = repo.FailJob(ctx, j.ID, "network problem")
	fd := &fakeDrive{}

	startRun(t, root, repo, fd, RunOptions{})

	eventually(t, "retry on start", func() bool { return fd.count("a.txt") == 1 })
}

// A file created after the watcher is up but before the reconcile has queued
// anything is uploaded exactly once, whether the watcher, the scan or both saw it.
func TestRunFileCreatedDuringStartIsUploadedOnce(t *testing.T) {
	root := t.TempDir()
	repo := openRepo(t)
	fd := &fakeDrive{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, root, repo, fd.upload, func(Event) {}, RunOptions{
			Settle: 40 * time.Millisecond, PollEvery: 20 * time.Millisecond,
			OnStart: func(int) { write(t, filepath.Join(root, "racy.txt"), "created between watch and scan") },
		})
	}()
	t.Cleanup(func() { cancel(); <-done })

	eventually(t, "upload", func() bool { return fd.count("racy.txt") >= 1 })
	stays(t, "exactly one upload", 500*time.Millisecond, func() bool { return fd.count("racy.txt") == 1 })
}
