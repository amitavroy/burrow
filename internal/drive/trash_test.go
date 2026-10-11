package drive

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestTrashSetsTrashedAndKeepsTheFile(t *testing.T) {
	fd := &uploadDrive{files: map[string]*fakeFile{"f1": {parents: []string{"root-id"}, name: "a.txt"}}}
	u := moveSetup(t, fd)
	for range 2 { // repeating is harmless
		if err := u.Trash(context.Background(), "f1"); err != nil {
			t.Fatal(err)
		}
	}
	if !fd.files["f1"].trashed || len(fd.moves) != 2 || !fd.moves[0].trashed || fd.moves[0].name != "" {
		t.Errorf("trashed = %v, updates = %+v; want trashed:true and nothing else changed", fd.files["f1"].trashed, fd.moves)
	}
}

func TestTrashUnknownFileIsNotFound(t *testing.T) {
	u := moveSetup(t, &uploadDrive{})
	if err := u.Trash(context.Background(), "nope"); !errors.Is(err, ErrFileNotFound) {
		t.Errorf("err = %v, want ErrFileNotFound", err)
	}
}

func TestMoveFolderMovesTheFolderAndRewritesTheCache(t *testing.T) {
	ctx := context.Background()
	fd := &uploadDrive{
		files: map[string]*fakeFile{"F-old": {parents: []string{"root-id"}, name: "old"}},
		dirs:  []fakeDir{{id: "dir-docs", name: "docs", parent: "root-id"}},
	}
	client, extra := setup(t, fd)
	cache := repoAt(t)
	for rel, id := range map[string]string{"old": "F-old", "old/sub": "F-sub", "older": "F-older"} {
		if err := cache.PutFolder(ctx, rel, id); err != nil {
			t.Fatal(err)
		}
	}
	u := NewUploader(client, signedIn(t, "r"), cacheAt(t, "root-id"), cache, extra...)

	if err := u.MoveFolder(ctx, "old", "docs/new"); err != nil {
		t.Fatal(err)
	}

	want := []moveReq{{id: "F-old", name: "new", add: "dir-docs", remove: "root-id"}}
	if !reflect.DeepEqual(fd.moves, want) {
		t.Errorf("moves = %+v, want %+v", fd.moves, want)
	}
	if got := cachedFolder(t, cache, "docs/new"); got != "F-old" {
		t.Errorf("docs/new = %q", got)
	}
	if got := cachedFolder(t, cache, "docs/new/sub"); got != "F-sub" {
		t.Errorf("docs/new/sub = %q", got)
	}
	if _, err := cache.GetFolder(ctx, "old"); err == nil {
		t.Error("old is still cached")
	}
	if got := cachedFolder(t, cache, "older"); got != "F-older" { // a prefix of the name is not a child
		t.Errorf("older = %q", got)
	}
}

func TestMoveFolderUnknownFolderIsNotFound(t *testing.T) {
	fd := &uploadDrive{}
	client, extra := setup(t, fd)
	u := NewUploader(client, signedIn(t, "r"), cacheAt(t, "root-id"), repoAt(t), extra...)
	if err := u.MoveFolder(context.Background(), "old", "new"); !errors.Is(err, ErrFileNotFound) {
		t.Errorf("err = %v, want ErrFileNotFound", err)
	}
	if len(fd.moves) != 0 {
		t.Errorf("updates sent: %+v", fd.moves)
	}
}
