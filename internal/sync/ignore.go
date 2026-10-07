package sync

import (
	"strings"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// defaultIgnores are always applied. Editors write temp files (~$*, *.tmp) on
// every save, and .git and node_modules are never worth uploading.
var defaultIgnores = []string{".git", "node_modules", "*.tmp", "~$*"}

// Matcher decides which paths a scan skips, using gitignore syntax.
type Matcher struct {
	m gitignore.Matcher
}

// NewMatcher returns a Matcher holding the default ignore rules.
func NewMatcher() *Matcher {
	ps := make([]gitignore.Pattern, 0, len(defaultIgnores))
	for _, line := range defaultIgnores {
		ps = append(ps, gitignore.ParsePattern(line, nil))
	}
	return &Matcher{m: gitignore.NewMatcher(ps)}
}

// Ignored reports whether relPath (slash form, relative to the root) is
// ignored. A path inside an ignored directory is ignored too.
func (m *Matcher) Ignored(relPath string, isDir bool) bool {
	return m.m.Match(strings.Split(relPath, "/"), isDir)
}
