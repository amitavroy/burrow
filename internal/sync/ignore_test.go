package sync

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type ignoreCase struct {
	path  string
	isDir bool
	want  bool
}

func checkIgnored(t *testing.T, m *Matcher, cases []ignoreCase) {
	t.Helper()
	for _, tt := range cases {
		if got := m.Ignored(tt.path, tt.isDir); got != tt.want {
			t.Errorf("Ignored(%q, dir=%v) = %v, want %v", tt.path, tt.isDir, got, tt.want)
		}
	}
}

// matcherFor builds a Matcher for a root whose .syncignore holds content.
func matcherFor(t *testing.T, content string) *Matcher {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, ".syncignore"), content)
	m, err := NewMatcher(root)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMatcherDefaults(t *testing.T) {
	m, err := NewMatcher(t.TempDir()) // no .syncignore
	if err != nil {
		t.Fatal(err)
	}
	checkIgnored(t, m, []ignoreCase{
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
		{".syncignore", false, true},
		{"readme.md", false, false},
		{"a/b/c.txt", false, false},
		{"tmp", false, false},
		{"my~notes.txt", false, false},
		{"a/.syncignore", false, false},
	})
}

func TestMatcherSyncignoreSyntax(t *testing.T) {
	const rules = "# a comment\n\n" +
		"secret.txt\n" + // plain name
		"*.log\n" + // glob
		"!keep.log\n" + // negation
		"**/gen/*.go\n" + // double star
		"/top.txt\n" + // anchored to the root
		"build/\n" // directories only
	for name, content := range map[string]string{
		"LF":   rules,
		"CRLF": strings.ReplaceAll(rules, "\n", "\r\n"),
	} {
		t.Run(name, func(t *testing.T) {
			checkIgnored(t, matcherFor(t, content), []ignoreCase{
				{"secret.txt", false, true},
				{"a/secret.txt", false, true},
				{"a.log", false, true},
				{"d/a.log", false, true},
				{"keep.log", false, false},
				{"d/keep.log", false, false},
				{"a/gen/x.go", false, true},
				{"gen/x.go", false, true},
				{"x.go", false, false},
				{"top.txt", false, true},
				{"a/top.txt", false, false},
				{"build", true, true},
				{"a/build", true, true},
				{"a/build/o.bin", false, true},
				{"build", false, false}, // a file named build is not matched by build/
				{"# a comment", false, false},
			})
		})
	}
}

func TestMatcherNegationOverridesDefaultButNotHardRules(t *testing.T) {
	m := matcherFor(t, "!keep.tmp\n!.git\n!.syncignore\n!node_modules\n")
	checkIgnored(t, m, []ignoreCase{
		{"keep.tmp", false, false}, // a default can be overridden
		{"other.tmp", false, true},
		{"node_modules", true, false},
		{".git", true, true}, // .git and .syncignore cannot
		{".syncignore", false, true},
	})
}

func TestNewMatcherUnreadableFile(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs chmod 000 to make a file unreadable")
	}
	root := t.TempDir()
	path := filepath.Join(root, ".syncignore")
	write(t, path, "x\n")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o644) })

	_, err := NewMatcher(root)
	if err == nil || !strings.Contains(err.Error(), ".syncignore") {
		t.Fatalf("err = %v, want one naming .syncignore", err)
	}
}
