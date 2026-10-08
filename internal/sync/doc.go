// Package sync holds the sync engine: watcher, debounce, persistent queue and uploader.
// Scan walks the sync root and returns the files a sync would upload, after
// the default ignore rules and the root's .syncignore (see Matcher). Sync
// uploads those files through an UploadFunc and records each one's Drive ID in
// the state database, skipping files already recorded.
package sync
