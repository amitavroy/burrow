// Package sync holds the sync engine: watcher, debounce, persistent queue and uploader.
// Scan walks the sync root and returns the files a sync would upload, after
// the default ignore rules and the root's .syncignore (see Matcher). Sync
// uploads new files through an UploadFunc and records each one's Drive ID in
// the state database. A recorded file is skipped when its size and mtime or
// its streamed MD5 match the row, and is replaced in place when its content
// changed.
package sync
