// Package sync holds the sync engine: watcher, debounce, persistent queue and uploader.
// Scan walks the sync root and returns the files a sync would upload, after
// the default ignore rules and the root's .syncignore (see Matcher).
package sync
