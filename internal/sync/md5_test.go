package sync

import (
	"path/filepath"
	"testing"
)

func TestFileMD5(t *testing.T) {
	dir := t.TempDir()
	for name, tc := range map[string]struct{ content, want string }{
		"known": {"x", "9dd4e461268c8034f5c8564e155c67a6"},
		"empty": {"", "d41d8cd98f00b204e9800998ecf8427e"},
	} {
		p := filepath.Join(dir, name)
		write(t, p, tc.content)
		got, err := fileMD5(p)
		if err != nil || got != tc.want {
			t.Errorf("%s: fileMD5 = %q, %v; want %q", name, got, err, tc.want)
		}
	}
	if _, err := fileMD5(filepath.Join(dir, "missing")); err == nil {
		t.Error("fileMD5 of a missing file returned no error")
	}
}
