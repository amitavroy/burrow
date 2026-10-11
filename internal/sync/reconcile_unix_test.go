//go:build unix

package sync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Rows below a folder the scan could not read are not "missing", and the
// folder gets an error row.
func TestReconcileUnreadableFolder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read anything")
	}
	ctx := context.Background()
	root := t.TempDir()
	repo := openRepo(t)
	write(t, filepath.Join(root, "locked", "a.txt"), "x")
	write(t, filepath.Join(root, "ok.txt"), "x")
	synced(t, repo, root, "locked/a.txt")
	synced(t, repo, root, "ok.txt")
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })

	plan, err := Reconcile(ctx, root, repo)

	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Missing) != 0 || len(plan.Unreadable) != 1 || plan.Unchanged != 1 {
		t.Errorf("plan = %+v, want nothing missing under the unreadable folder", plan)
	}
	if _, ok := errorsOf(t, repo)["locked"]; !ok {
		t.Error("no error row for the unreadable folder")
	}
}
