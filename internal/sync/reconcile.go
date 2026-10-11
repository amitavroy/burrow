package sync

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
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
	// Moved are likely renames: a new file whose inode and size equal those of
	// exactly one missing row. Found without hashing, so it is a preview; the
	// job that handles the new path confirms it by hash. Such files are in
	// neither New nor Missing.
	Moved []Move
	// Missing are rows whose file is not in the scan: deleted, renamed, or in a
	// folder that could not be read or is no longer mounted. They are only
	// queued as jobs, which trash their Drive file when the run has a TrashFunc.
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

// Move is a likely rename of a tracked file.
type Move struct{ From, To string }

// Queued is how many files a Reconcile queues. A move is queued under its new
// path.
func (p Plan) Queued() int { return len(p.New) + len(p.Changed) + len(p.Moved) + len(p.Missing) }

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
	pairMoves(&plan, res.Entries, byPath)
	sort.Strings(plan.New)
	sort.Strings(plan.Changed)
	sort.Strings(plan.Missing)
	sort.Slice(plan.Moved, func(i, j int) bool { return plan.Moved[i].To < plan.Moved[j].To })
	return plan, res, nil
}

// pairMoves moves a new file out of plan.New, and its counterpart out of
// plan.Missing, into plan.Moved when the file has a known inode that exactly
// one missing tracked row also has, and the sizes agree. Anything ambiguous
// stays new and missing.
func pairMoves(plan *Plan, entries []Entry, rows map[string]store.File) {
	missingByInode := map[uint64][]string{}
	for _, m := range plan.Missing {
		if r := rows[m]; r.Inode != 0 && r.DriveFileID != "" {
			missingByInode[r.Inode] = append(missingByInode[r.Inode], m)
		}
	}
	entryOf := make(map[string]Entry, len(entries))
	newByInode := map[uint64]int{}
	for _, e := range entries {
		entryOf[e.RelPath] = e
	}
	for _, n := range plan.New {
		if ino := entryOf[n].Inode; ino != 0 {
			newByInode[ino]++
		}
	}
	paired := map[string]bool{} // new and missing paths that became a move
	for _, n := range plan.New {
		e := entryOf[n]
		from := missingByInode[e.Inode]
		if e.Inode == 0 || len(from) != 1 || newByInode[e.Inode] != 1 || rows[from[0]].Size != e.Size {
			continue
		}
		plan.Moved = append(plan.Moved, Move{From: from[0], To: n})
		paired[n], paired[from[0]] = true, true
	}
	if len(paired) == 0 {
		return
	}
	plan.New = slices.DeleteFunc(plan.New, func(p string) bool { return paired[p] })
	plan.Missing = slices.DeleteFunc(plan.Missing, func(p string) bool { return paired[p] })
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
	queue := append(append([]string(nil), plan.New...), plan.Changed...)
	for _, m := range plan.Moved {
		queue = append(queue, m.To) // the job confirms the move by hash
	}
	queue = append(queue, plan.Missing...) // the job trashes its Drive file
	if err := repo.EnqueueJobs(ctx, queue); err != nil {
		return plan, err
	}
	return plan, nil
}
