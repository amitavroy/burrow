package sync

import "testing"

func TestMatcherDefaults(t *testing.T) {
	tests := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{".git", true, true},
		{"a/.git", true, true},
		{"a/.git/config", false, true},
		{"node_modules", true, true},
		{"a/b/node_modules", true, true},
		{"a/b/node_modules/x.js", false, true},
		{"report.tmp", false, true},
		{"a/report.tmp", false, true},
		{"~$draft.docx", false, true},
		{"a/~$draft.docx", false, true},
		{"readme.md", false, false},
		{"a/b/c.txt", false, false},
		{"tmp", false, false},
		{"my~notes.txt", false, false},
	}
	m := NewMatcher()
	for _, tt := range tests {
		if got := m.Ignored(tt.path, tt.isDir); got != tt.want {
			t.Errorf("Ignored(%q, dir=%v) = %v, want %v", tt.path, tt.isDir, got, tt.want)
		}
	}
}
