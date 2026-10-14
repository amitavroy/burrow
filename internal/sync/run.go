package sync

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	stdsync "sync"
	"time"

	"github.com/amitavroy/burrow/internal/drive"
	"github.com/amitavroy/burrow/internal/store"
)

// RunOptions tune Run. Zero values mean the defaults.
type RunOptions struct {
	Workers    int           // jobs in flight at once; default 3
	Settle     time.Duration // debounce settle period; default 1s
	RetryEvery time.Duration // failed jobs go back to pending this often; default 5m
	Move       MoveFunc      // moves a renamed file in Drive instead of uploading it; nil: no move detection
	MoveDir    MoveDirFunc   // moves a renamed folder in Drive; nil: files move one by one
	Control    *Control      // lets the control API pause, resume and read the last sync; nil: not steerable
	Trash      TrashFunc     // trashes the Drive file of a deleted file; nil: deletes are not propagated
	PollEvery  time.Duration // an idle worker looks for work at least this often; default 1s
	Changes    *ChangeSource // applies edits made on Drive (Pull); nil: one-way, Drive edits are ignored

	// Remote lists every file in Drive. With Changes set, Run calls it once, when
	// no changes token is stored yet (a database that has never seen Drive), and
	// restores the root from the list before it starts watching. nil: no restore.
	Remote func(ctx context.Context) ([]drive.RemoteFile, error)
	// OnRestore is called for each file the first-run restore handles.
	OnRestore func(RestoreEvent)

	// OnStart is called once the watch is up, with the number of directories
	// watched. OnWatchError is called for each problem the watcher reports (a
	// queue overflow, a folder that could not be watched); the run goes on.
	OnStart      func(dirs int)
	OnWatchError func(error)
	// OnReconcile is called once with what the startup scan found, before the
	// workers start.
	OnReconcile func(Plan)
}

func (o *RunOptions) defaults() {
	if o.Workers < 1 {
		o.Workers = 3
	}
	if o.Settle <= 0 {
		o.Settle = time.Second
	}
	if o.RetryEvery <= 0 {
		o.RetryEvery = 5 * time.Minute
	}
	if o.PollEvery <= 0 {
		o.PollEvery = time.Second
	}
}

// Run watches root and uploads what changes until ctx is cancelled, which is a
// normal stop (it returns nil). The pipeline is Watcher -> Debouncer -> job
// queue -> workers, the same workers and job rules as Sync. Jobs left over
// from an earlier run are processed first, then the root is reconciled with
// the files table (Reconcile) so changes made while stopped are queued. Jobs that failed are queued again every
// RetryEvery. A cancel puts the jobs in flight back in the queue. It returns an
// error, after the same requeueing, when the sign-in is gone
// (drive.ErrNotSignedIn or drive.ErrSessionExpired), when repo fails, or when
// the root cannot be watched. report is never called concurrently.
func Run(ctx context.Context, root string, repo *store.Repo, upload UploadFunc, report func(Event), opts RunOptions) error {
	opts.defaults()
	// No other process uses the database, so a job still marked running is
	// one a dead process left behind.
	if _, err := repo.ResetRunningJobs(ctx); err != nil {
		return err
	}
	if err := firstRunRestore(ctx, root, repo, opts); err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return nil // the caller cancelled: a normal stop; the restore resumes next start
		}
		return err
	}
	ignore, err := NewMatcher(root)
	if err != nil {
		return err
	}
	w, err := NewWatcher(root)
	if err != nil {
		return err
	}
	defer w.Close()
	d := NewDebouncer(w.Events(), root, opts.Settle)
	defer d.Close()
	if opts.OnStart != nil {
		opts.OnStart(w.Watching())
	}
	// The watcher is already running, so a change made during the walk is seen
	// by one or the other (or both, which is still one job).
	plan, err := Reconcile(ctx, root, repo)
	if err != nil {
		return err
	}
	slog.Info("reconciled", "new", len(plan.New), "changed", len(plan.Changed),
		"unchanged", plan.Unchanged, "missing", len(plan.Missing))
	if opts.OnReconcile != nil {
		opts.OnReconcile(plan)
	}

	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		once  stdsync.Once
		fatal error
		wg    stdsync.WaitGroup
	)
	stop := func(err error) { once.Do(func() { fatal = err; cancel() }) }
	wake := make(chan struct{}, opts.Workers)
	kick := func(n int) {
		for range min(n, opts.Workers) {
			select {
			case wake <- struct{}{}:
			default:
			}
		}
	}
	exp := &expected{}
	state := &runState{expect: exp, report: report, move: opts.Move, moveDir: opts.MoveDir, trash: opts.Trash, ctl: opts.Control}

	// Ready paths become jobs.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case p, ok := <-d.Ready():
				if !ok {
					return
				}
				safely("run.feeder", func() {
					if err := repo.EnqueueJobs(ctx, []string{p}); err != nil {
						stop(err)
						return
					}
					kick(1)
				}, "path", p)
			}
		}
	}()

	// A delete or rename is followed by a rescan once things go quiet, long
	// enough (3 settle periods) for the new path of a rename to have been
	// debounced and its move job started, so Diff pairs the two as a move
	// instead of reporting the old path as deleted. Missing files become jobs
	// that trash their Drive file.
	// ponytail: full walk per burst of deletes; a per-path check if big trees make it slow.
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTimer(time.Hour)
		t.Stop()
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-d.Removed():
				t.Reset(3 * opts.Settle)
			case <-t.C:
				safely("run.rescan", func() {
					plan, err := Reconcile(ctx, root, repo)
					if err != nil {
						if ctx.Err() == nil {
							stop(err)
						}
						return
					}
					kick(plan.Queued())
				})
			}
		}
	}()

	// Failed jobs are tried again now and then.
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(opts.RetryEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				safely("run.retry", func() {
					n, err := repo.RetryFailedJobs(ctx)
					if err != nil {
						stop(err)
						return
					}
					if n > 0 {
						slog.Info("retrying failed jobs", "count", n)
						kick(n)
					}
				})
			}
		}
	}()

	// Edits made on Drive are pulled now and then.
	if src := opts.Changes; src != nil {
		every := src.Every
		if every <= 0 {
			every = 30 * time.Second
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(every)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if opts.Control.Paused() {
						continue
					}
					safely("run.pull", func() {
						err := pollChanges(ctx, root, repo, src, exp)
						switch {
						case err == nil, ctx.Err() != nil:
						case errors.Is(err, drive.ErrNotSignedIn), errors.Is(err, drive.ErrSessionExpired):
							stop(err)
						default:
							slog.Warn("pulling Drive changes failed, will retry", "err", err)
						}
					})
				}
			}
		}()
	}

	// Watcher problems are reported, not fatal.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case err, ok := <-w.Errors():
				if !ok {
					return
				}
				safely("run.watch-errors", func() {
					slog.Warn("watcher problem", "err", err)
					if opts.OnWatchError != nil {
						opts.OnWatchError(err)
					}
				})
			}
		}
	}()

	lookup := func(rel string) (Entry, bool) {
		st, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || !st.Mode().IsRegular() || ignore.Ignored(rel, false) {
			return Entry{}, false
		}
		return Entry{RelPath: rel, Size: st.Size(), MTime: st.ModTime().UnixNano(), Inode: fileID(filepath.Join(root, filepath.FromSlash(rel)), st)}, true
	}
	idle := func(ctx context.Context) error {
		t := time.NewTimer(opts.PollEvery)
		defer t.Stop()
		select {
		case <-wake:
		case <-t.C:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	}
	for range opts.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := worker(ctx, root, repo, upload, state, lookup, idle); err != nil {
				stop(err)
			}
		}()
	}
	wg.Wait()
	if errors.Is(fatal, context.Canceled) && parent.Err() != nil {
		return nil // the caller cancelled: a normal stop
	}
	return fatal
}

