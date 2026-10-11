package sync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The ticket checkpoint: one editor save produces one queued job.
func TestEditorSaveProducesOneQueuedJob(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := openRepo(t)
	w, err := NewWatcher(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	d := NewDebouncer(w.Events(), root, 50*time.Millisecond)
	t.Cleanup(d.Close)

	save := func(content string) {
		tmp, final := filepath.Join(root, "doc.txt.tmp"), filepath.Join(root, "doc.txt")
		write(t, tmp, content[:len(content)/2]) // editors write a temp file in pieces,
		write(t, tmp, content)
		if err := os.Rename(tmp, final); err != nil { // then rename it over the original
			t.Fatal(err)
		}
	}
	awaitReady := func() string {
		select {
		case p := <-d.Ready():
			return p
		case <-time.After(3 * time.Second):
			t.Fatal("no ready path")
		}
		return ""
	}

	save("first version of the document")
	p := awaitReady()
	if p != "doc.txt" {
		t.Fatalf("ready %q, want doc.txt", p)
	}
	if err := repo.EnqueueJobs(ctx, []string{p}); err != nil {
		t.Fatal(err)
	}
	if n, _ := repo.CountJobs(ctx); n != 1 {
		t.Fatalf("queued jobs = %d, want 1", n)
	}
	select {
	case extra := <-d.Ready():
		t.Fatalf("a second ready for one save: %q", extra)
	case <-time.After(300 * time.Millisecond):
	}

	// Saving again before the job is claimed is still one job.
	save("second version of the document, longer")
	if err := repo.EnqueueJobs(ctx, []string{awaitReady()}); err != nil {
		t.Fatal(err)
	}
	if n, _ := repo.CountJobs(ctx); n != 1 {
		t.Fatalf("queued jobs after a second save = %d, want 1", n)
	}
}
