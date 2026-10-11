package sync

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/amitavroy/burrow/internal/drive"
	"github.com/amitavroy/burrow/internal/store"
)

// remoteOf is a fake Drive: content by Drive file ID, and the RemoteFile for it.
type fakeRemote struct {
	content map[string]string // id -> content
	files   []drive.RemoteFile
	got     []string // ids downloaded
	failID  string
}

func (f *fakeRemote) add(id, rel, content string) {
	f.content[id] = content
	f.files = append(f.files, drive.RemoteFile{ID: id, RelPath: rel, Tagged: true, Size: int64(len(content)), MD5: md5Hex(content), RevisionID: "rev-" + id})
}

func newFakeRemote() *fakeRemote { return &fakeRemote{content: map[string]string{}} }

func (f *fakeRemote) download(_ context.Context, id, wantMD5, dst string) error {
	if id == f.failID {
		return errors.New("boom")
	}
	f.got = append(f.got, id)
	if md5Hex(f.content[id]) != wantMD5 {
		return drive.ErrChecksumMismatch
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil { // the real Download does this too
		return err
	}
	return os.WriteFile(dst, []byte(f.content[id]), 0o644)
}

func md5Hex(s string) string { h := md5.Sum([]byte(s)); return hex.EncodeToString(h[:]) }

func restoreOpts() RestoreOptions {
	return RestoreOptions{Host: "laptop", Now: time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)}
}