// firstRunRestore fills the root from Drive when the database has no changes
// token, which means it has never seen Drive (a new machine, or the database
// was deleted). It runs before the watcher starts, so the files it downloads
// are never mistaken for local edits and uploaded back; a file the user adds
// meanwhile is found by the reconcile that follows.
//
// The changes position is taken before the list, so an edit made in Drive
// during the restore shows up in the first poll, and it is stored only after
// the pass finishes: a stop in the middle restores again next start, and
// Restore skips what is already in place. A file that failed is left in
// sync_errors (run `syncd restore` to try it again); only a lost sign-in, a
// cancel, or a failing database or Drive listing ends the pass, and then the
// token stays unset so the next start tries again.
func firstRunRestore(ctx context.Context, root string, repo *store.Repo, opts RunOptions) error {
	src := opts.Changes
	if src == nil || opts.Remote == nil {
		return nil
	}
	if tok, _, err := repo.GetMeta(ctx, store.MetaChangesToken); err != nil || tok != "" {
		return err
	}
	start, err := src.Start(ctx)
	if err != nil {
		return err
	}
	remote, err := opts.Remote(ctx)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	slog.Info("restoring from Drive", "files", len(remote))
	done := 0
	opts.Control.restoreProgress(0, len(remote))
	defer opts.Control.restoreProgress(0, 0)
	report := func(e RestoreEvent) {
		done++
		opts.Control.restoreProgress(done, len(remote))
		if e.Err != nil {
			if rerr := repo.RecordError(ctx, e.RelPath, drive.Describe(e.Err), e.Err.Error()); rerr != nil {
				slog.Warn("could not record restore error", "rel", e.RelPath, "err", rerr)
			}
		}
		if opts.OnRestore != nil {
			opts.OnRestore(e)
		}
	}
	sum, err := Restore(ctx, root, repo, remote, src.Download, report, RestoreOptions{Host: src.Host})
	slog.Info("restore finished", "downloaded", sum.Downloaded, "matched", sum.Matched, "conflicts", sum.Conflicts, "failed", sum.Failed)
	if err != nil {
		return err
	}
	return repo.SetMeta(context.WithoutCancel(ctx), store.MetaChangesToken, start)
}
