package drive

import (
	"context"
	"errors"
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
