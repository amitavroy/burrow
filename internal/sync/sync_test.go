package sync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/amitavroy/burrow/internal/drive"
	"github.com/amitavroy/burrow/internal/store"
)

// uploads records the calls a fake UploadFunc receives.
type uploads struct {
	rels   []string
	paths  []string
	failOn map[string]error // rel_path -> error to return
	after  func(rel string) // runs after each call (to cancel, for example)
}

func (u *uploads) fn(_ context.Context, localPath, relPath string) (drive.FileInfo, error) {
	u.rels = append(u.rels, relPath)
	u.paths = append(u.paths, localPath)
	if u.after != nil {
		defer u.after(relPath)
	}
	if err := u.failOn[relPath]; err != nil {
		return drive.FileInfo{}, err
	}
	return drive.FileInfo{ID: "id-" + relPath, MD5: "md5-" + relPath, RevisionID: "rev-" + relPath, RelPath: relPath}, nil
}

func syncTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, rel := range []string{"z.txt", "a/b.txt", "a.txt", "skip.tmp", "node_modules/x.js"} {
		write(t, filepath.Join(root, filepath.FromSlash(rel)), "x")
	}
	return root
}

func openRepo(t *testing.T) *store.Repo {
	t.Helper()
	r, err := store.OpenRepo(filepath.Join(t.TempDir(), "burrow.db"))
	if err != nil {
		t.Fatalf("OpenRepo: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func collect(events *[]Event) func(Event) {
	return func(e Event) { *events = append(*events, e) }
}

func TestSyncUploadsInOrderAndSkipsIgnored(t *testing.T) {
	root := syncTree(t)
	u := &uploads{}
	var events []Event

	sum, err := Sync(context.Background(), root, openRepo(t), u.fn, collect(&events))
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"a.txt", "a/b.txt", "z.txt"}
	if !reflect.DeepEqual(u.rels, want) {
		t.Errorf("uploaded %v, want %v", u.rels, want)
	}
	if u.paths[1] != filepath.Join(root, "a", "b.txt") {
		t.Errorf("local path = %q, want it under the root", u.paths[1])
	}
	if sum.Uploaded != 3 || sum.Failed != 0 || sum.Ignored != 2 {
		t.Errorf("summary = %+v, want 3 uploaded, 0 failed, 2 ignored", sum)
	}
	if len(events) != 3 || events[0].RelPath != "a.txt" || events[0].Err != nil {
		t.Errorf("events = %+v", events)
	}
}

func TestSyncCarriesOnPastAFileError(t *testing.T) {
	root := syncTree(t)
	boom := errors.New("quota exceeded")
	u := &uploads{failOn: map[string]error{"a/b.txt": boom}}
	var events []Event

	sum, err := Sync(context.Background(), root, openRepo(t), u.fn, collect(&events))

	if err != nil {
		t.Fatalf("a per-file error must not abort the run: %v", err)
	}
	if !reflect.DeepEqual(u.rels, []string{"a.txt", "a/b.txt", "z.txt"}) {
		t.Errorf("uploaded %v, want all three attempted", u.rels)
	}
	if sum.Uploaded != 2 || sum.Failed != 1 {
		t.Errorf("summary = %+v, want 2 uploaded, 1 failed", sum)
	}
	if events[1].RelPath != "a/b.txt" || !errors.Is(events[1].Err, boom) {
		t.Errorf("failure event = %+v", events[1])
	}
}

func TestSyncAbortsWhenSignedOut(t *testing.T) {
	for _, signInErr := range []error{drive.ErrNotSignedIn, drive.ErrSessionExpired} {
		t.Run(signInErr.Error(), func(t *testing.T) {
			root := syncTree(t)
			u := &uploads{failOn: map[string]error{"a/b.txt": fmt.Errorf("upload: %w", signInErr)}}

			sum, err := Sync(context.Background(), root, openRepo(t), u.fn, func(Event) {})

			if !errors.Is(err, signInErr) {
				t.Fatalf("err = %v, want %v", err, signInErr)
			}
			if !reflect.DeepEqual(u.rels, []string{"a.txt", "a/b.txt"}) {
				t.Errorf("attempted %v, want to stop at the failing file", u.rels)
			}
			if sum.Uploaded != 1 || sum.Failed != 0 {
				t.Errorf("summary = %+v, want 1 uploaded, 0 failed", sum)
			}
		})
	}
}

func TestSyncStopsWhenCancelled(t *testing.T) {
	root := syncTree(t)
	ctx, cancel := context.WithCancel(context.Background())
	u := &uploads{after: func(rel string) {
		if rel == "a.txt" {
			cancel()
		}
	}}

	sum, err := Sync(ctx, root, openRepo(t), u.fn, func(Event) {})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if !reflect.DeepEqual(u.rels, []string{"a.txt"}) {
		t.Errorf("attempted %v, want only the first file", u.rels)
	}
	if sum.Uploaded != 1 {
		t.Errorf("summary = %+v, want the first upload counted", sum)
	}
}

func TestSyncCancelledDuringUploadIsNotAFileFailure(t *testing.T) {
	root := syncTree(t)
	ctx, cancel := context.WithCancel(context.Background())
	upload := func(ctx context.Context, _, _ string) (drive.FileInfo, error) {
		cancel()
		return drive.FileInfo{}, ctx.Err() // what the Drive client returns mid-request
	}

	sum, err := Sync(ctx, root, openRepo(t), upload, func(Event) {})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if sum.Failed != 0 {
		t.Errorf("summary = %+v, an interrupted upload is not a failed file", sum)
	}
}

func TestSyncBadRoot(t *testing.T) {
	u := &uploads{}
	if _, err := Sync(context.Background(), filepath.Join(t.TempDir(), "nope"), openRepo(t), u.fn, func(Event) {}); err == nil {
		t.Fatal("want an error for a missing root")
	}
	if len(u.rels) != 0 {
		t.Errorf("uploaded %v from a missing root", u.rels)
	}
}

func TestSyncRecordsARowPerUpload(t *testing.T) {
	root := syncTree(t)
	repo := openRepo(t)
	u := &uploads{}

	if _, err := Sync(context.Background(), root, repo, u.fn, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	st, err := os.Stat(filepath.Join(root, "a", "b.txt"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(context.Background(), "a/b.txt")
	if err != nil {
		t.Fatalf("no row for a/b.txt: %v", err)
	}
	want := store.File{
		RelPath:     "a/b.txt",
		DriveFileID: "id-a/b.txt",
		SyncedMD5:   "md5-a/b.txt",
		Size:        st.Size(),
		MTime:       st.ModTime().UnixNano(),

		BaseMD5:        "md5-a/b.txt",
		BaseRevisionID: "rev-a/b.txt",
	}
	if got != want {
		t.Errorf("row = %+v, want %+v", got, want)
	}
	rows, _ := repo.List(context.Background())
	if len(rows) != 3 {
		t.Errorf("rows = %d, want 3 (ignored files get none)", len(rows))
	}
}

func TestSyncSkipsFilesAlreadyRecorded(t *testing.T) {
	root := syncTree(t)
	repo := openRepo(t)
	ctx := context.Background()
	// a.txt matches its row exactly, so it is skipped without being read.
	res, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	var a Entry
	for _, e := range res.Entries {
		if e.RelPath == "a.txt" {
			a = e
		}
	}
	if err := repo.Upsert(ctx, store.File{RelPath: "a.txt", DriveFileID: "old-id", Size: a.Size, MTime: a.MTime, SyncedMD5: "bogus"}); err != nil {
		t.Fatal(err)
	}
	// A row with no Drive ID means "not on Drive yet", so it is uploaded.
	if err := repo.Upsert(ctx, store.File{RelPath: "z.txt"}); err != nil {
		t.Fatal(err)
	}
	u := &uploads{}

	sum, err := Sync(ctx, root, repo, u.fn, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(u.rels, []string{"a/b.txt", "z.txt"}) {
		t.Errorf("uploaded %v, want a/b.txt and z.txt only", u.rels)
	}
	if sum.Uploaded != 2 || sum.Updated != 0 || sum.Synced != 1 {
		t.Errorf("summary = %+v, want 2 uploaded, 1 already synced", sum)
	}
	if row, _ := repo.Get(ctx, "a.txt"); row.DriveFileID != "old-id" {
		t.Errorf("a recorded row was rewritten: %+v", row)
	}
}

func TestSyncSecondRunUploadsNothing(t *testing.T) {
	root := syncTree(t)
	repo := openRepo(t)
	first, second := &uploads{}, &uploads{}

	if _, err := Sync(context.Background(), root, repo, first.fn, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	sum, err := Sync(context.Background(), root, repo, second.fn, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}

	if len(second.rels) != 0 || sum.Uploaded != 0 || sum.Synced != 3 {
		t.Errorf("second run uploaded %v, summary %+v; want nothing uploaded and 3 already synced", second.rels, sum)
	}
}

func TestSyncFailedUploadWritesNoRow(t *testing.T) {
	root := syncTree(t)
	repo := openRepo(t)
	u := &uploads{failOn: map[string]error{"a/b.txt": errors.New("quota exceeded")}}

	if _, err := Sync(context.Background(), root, repo, u.fn, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.Get(context.Background(), "a/b.txt"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a failed upload left a row: %v", err)
	}
	// The next run retries just that file.
	retry := &uploads{}
	sum, err := Sync(context.Background(), root, repo, retry.fn, func(Event) {})
	if err != nil || !reflect.DeepEqual(retry.rels, []string{"a/b.txt"}) || sum.Uploaded != 1 || sum.Synced != 2 {
		t.Errorf("retry uploaded %v, summary %+v, err %v; want only a/b.txt", retry.rels, sum, err)
	}
}

func TestSyncInterruptedRunResumes(t *testing.T) {
	root := syncTree(t)
	repo := openRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	first := &uploads{after: func(rel string) {
		if rel == "a.txt" {
			cancel()
		}
	}}
	if _, err := Sync(ctx, root, repo, first.fn, func(Event) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("first run err = %v, want context.Canceled", err)
	}

	second := &uploads{}
	sum, err := Sync(context.Background(), root, repo, second.fn, func(Event) {})

	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(second.rels, []string{"a/b.txt", "z.txt"}) || sum.Synced != 1 {
		t.Errorf("second run uploaded %v, summary %+v; want the remaining two, 1 already synced", second.rels, sum)
	}
}

func TestSyncStopsWhenTheDatabaseFails(t *testing.T) {
	root := syncTree(t)
	repo := openRepo(t)
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	u := &uploads{}

	_, err := Sync(context.Background(), root, repo, u.fn, func(Event) {})

	if !errors.Is(err, store.ErrClosed) {
		t.Fatalf("err = %v, want store.ErrClosed", err)
	}
	if len(u.rels) != 0 {
		t.Errorf("uploaded %v before noticing the database was gone", u.rels)
	}
}

const md5OfX = "9dd4e461268c8034f5c8564e155c67a6"

func TestSyncMatchingSizeAndMTimeIsNotHashed(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.txt"), "x")
	res, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	repo := openRepo(t)
	ctx := context.Background()
	// A bogus SyncedMD5 shows the file is never read: a hash would not match it.
	row := store.File{RelPath: "a.txt", DriveFileID: "id", Size: res.Entries[0].Size, MTime: res.Entries[0].MTime, SyncedMD5: "bogus"}
	if err := repo.Upsert(ctx, row); err != nil {
		t.Fatal(err)
	}
	u := &uploads{}

	sum, err := Sync(ctx, root, repo, u.fn, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}

	if len(u.rels) != 0 || sum.Synced != 1 {
		t.Errorf("uploaded %v, summary %+v; want nothing uploaded, 1 synced", u.rels, sum)
	}
	if got, _ := repo.Get(ctx, "a.txt"); got.LocalMD5 != "" {
		t.Errorf("file was hashed despite matching size and mtime: %+v", got)
	}
}

func TestSyncTouchedButIdenticalFileRefreshesItsRow(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.txt"), "x")
	res, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	repo := openRepo(t)
	ctx := context.Background()
	stale := store.File{RelPath: "a.txt", DriveFileID: "id", Size: res.Entries[0].Size, MTime: res.Entries[0].MTime - 1, SyncedMD5: md5OfX}
	if err := repo.Upsert(ctx, stale); err != nil {
		t.Fatal(err)
	}
	u := &uploads{}

	sum, err := Sync(ctx, root, repo, u.fn, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}

	if len(u.rels) != 0 || sum.Uploaded != 0 || sum.Synced != 1 {
		t.Errorf("uploaded %v, summary %+v; want nothing uploaded, 1 synced", u.rels, sum)
	}
	got, _ := repo.Get(ctx, "a.txt")
	if got.MTime != res.Entries[0].MTime || got.LocalMD5 != md5OfX || got.DriveFileID != "id" || got.SyncedMD5 != md5OfX {
		t.Errorf("row = %+v, want mtime and local md5 refreshed, Drive fields kept", got)
	}
}

func TestSyncUploadsOnlyTheEditedFileAsAnUpdate(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"a.txt", "b.txt"} {
		write(t, filepath.Join(root, rel), "x")
	}
	res, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	repo := openRepo(t)
	ctx := context.Background()
	for _, e := range res.Entries {
		row := store.File{RelPath: e.RelPath, DriveFileID: "id-" + e.RelPath, Size: e.Size, MTime: e.MTime, SyncedMD5: md5OfX}
		if err := repo.Upsert(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(root, "b.txt"), "edited")
	u := &uploads{}
	var events []Event

	sum, err := Sync(ctx, root, repo, u.fn, collect(&events))
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(u.rels, []string{"b.txt"}) {
		t.Errorf("uploaded %v, want b.txt only", u.rels)
	}
	if sum.Uploaded != 0 || sum.Updated != 1 || sum.Synced != 1 {
		t.Errorf("summary = %+v, want 0 uploaded, 1 updated, 1 synced", sum)
	}
	if !reflect.DeepEqual(events, []Event{{RelPath: "b.txt", Updated: true}}) {
		t.Errorf("events = %+v, want one Updated event for b.txt", events)
	}
	row, _ := repo.Get(ctx, "b.txt")
	if row.DriveFileID != "id-b.txt" || row.SyncedMD5 != "md5-b.txt" || row.Size != int64(len("edited")) || row.LocalMD5 == "" {
		t.Errorf("row = %+v, want Drive ID kept and size, md5s refreshed", row)
	}
}

func TestSyncFailedUpdateKeepsTheOldRow(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.txt"), "x")
	res, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	repo := openRepo(t)
	ctx := context.Background()
	old := store.File{RelPath: "a.txt", DriveFileID: "id", Size: res.Entries[0].Size, MTime: res.Entries[0].MTime, SyncedMD5: md5OfX}
	if err := repo.Upsert(ctx, old); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "a.txt"), "edited")
	u := &uploads{failOn: map[string]error{"a.txt": errors.New("boom")}}

	sum, err := Sync(ctx, root, repo, u.fn, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}

	if sum.Failed != 1 || sum.Updated != 0 {
		t.Errorf("summary = %+v, want 1 failed", sum)
	}
	if got, _ := repo.Get(ctx, "a.txt"); got != old {
		t.Errorf("row = %+v, want unchanged %+v", got, old)
	}
}

func TestSyncRowWithoutSyncedMD5IsUpdatedOnce(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.txt"), "x")
	repo := openRepo(t)
	ctx := context.Background()
	if err := repo.Upsert(ctx, store.File{RelPath: "a.txt", DriveFileID: "id"}); err != nil {
		t.Fatal(err)
	}
	u := &uploads{}

	sum, err := Sync(ctx, root, repo, u.fn, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}

	if sum.Updated != 1 || len(u.rels) != 1 {
		t.Errorf("uploaded %v, summary %+v; want one update", u.rels, sum)
	}
}

func TestSyncRecordsBaseAndRevisionsAcrossAnUpdate(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	write(t, filepath.Join(root, "a.txt"), "one")
	repo := openRepo(t)
	rev := "r1"
	fn := func(_ context.Context, _, rel string) (drive.FileInfo, error) {
		return drive.FileInfo{ID: "id-" + rel, MD5: "md5-" + rev, RevisionID: rev}, nil
	}
	run := func() Summary {
		t.Helper()
		sum, err := Sync(ctx, root, repo, fn, func(Event) {})
		if err != nil {
			t.Fatal(err)
		}
		return sum
	}

	run()
	revs, _ := repo.Revisions(ctx, "a.txt")
	if len(revs) != 1 || revs[0].RevisionID != "r1" || revs[0].Source != "upload" || revs[0].Size != 3 {
		t.Fatalf("after upload revisions = %+v", revs)
	}

	// Unchanged rerun: no new revision, base untouched.
	run()
	if revs, _ = repo.Revisions(ctx, "a.txt"); len(revs) != 1 {
		t.Errorf("skip added a revision: %+v", revs)
	}

	rev = "r2"
	write(t, filepath.Join(root, "a.txt"), "edited")
	if sum := run(); sum.Updated != 1 {
		t.Fatalf("summary = %+v, want 1 updated", sum)
	}
	revs, _ = repo.Revisions(ctx, "a.txt")
	if len(revs) != 2 || revs[0].RevisionID != "r2" {
		t.Errorf("after update revisions = %+v, want r2 then r1", revs)
	}
	row, _ := repo.Get(ctx, "a.txt")
	if row.BaseRevisionID != "r2" || row.BaseMD5 != "md5-r2" {
		t.Errorf("base = %q/%q, want r2/md5-r2", row.BaseRevisionID, row.BaseMD5)
	}
}

func TestSyncFailedUploadRecordsNoRevision(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	write(t, filepath.Join(root, "a.txt"), "x")
	repo := openRepo(t)
	u := &uploads{failOn: map[string]error{"a.txt": errors.New("boom")}}
	if _, err := Sync(ctx, root, repo, u.fn, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if revs, _ := repo.Revisions(ctx, "a.txt"); len(revs) != 0 {
		t.Errorf("revisions = %+v, want none", revs)
	}
}

func TestSyncEmptyRevisionIDStillRecordsTheRow(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	write(t, filepath.Join(root, "a.txt"), "x")
	repo := openRepo(t)
	fn := func(context.Context, string, string) (drive.FileInfo, error) {
		return drive.FileInfo{ID: "id", MD5: "m"}, nil
	}
	if sum, err := Sync(ctx, root, repo, fn, func(Event) {}); err != nil || sum.Uploaded != 1 || sum.Failed != 0 {
		t.Fatalf("sum = %+v, err = %v", sum, err)
	}
	row, err := repo.Get(ctx, "a.txt")
	if err != nil || row.DriveFileID != "id" || row.BaseMD5 != "m" || row.BaseRevisionID != "" {
		t.Errorf("row = %+v, err = %v", row, err)
	}
	if revs, _ := repo.Revisions(ctx, "a.txt"); len(revs) != 0 {
		t.Errorf("revisions = %+v, want none", revs)
	}
}

// captureLogs makes slog's default logger write debug-level text to the
// returned buffer for the rest of the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

func TestSyncLogsPathJobAndDriveFileID(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	write(t, filepath.Join(root, "a.txt"), "TOPSECRET")
	write(t, filepath.Join(root, "b.txt"), "x")
	repo := openRepo(t)
	u := &uploads{failOn: map[string]error{"b.txt": errors.New("boom")}}
	logs := captureLogs(t)

	if _, err := Sync(ctx, root, repo, u.fn, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "a.txt"), "TOPSECRET edited")
	if _, err := Sync(ctx, root, repo, u.fn, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	out := logs.String()
	for _, want := range []string{
		`msg=uploaded path=a.txt job=1 drive_file_id=id-a.txt`,
		`msg=updated path=a.txt job=1 drive_file_id=id-a.txt`,
		`msg="upload failed" path=b.txt job=2`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("logs missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "TOPSECRET") {
		t.Errorf("logs contain file content:\n%s", out)
	}
}