func runRestore(t *testing.T, root string, repo *store.Repo, fr *fakeRemote) (RestoreSummary, []RestoreEvent) {
	t.Helper()
	var events []RestoreEvent
	sum, err := Restore(context.Background(), root, repo, fr.files, fr.download, func(e RestoreEvent) { events = append(events, e) }, restoreOpts())
	if err != nil {
		t.Fatal(err)
	}
	return sum, events
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The ticket checkpoint: a fresh folder fills from Drive with DB rows created,
// and a following sync finds nothing to upload.
func TestRestoreFillsAFreshFolderAndRecordsRows(t *testing.T) {
	root, repo, fr := t.TempDir(), openRepo(t), newFakeRemote()
	fr.add("f1", "a.txt", "alpha")
	fr.add("f2", "docs/sub/b.txt", "bravo")

	sum, events := runRestore(t, root, repo, fr)

	if sum != (RestoreSummary{Downloaded: 2}) || len(events) != 2 {
		t.Fatalf("sum = %+v, events = %+v", sum, events)
	}
	if readFile(t, filepath.Join(root, "docs/sub/b.txt")) != "bravo" {
		t.Error("nested file not restored")
	}
	row, err := repo.Get(context.Background(), "docs/sub/b.txt")
	if err != nil || row.DriveFileID != "f2" || row.SyncedMD5 != md5Hex("bravo") || row.BaseMD5 != row.SyncedMD5 || row.BaseRevisionID != "rev-f2" || row.Size != 5 {
		t.Errorf("row = %+v, err = %v", row, err)
	}
	if revs, _ := repo.Revisions(context.Background(), "docs/sub/b.txt"); len(revs) != 1 || revs[0].Source != "download" {
		t.Errorf("revisions = %+v", revs)
	}

	// Nothing is new or changed afterwards, so a run/sync would upload nothing.
	plan, err := Diff(root, repo)
	if err != nil || plan.Queued() != 0 || plan.Unchanged != 2 {
		t.Errorf("plan after restore = %+v, err = %v", plan, err)
	}
}

func TestRestoreMatchingFileIsNotTransferred(t *testing.T) {
	root, repo, fr := t.TempDir(), openRepo(t), newFakeRemote()
	fr.add("f1", "a.txt", "alpha")
	write(t, filepath.Join(root, "a.txt"), "alpha")

	sum, events := runRestore(t, root, repo, fr)

	if sum != (RestoreSummary{Matched: 1}) || len(fr.got) != 0 || events[0].Action != RestoreMatched {
		t.Errorf("sum = %+v, downloads = %v, events = %+v", sum, fr.got, events)
	}
	if row, err := repo.Get(context.Background(), "a.txt"); err != nil || row.DriveFileID != "f1" {
		t.Errorf("row = %+v, err = %v", row, err)
	}
}

// Different content: both copies survive, and the original path matches Drive.
func TestRestoreDifferentContentKeepsBoth(t *testing.T) {
	root, repo, fr := t.TempDir(), openRepo(t), newFakeRemote()
	fr.add("f1", "docs/q3.pdf", "drive version")
	write(t, filepath.Join(root, "docs/q3.pdf"), "my local edits")

	sum, events := runRestore(t, root, repo, fr)

	if sum != (RestoreSummary{Conflicts: 1}) {
		t.Fatalf("sum = %+v", sum)
	}
	const copyRel = "docs/q3 (conflicted copy, laptop, 2026-10-11).pdf"
	if events[0].ConflictCopy != copyRel {
		t.Errorf("copy = %q", events[0].ConflictCopy)
	}
	if readFile(t, filepath.Join(root, "docs/q3.pdf")) != "drive version" || readFile(t, filepath.Join(root, filepath.FromSlash(copyRel))) != "my local edits" {
		t.Error("expected Drive's version at the original path and the local one in the copy")
	}
	if row, _ := repo.Get(context.Background(), "docs/q3.pdf"); row.DriveFileID != "f1" || row.SyncedMD5 != md5Hex("drive version") {
		t.Errorf("row = %+v", row)
	}
	if _, err := repo.Get(context.Background(), copyRel); !errors.Is(err, store.ErrNotFound) {
		t.Error("the copy must be a new file for the next sync, not a tracked row")
	}
	plan, _ := Diff(root, repo)
	if !reflect.DeepEqual(plan.New, []string{copyRel}) {
		t.Errorf("next sync would upload %v, want only the copy", plan.New)
	}
}

func TestRestoreConflictCopyNameIsNeverReused(t *testing.T) {
	root, repo, fr := t.TempDir(), openRepo(t), newFakeRemote()
	fr.add("f1", "a.txt", "drive")
	write(t, filepath.Join(root, "a.txt"), "local")
	write(t, filepath.Join(root, "a (conflicted copy, laptop, 2026-10-11).txt"), "earlier copy")

	_, events := runRestore(t, root, repo, fr)

	if events[0].ConflictCopy != "a (conflicted copy, laptop, 2026-10-11 2).txt" {
		t.Errorf("copy = %q", events[0].ConflictCopy)
	}
	if readFile(t, filepath.Join(root, "a (conflicted copy, laptop, 2026-10-11).txt")) != "earlier copy" {
		t.Error("an existing copy was overwritten")
	}
}

// A failed download puts the user's file back where it was.
func TestRestoreFailedDownloadRestoresTheLocalFile(t *testing.T) {
	root, repo, fr := t.TempDir(), openRepo(t), newFakeRemote()
	fr.add("f1", "a.txt", "drive")
	fr.failID = "f1"
	write(t, filepath.Join(root, "a.txt"), "local")

	sum, events := runRestore(t, root, repo, fr)

	if sum.Failed != 1 || events[0].Err == nil {
		t.Fatalf("sum = %+v, events = %+v", sum, events)
	}
	if readFile(t, filepath.Join(root, "a.txt")) != "local" {
		t.Error("local file lost")
	}
	if got, _ := filepath.Glob(filepath.Join(root, "*conflicted*")); len(got) != 0 {
		t.Errorf("leftover copies: %v", got)
	}
	if _, err := repo.Get(context.Background(), "a.txt"); !errors.Is(err, store.ErrNotFound) {
		t.Error("no row should be written for a failed file")
	}
}

func TestRestoreSkipsUnsafeDuplicateAndUnverifiableFiles(t *testing.T) {
	root, repo, fr := t.TempDir(), openRepo(t), newFakeRemote()
	fr.add("f1", "../escape.txt", "x")
	fr.add("f2", "/abs.txt", "x")
	fr.add("f3", "ok.txt", "first")
	fr.add("f4", "ok.txt", "second") // duplicate path
	fr.files = append(fr.files, drive.RemoteFile{ID: "f5", RelPath: "nomd5.txt", Tagged: true})
	fr.add("f6", "good.txt", "fine")
	write(t, filepath.Join(root, "dir", "x"), "") // "dir" is a folder where a file is wanted
	fr.add("f7", "dir", "i am a file")

	sum, events := runRestore(t, root, repo, fr)

	if sum.Failed != 5 || sum.Downloaded != 2 {
		t.Fatalf("sum = %+v, events = %+v", sum, events)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.txt")); err == nil {
		t.Error("a ../ path escaped the root")
	}
	if readFile(t, filepath.Join(root, "ok.txt")) != "first" {
		t.Error("the first duplicate should win")
	}
}

func TestRestoreStopsOnSignInLossAndCancel(t *testing.T) {
	root, repo, fr := t.TempDir(), openRepo(t), newFakeRemote()
	fr.add("f1", "a.txt", "a")
	fr.add("f2", "b.txt", "b")
	lost := func(context.Context, string, string, string) error { return drive.ErrSessionExpired }
	sum, err := Restore(context.Background(), root, repo, fr.files, lost, func(RestoreEvent) {}, restoreOpts())
	if !errors.Is(err, drive.ErrSessionExpired) || sum.Failed != 0 {
		t.Errorf("sign-in loss: sum = %+v, err = %v", sum, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Restore(ctx, root, repo, fr.files, fr.download, func(RestoreEvent) {}, restoreOpts()); !errors.Is(err, context.Canceled) {
		t.Errorf("cancel: err = %v", err)
	}
}
