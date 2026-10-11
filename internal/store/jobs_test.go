package store

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestJobsEnqueueIsIdempotent(t *testing.T) {
	r, _ := openTempRepo(t)
	ctx := context.Background()
	for range 2 {
		if err := r.EnqueueJobs(ctx, []string{"a", "b"}); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := r.CountJobs(ctx); n != 2 {
		t.Fatalf("count = %d, want 2", n)
	}
}

func TestJobsClaimOldestFirstThenNone(t *testing.T) {
	r, _ := openTempRepo(t)
	ctx := context.Background()
	_ = r.EnqueueJobs(ctx, []string{"a", "b", "c"})
	for _, want := range []string{"a", "b", "c"} {
		j, err := r.ClaimJob(ctx)
		if err != nil || j.RelPath != want {
			t.Fatalf("claim = %+v, %v; want %s", j, err, want)
		}
	}
	if _, err := r.ClaimJob(ctx); !errors.Is(err, ErrNoJob) {
		t.Fatalf("err = %v, want ErrNoJob", err)
	}
}

func TestJobsEnqueueLeavesRunningJobAlone(t *testing.T) {
	r, _ := openTempRepo(t)
	ctx := context.Background()
	_ = r.EnqueueJobs(ctx, []string{"a"})
	j, _ := r.ClaimJob(ctx)
	_ = r.EnqueueJobs(ctx, []string{"a"})
	if _, err := r.ClaimJob(ctx); !errors.Is(err, ErrNoJob) {
		t.Fatalf("a running job was claimed twice: %v", err)
	}
	_ = r.CompleteJob(ctx, j.ID)
	if n, _ := r.CountJobs(ctx); n != 0 {
		t.Fatalf("count = %d after complete", n)
	}
}

func TestJobsFailKeepsMessageAndNextEnqueueRetries(t *testing.T) {
	r, path := openTempRepo(t)
	ctx := context.Background()
	_ = r.EnqueueJobs(ctx, []string{"a"})
	j, _ := r.ClaimJob(ctx)
	if err := r.FailJob(ctx, j.ID, "quota exceeded"); err != nil {
		t.Fatal(err)
	}
	if n, _ := r.CountJobs(ctx); n != 0 {
		t.Fatalf("a failed job counts as work to do: %d", n)
	}
	var status, msg string
	var attempts int
	row := func() {
		db, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err := db.QueryRow(`SELECT status, attempts, last_error FROM jobs`).Scan(&status, &attempts, &msg); err != nil {
			t.Fatal(err)
		}
	}
	row()
	if status != "failed" || attempts != 1 || msg != "quota exceeded" {
		t.Fatalf("row = %s %d %q", status, attempts, msg)
	}
	_ = r.EnqueueJobs(ctx, []string{"a"})
	again, err := r.ClaimJob(ctx)
	if err != nil || again.ID != j.ID || again.Attempts != 1 {
		t.Fatalf("retry claim = %+v, %v", again, err)
	}
}

func TestJobsResetAndRequeue(t *testing.T) {
	r, _ := openTempRepo(t)
	ctx := context.Background()
	_ = r.EnqueueJobs(ctx, []string{"a", "b"})
	a, _ := r.ClaimJob(ctx)
	_, _ = r.ClaimJob(ctx)
	if err := r.RequeueJob(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	n, err := r.ResetRunningJobs(ctx)
	if err != nil || n != 1 {
		t.Fatalf("reset = %d, %v; want 1", n, err)
	}
	if c, _ := r.CountJobs(ctx); c != 2 {
		t.Fatalf("count = %d, want 2 pending", c)
	}
}

// The queue is the point of the ticket: it must survive a restart.
func TestJobsSurviveReopen(t *testing.T) {
	r, path := openTempRepo(t)
	ctx := context.Background()
	_ = r.EnqueueJobs(ctx, []string{"a", "b"})
	_, _ = r.ClaimJob(ctx) // "a" is running when the process dies
	r.Close()

	r2, err := OpenRepo(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	if n, _ := r2.ResetRunningJobs(ctx); n != 1 {
		t.Fatalf("reset = %d, want 1", n)
	}
	j, err := r2.ClaimJob(ctx)
	if err != nil || j.RelPath != "a" {
		t.Fatalf("claim = %+v, %v", j, err)
	}
}

func TestJobsConcurrentClaimsAreDistinct(t *testing.T) {
	r, _ := openTempRepo(t)
	ctx := context.Background()
	const n = 50
	paths := make([]string, n)
	for i := range paths {
		paths[i] = string(rune('A'+i/26)) + string(rune('a'+i%26))
	}
	_ = r.EnqueueJobs(ctx, paths)

	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				j, err := r.ClaimJob(ctx)
				if err != nil {
					return
				}
				mu.Lock()
				seen[j.RelPath]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != n {
		t.Fatalf("claimed %d distinct jobs, want %d", len(seen), n)
	}
	for p, c := range seen {
		if c != 1 {
			t.Errorf("%s claimed %d times", p, c)
		}
	}
}
