package sync

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// ignoreFile is the optional per-root rules file. It uses gitignore syntax.
const ignoreFile = ".syncignore"

// defaultIgnores come first, so a .syncignore rule (including a negation like
// !keep.tmp) can override them. Editors write temp files (~$*, *.tmp) on every
// save, and node_modules is never worth uploading.
var defaultIgnores = []string{"node_modules", "*.tmp", "~$*"}

// Matcher decides which paths a scan skips, using gitignore syntax.
type Matcher struct {
	m gitignore.Matcher
}

// NewMatcher returns a Matcher holding the default rules followed by the
// rules in <root>/.syncignore. A missing file is fine; an unreadable one is an
// error naming the file.
func NewMatcher(root string) (*Matcher, error) {
	lines := append([]string(nil), defaultIgnores...)
	path := filepath.Join(root, ignoreFile)
	f, err := os.Open(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", path, err)
	default:
		defer f.Close()
		sc := bufio.NewScanner(f) // ScanLines also drops the \r of CRLF
		for sc.Scan() {
			line := sc.Text()
			if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
				continue
			}
			lines = append(lines, line)
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
	}

	ps := make([]gitignore.Pattern, 0, len(lines))
	for _, line := range lines {
		ps = append(ps, gitignore.ParsePattern(line, nil))
	}
	return &Matcher{m: gitignore.NewMatcher(ps)}, nil
}

// Ignored reports whether relPath (slash form, relative to the root) is
// ignored. A path inside an ignored directory is ignored too. Two rules cannot
// be overridden by .syncignore: .git is never synced, and the .syncignore file
// at the root is never uploaded.
func (m *Matcher) Ignored(relPath string, isDir bool) bool {
	parts := strings.Split(relPath, "/")
	for _, p := range parts {
		if p == ".git" {
			return true
		}
	}
	if relPath == ignoreFile {
		return true
	}
	return m.m.Match(parts, isDir)
}
