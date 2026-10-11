package sync

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	stdsync "sync"
	"time"

	"github.com/amitavroy/burrow/internal/store"
)

// RunOptions tune Run. Zero values mean the defaults.
type RunOptions struct {
	Workers    int           // jobs in flight at once; default 3
	Settle     time.Duration // debounce settle period; default 1s
	RetryEvery time.Duration // failed jobs go back to pending this often; default 5m
	PollEvery  time.Duration // an idle worker looks for work at least this often; default 1s

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
	for _, m := range plan.Missing {
		slog.Info("missing locally, not in Drive trash yet", "path", m)
	}
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
	state := &runState{report: report}

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
		return Entry{RelPath: rel, Size: st.Size(), MTime: st.ModTime().UnixNano()}, true
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
