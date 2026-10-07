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

// Result is what one Scan found.
type Result struct {
	// Entries are the regular files to sync, sorted by RelPath.
	Entries []Entry
	// Ignored counts the files and directories the ignore rules skipped. An
	// ignored directory counts once, not once per file inside it.
	Ignored int
	// Errors are unreadable entries that were skipped; the scan carried on.
	Errors []error
}

// Scan walks root and returns its regular files sorted by RelPath, skipping
// anything the default rules or <root>/.syncignore match; an ignored directory
// is not walked at all. Symlinks and other non-regular files are not listed,
// and directories themselves are not listed. It never writes.
//
// An unreadable file or directory below the root is recorded in Result.Errors
// and skipped, so one locked file does not hide the rest of the tree. Only a
// problem with the root itself, or a bad .syncignore, fails the scan.
func Scan(root string) (Result, error) {
	st, err := os.Stat(root)
	if err != nil {
		return Result{}, fmt.Errorf("scan root: %w", err)
	}
	if !st.IsDir() {
		return Result{}, fmt.Errorf("scan root: %s is not a directory", root)
	}
	ignore, err := NewMatcher(root)
	if err != nil {
		return Result{}, err
	}

	var res Result
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			res.Errors = append(res.Errors, err)
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
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
			res.Ignored++
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
			res.Errors = append(res.Errors, err)
			return nil
		}
		res.Entries = append(res.Entries, Entry{
			RelPath: rel,
			Size:    info.Size(),
			MTime:   info.ModTime().UnixNano(),
		})
		return nil
	})
	if err != nil {
		return Result{}, fmt.Errorf("scan %s: %w", root, err)
	}
	// WalkDir orders by OS path; sort again so the order is the slash-form one.
	sort.Slice(res.Entries, func(i, j int) bool { return res.Entries[i].RelPath < res.Entries[j].RelPath })
	return res, nil
}
