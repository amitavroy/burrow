// Package sync holds the sync engine: watcher, debounce, persistent queue and uploader.
// Scan walks the sync root and returns the files a sync would upload, after
// the default ignore rules and the root's .syncignore (see Matcher). Sync
// queues a job per file in the state database and drains the queue with one
// or more workers (SyncWorkers), so a killed run resumes. It uploads new files
// through an UploadFunc and records each one's Drive ID in
// the state database. A recorded file is skipped when its size and mtime or
// its streamed MD5 match the row, and is replaced in place when its content
// changed. Sync logs through slog.Default with path, job and drive_file_id
// attributes, never file contents. Each successful upload records the file's base MD5 and Drive
// revision and a file_revisions entry. Watcher reports create, write, rename and
// remove events under the root, recursively (fsnotify is not), including for
// directories created later; it applies the same ignore rules and does no
// debouncing. Debouncer sits behind it: one ready rel_path per file whose size
// and mtime have stopped changing, so an editor save is one job. Run joins
// Watcher, Debouncer, the job queue and the workers into the foreground
// daemon behind syncd run. Diff and Reconcile compare the root with the files
// table, so Run queues what changed while it was stopped. Long-lived loops run
// one iteration at a time under safely, which recovers and logs a panic, and a
// watch refused by the operating system is reported as ErrWatchLimit. Entry and
// the saved rows carry the file's inode (Unix) or file ID (Windows), read by
// fileID, for rename detection: a new file that matches a vanished tracked file
// by inode and hash is moved in Drive (MoveFunc) instead of uploaded, and a
// file deleted locally is moved to the Drive trash (TrashFunc). A renamed folder is
// moved as a folder (MoveDirFunc) and an emptied, deleted one is trashed.
// Decide is the pure three-way compare (local, Drive, base) behind phase 2.
// Restore fills a root from a Drive listing on a new machine. Control and
// ServeControl let a running daemon be paused, resumed and queried over a Unix
// socket.
package sync
