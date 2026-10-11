package drive

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func moveSetup(t *testing.T, fd *uploadDrive) *Uploader {
	t.Helper()
	client, extra := setup(t, fd)
	return NewUploader(client, signedIn(t, "r"), cacheAt(t, "root-id"), nil, extra...)
}

func TestMoveRenamesInTheSameFolder(t *testing.T) {
	fd := &uploadDrive{files: map[string]*fakeFile{"f1": {parents: []string{"root-id"}, name: "a.txt"}}}
	u := moveSetup(t, fd)

	info, err := u.Move(context.Background(), "f1", "b.txt")

	if err != nil {
		t.Fatal(err)
	}
	if info.ID != "f1" || info.RelPath != "b.txt" {
		t.Errorf("info = %+v", info)
	}
	want := []moveReq{{id: "f1", name: "b.txt", rel: "b.txt"}} // already in the right folder: no parent changes
	if !reflect.DeepEqual(fd.moves, want) {
		t.Errorf("moves = %+v, want %+v", fd.moves, want)
	}
	if fd.posts != 0 || fd.received != 0 {
		t.Errorf("content was sent: posts=%d received=%d", fd.posts, fd.received)
	}
}

func TestMoveToAnotherFolderAddsAndRemovesParents(t *testing.T) {
	fd := &uploadDrive{
		files: map[string]*fakeFile{"f1": {parents: []string{"dir-old"}, name: "a.txt"}},
		dirs:  []fakeDir{{id: "dir-new", name: "docs", parent: "root-id"}},
	}
	u := moveSetup(t, fd)

	if _, err := u.Move(context.Background(), "f1", "docs/b.txt"); err != nil {
		t.Fatal(err)
	}

	want := []moveReq{{id: "f1", name: "b.txt", rel: "docs/b.txt", add: "dir-new", remove: "dir-old"}}
	if !reflect.DeepEqual(fd.moves, want) {
		t.Errorf("moves = %+v, want %+v", fd.moves, want)
	}
	if got := fd.files["f1"].parents; !reflect.DeepEqual(got, []string{"dir-new"}) {
		t.Errorf("parents = %v, want only dir-new", got)
	}
}

func TestMoveCreatesMissingFolders(t *testing.T) {
	fd := &uploadDrive{files: map[string]*fakeFile{"f1": {parents: []string{"root-id"}, name: "a.txt"}}}
	u := moveSetup(t, fd)

	if _, err := u.Move(context.Background(), "f1", "x/y/a.txt"); err != nil {
		t.Fatal(err)
	}

	if len(fd.createdDirs) != 2 { // x and x/y
		t.Errorf("created %d folders, want 2", len(fd.createdDirs))
	}
	if len(fd.moves) != 1 || fd.moves[0].add != "dir-2" || fd.moves[0].remove != "root-id" {
		t.Errorf("moves = %+v", fd.moves)
	}
}

// Repeating a move that already happened (a lost reply) changes no parents.
func TestMoveIsIdempotent(t *testing.T) {
	fd := &uploadDrive{
		files: map[string]*fakeFile{"f1": {parents: []string{"root-id"}, name: "a.txt"}},
		dirs:  []fakeDir{{id: "dir-new", name: "docs", parent: "root-id"}},
	}
	u := moveSetup(t, fd)
	for range 2 {
		if _, err := u.Move(context.Background(), "f1", "docs/b.txt"); err != nil {
			t.Fatal(err)
		}
	}

	second := fd.moves[1]
	if second.add != "" || second.remove != "" {
		t.Errorf("the repeat changed parents: %+v", second)
	}
	if got := fd.files["f1"].parents; !reflect.DeepEqual(got, []string{"dir-new"}) {
		t.Errorf("parents = %v", got)
	}
}

func TestMoveMissingOrTrashedFileIsNotFound(t *testing.T) {
	fd := &uploadDrive{files: map[string]*fakeFile{"gone": {parents: []string{"root-id"}, trashed: true}}}
	u := moveSetup(t, fd)

	if _, err := u.Move(context.Background(), "gone", "b.txt"); !errors.Is(err, ErrFileNotFound) {
		t.Errorf("trashed: err = %v, want ErrFileNotFound", err)
	}
	if _, err := u.Move(context.Background(), "never-existed", "b.txt"); !errors.Is(err, ErrFileNotFound) {
		t.Errorf("unknown: err = %v, want ErrFileNotFound", err)
	}
	if len(fd.moves) != 0 {
		t.Errorf("updates sent: %+v", fd.moves)
	}
}

func TestMoveRetriesATransientError(t *testing.T) {
	fd := &uploadDrive{
		files: map[string]*fakeFile{"f1": {parents: []string{"root-id"}}},
		fails: []fakeFail{{on: "file", status: 503, n: 2}}, // files.get fails once, then files.update would too
	}
	u := moveSetup(t, fd)
	sleeps := stubSleep(t)

	if _, err := u.Move(context.Background(), "f1", "b.txt"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if *sleeps != 2 || len(fd.moves) != 1 {
		t.Errorf("sleeps = %d, moves = %d; want two retries and then one move", *sleeps, len(fd.moves))
	}
}

func TestMoveRejectsABadPath(t *testing.T) {
	u := moveSetup(t, &uploadDrive{})
	if _, err := u.Move(context.Background(), "f1", "../x"); err == nil {
		t.Error("want an error for a bad rel_path")
	}
}
