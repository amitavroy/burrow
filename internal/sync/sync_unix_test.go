//go:build unix

package sync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A file the scan cannot read gets an error row, and loses it once readable.
func TestSyncRecordsUnreadableEntryThenClearsIt(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read anything")
	}
	ctx := context.Background()
	root := t.TempDir()
	write(t, filepath.Join(root, "ok.txt"), "x")
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(locked, "a.txt"), "x")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	repo := openRepo(t)

	if _, err := Sync(ctx, root, repo, (&uploads{}).fn, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	got := errorsOf(t, repo)
	if e, ok := got["locked"]; !ok || e.Message != "cannot read file: permission denied" || len(got) != 1 {
		t.Fatalf("errors = %+v", got)
	}

	if err := os.Chmod(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(ctx, root, repo, (&uploads{}).fn, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if got := errorsOf(t, repo); len(got) != 0 {
		t.Errorf("errors after it became readable = %+v", got)
	}
}
