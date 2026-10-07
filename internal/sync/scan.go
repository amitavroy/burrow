package sync

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Entry is one local file found by Scan.
type Entry struct {
	// RelPath is the slash-form path relative to the root, with no
	// machine-specific parts.
	RelPath string
	Size    int64
	// MTime is Unix nanoseconds, same unit as store.File.
	MTime int64
}

// Scan walks root and returns its regular files sorted by RelPath, skipping
// anything the default ignore rules match; an ignored directory is not walked
// at all. It never writes. Directories, symlinks and other non-regular files
// are not listed.
func Scan(root string) ([]Entry, error) {
	st, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("scan root: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("scan root: %s is not a directory", root)
	}

	ignore := NewMatcher()
	var entries []Entry
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if ignore.Ignored(rel, d.IsDir()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entries = append(entries, Entry{
			RelPath: rel,
			Size:    info.Size(),
			MTime:   info.ModTime().UnixNano(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}
	// WalkDir orders by OS path; sort again so the order is the slash-form one.
	sort.Slice(entries, func(i, j int) bool { return entries[i].RelPath < entries[j].RelPath })
	return entries, nil
}
