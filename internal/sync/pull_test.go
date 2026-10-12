package sync

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/amitavroy/burrow/internal/drive"
	"github.com/amitavroy/burrow/internal/store"
)

func TestPull(t *testing.T) {
	ctx := context.Background()
	root, repo := t.TempDir(), openRepo(t)
	for rel, c := range map[string]string{"drive-edit.txt": "a", "local-edit.txt": "a", "both.txt": "a", "echo.txt": "a"} {
		write(t, filepath.Join(root, rel), c)
		if err := repo.Upsert(ctx, store.File{RelPath: rel, DriveFileID: "id-" + rel, LocalMD5: md5Hex("a"), SyncedMD5: md5Hex("a"), BaseMD5: md5Hex("a"), BaseRevisionID: "r1"}); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(root, "local-edit.txt"), "mine")
	write(t, filepath.Join(root, "both.txt"), "mine")

	fr := newFakeRemote()
	ch := func(rel, content, rev string) drive.Change {
		fr.content["id-"+rel] = content
		return drive.Change{FileID: "id-" + rel, RelPath: rel, Tagged: true, MD5: md5Hex(content), RevisionID: rev}
	}
	changes := []drive.Change{
		ch("drive-edit.txt", "b", "r2"),
		ch("local-edit.txt", "a", "r1"), // unchanged on Drive
		ch("both.txt", "theirs", "r2"),
		ch("echo.txt", "a", "r1"), // our own revision
		ch("new.txt", "fresh", "r1"),
	}
	if err := Pull(ctx, root, repo, changes, fr.download, &expected{}); err != nil {
		t.Fatal(err)
	}

	for rel, want := range map[string]string{"drive-edit.txt": "b", "local-edit.txt": "mine", "both.txt": "mine", "new.txt": "fresh"} {
		if got := readFile(t, filepath.Join(root, rel)); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	if len(fr.got) != 2 {
		t.Errorf("downloaded %v, want drive-edit.txt and new.txt only", fr.got)
	}
	row, err := repo.Get(ctx, "drive-edit.txt")
	if err != nil || row.BaseMD5 != md5Hex("b") || row.BaseRevisionID != "r2" || row.SyncedMD5 != md5Hex("b") {
		t.Errorf("row after pull = %+v, %v", row, err)
	}
	if _, err := repo.Get(ctx, "new.txt"); err != nil {
		t.Errorf("new.txt has no row: %v", err)
	}
}

// A download is not uploaded back: the job the watcher queues for the renamed
// file is dropped while its MD5 is the expected one, even before the row is written.
func TestPullEchoNotUploaded(t *testing.T) {
	ctx := context.Background()
	root, repo := t.TempDir(), openRepo(t)
	write(t, filepath.Join(root, "f.txt"), "a")
	if err := repo.Upsert(ctx, store.File{RelPath: "f.txt", DriveFileID: "id", LocalMD5: md5Hex("a"), SyncedMD5: md5Hex("a"), BaseMD5: md5Hex("a"), BaseRevisionID: "r1"}); err != nil {
		t.Fatal(err)
	}
	exp := &expected{}
	run := &runState{expect: exp}
	upload := func(context.Context, string, string) (drive.FileInfo, error) {
		t.Error("uploaded a downloaded file")
		return drive.FileInfo{}, nil
	}
	fr := newFakeRemote()
	fr.content["id"] = "b"
	download := func(ctx context.Context, id, md5, dst string) error {
		if err := fr.download(ctx, id, md5, dst); err != nil {
			return err
		}
		st, _ := os.Lstat(dst) // the watcher's event, before Pull writes the row
		if _, fatal := process(ctx, slog.Default(), root, repo, upload, run, Entry{RelPath: "f.txt", Size: st.Size(), MTime: st.ModTime().UnixNano()}); fatal != nil {
			t.Error(fatal)
		}
		return nil
	}
	c := drive.Change{FileID: "id", RelPath: "f.txt", Tagged: true, MD5: md5Hex("b"), RevisionID: "r2"}
	if err := Pull(ctx, root, repo, []drive.Change{c}, download, exp); err != nil {
		t.Fatal(err)
	}
	if _, ok := exp.get("f.txt"); ok {
		t.Error("expectation not cleared")
	}
}
