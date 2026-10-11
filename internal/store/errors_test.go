package store

import (
	"context"
	"testing"
)

func TestErrorsRecordTwiceKeepsOneRow(t *testing.T) {
	r, _ := openTempRepo(t)
	ctx := context.Background()
	_ = r.RecordError(ctx, "a.txt", "network problem", "dial tcp: boom")
	first, _ := r.ListErrors(ctx)
	_ = r.RecordError(ctx, "a.txt", "Drive rate limit hit", "429")

	got, err := r.ListErrors(ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	e := got[0]
	if e.Attempts != 2 || e.Message != "Drive rate limit hit" || e.Detail != "429" || e.FirstSeen != first[0].FirstSeen || e.LastSeen < e.FirstSeen {
		t.Errorf("row = %+v", e)
	}
}

func TestErrorsClearAndOrder(t *testing.T) {
	r, _ := openTempRepo(t)
	ctx := context.Background()
	_ = r.RecordError(ctx, "old.txt", "m", "")
	_ = r.RecordError(ctx, "new.txt", "m", "")
	got, _ := r.ListErrors(ctx)
	if len(got) != 2 || got[0].RelPath != "new.txt" {
		t.Fatalf("order = %+v, want newest first", got)
	}
	if err := r.ClearError(ctx, "new.txt"); err != nil {
		t.Fatal(err)
	}
	if err := r.ClearError(ctx, "missing.txt"); err != nil {
		t.Fatalf("clearing a missing row: %v", err)
	}
	if got, _ := r.ListErrors(ctx); len(got) != 1 || got[0].RelPath != "old.txt" {
		t.Errorf("after clear = %+v", got)
	}
}

func TestErrorsSurviveReopen(t *testing.T) {
	r, path := openTempRepo(t)
	_ = r.RecordError(context.Background(), "a.txt", "m", "d")
	r.Close()
	r2, err := OpenRepo(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	if got, _ := r2.ListErrors(context.Background()); len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
}
