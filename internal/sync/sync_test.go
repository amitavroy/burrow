package sync

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/amitavroy/burrow/internal/drive"
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
	return drive.FileInfo{ID: "id-" + relPath, RelPath: relPath}, nil
}

func syncTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, rel := range []string{"z.txt", "a/b.txt", "a.txt", "skip.tmp", "node_modules/x.js"} {
		write(t, filepath.Join(root, filepath.FromSlash(rel)), "x")
	}
	return root
}

func collect(events *[]Event) func(Event) {
	return func(e Event) { *events = append(*events, e) }
}

func TestSyncUploadsInOrderAndSkipsIgnored(t *testing.T) {
	root := syncTree(t)
	u := &uploads{}
	var events []Event

	sum, err := Sync(context.Background(), root, u.fn, collect(&events))
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

	sum, err := Sync(context.Background(), root, u.fn, collect(&events))

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

			sum, err := Sync(context.Background(), root, u.fn, func(Event) {})

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

	sum, err := Sync(ctx, root, u.fn, func(Event) {})

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

	sum, err := Sync(ctx, root, upload, func(Event) {})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if sum.Failed != 0 {
		t.Errorf("summary = %+v, an interrupted upload is not a failed file", sum)
	}
}

func TestSyncBadRoot(t *testing.T) {
	u := &uploads{}
	if _, err := Sync(context.Background(), filepath.Join(t.TempDir(), "nope"), u.fn, func(Event) {}); err == nil {
		t.Fatal("want an error for a missing root")
	}
	if len(u.rels) != 0 {
		t.Errorf("uploaded %v from a missing root", u.rels)
	}
}
