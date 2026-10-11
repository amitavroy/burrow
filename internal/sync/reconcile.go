package sync

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/amitavroy/burrow/internal/store"
)

// Plan is what a scan of the sync root found, compared with the files table.
type Plan struct {
	// New files have no row, or a row without a Drive ID (an upload that never
	// finished). Changed files have a row whose size or mtime differs.
	New     []string
	Changed []string
	// Missing are rows whose file is not in the scan: deleted, renamed, or in a
	// folder that could not be read or is no longer mounted. They are only
	// reported; trashing them in Drive is a later ticket.
	Missing   []string
	Unchanged int
	Ignored   int
	// Unreadable are entries the scan could not read; their files are in none
	// of the lists above.
	Unreadable []error

	// inodes are unchanged files whose row has no inode or a different one;
	// Reconcile refreshes them, Diff only reports.
	inodes map[string]uint64
}

// Queued is how many files a Reconcile queues.
func (p Plan) Queued() int { return len(p.New) + len(p.Changed) }

// Diff scans root and compares it with the files table. It writes nothing. A
// file is unchanged when its size and mtime match its row; the job that
// uploads a changed file still checks its MD5, so a touched file costs a read
// and no upload. Every list is sorted.
func Diff(root string, repo *store.Repo) (Plan, error) {
	plan, _, err := diff(context.Background(), root, repo)
	return plan, err
}

func diff(ctx context.Context, root string, repo *store.Repo) (Plan, Result, error) {
	res, err := Scan(root)
	if err != nil {
		return Plan{}, Result{}, err
	}
	rows, err := repo.List(ctx)
	if err != nil {
		return Plan{}, Result{}, err
	}
	byPath := make(map[string]store.File, len(rows))
	for _, r := range rows {
		byPath[r.RelPath] = r
	}
	plan := Plan{Ignored: res.Ignored, Unreadable: res.Errors}
	seen := make(map[string]bool, len(res.Entries))
	for _, e := range res.Entries {
		seen[e.RelPath] = true
		row, ok := byPath[e.RelPath]
		switch {
		case !ok || row.DriveFileID == "":
			plan.New = append(plan.New, e.RelPath)
		case row.Size != e.Size || row.MTime != e.MTime:
			plan.Changed = append(plan.Changed, e.RelPath)
		default:
			plan.Unchanged++
			if e.Inode != 0 && row.Inode != e.Inode {
				if plan.inodes == nil {
					plan.inodes = map[string]uint64{}
				}
				plan.inodes[e.RelPath] = e.Inode
			}
		}
	}
	// Rows under a folder the scan could not read are not "missing": nobody
	// looked there.
	var blind []string
	for _, serr := range res.Errors {
		if rel, ok := relOfError(root, serr); ok {
			blind = append(blind, rel)
		}
	}
	for _, r := range rows {
		if seen[r.RelPath] || under(r.RelPath, blind) {
			continue
		}
		plan.Missing = append(plan.Missing, r.RelPath)
	}
	sort.Strings(plan.New)
	sort.Strings(plan.Changed)
	sort.Strings(plan.Missing)
	return plan, res, nil
}

func under(rel string, dirs []string) bool {
	for _, d := range dirs {
		if rel == d || strings.HasPrefix(rel, d+"/") {
			return true
		}
	}
	return false
}

// relOfError is the slash-form rel_path of the entry a scan error is about.
func relOfError(root string, err error) (string, bool) {
	var pe *fs.PathError
	if !errors.As(err, &pe) {
		return "", false
	}
	rel, rerr := filepath.Rel(root, pe.Path)
	if rerr != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// Reconcile is Diff plus its effects: unreadable entries get error rows (and
// stale error rows are dropped, as in Sync), and the new and changed files are
// queued as jobs. Unchanged files are not queued. Calling it again queues the
// same paths again as one job each.
func Reconcile(ctx context.Context, root string, repo *store.Repo) (Plan, error) {
	plan, res, err := diff(ctx, root, repo)
	if err != nil {
		return Plan{}, err
	}
	entries := make(map[string]Entry, len(res.Entries))
	for _, e := range res.Entries {
		entries[e.RelPath] = e
	}
	if err := noteScanErrors(ctx, root, repo, res, entries); err != nil {
		return plan, err
	}
	if err := repo.SetInodes(ctx, plan.inodes); err != nil {
		return plan, err
	}
	if err := repo.EnqueueJobs(ctx, append(append([]string(nil), plan.New...), plan.Changed...)); err != nil {
		return plan, err
	}
	return plan, nil
}
