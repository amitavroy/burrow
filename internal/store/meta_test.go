package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMeta(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "m.db")
	r, err := OpenRepo(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := r.GetMeta(ctx, "k"); err != nil || ok {
		t.Fatalf("missing key = %v, %v", ok, err)
	}
	if err := r.SetMeta(ctx, "k", "one"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetMeta(ctx, "k", "two"); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = OpenRepo(path) // survives a reopen
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if v, ok, err := r.GetMeta(ctx, "k"); err != nil || !ok || v != "two" {
		t.Fatalf("after reopen = %q, %v, %v", v, ok, err)
	}
}
