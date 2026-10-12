// Package drive wraps the Google Drive client and the OAuth login flow.
// Upload mirrors a file's rel_path as nested folders under MySync, creating
// them lazily and caching their IDs in the state database. An Uploader reuses
// one Drive service and MySync lookup to send many files. Upload finds an
// existing file by its rel_path tag and updates it in place, so a changed file
// keeps its Drive file ID. Content is streamed in 8 MB resumable chunks, so
// memory use does not grow with file size. Upload retries 429, 5xx, rate-limit 403s
// and transient network errors with jittered exponential backoff (5 attempts);
// the tag lookup at the start of each attempt keeps a retry from duplicating a file.
// Describe turns an upload error into the short readable message shown by
// syncd errors. Uploader.Move renames and moves a Drive file with one update
// (addParents/removeParents), keeping its ID and revisions. ReadConfig and
// WriteConfig keep the shared settings file (config.json) under MySync. Download saves a Drive file through a temporary file, verified by MD5 and
// renamed into place. ListTree lists every file under MySync with its tag,
// MD5, size and head revision. StartPageToken and Changes read the Drive changes feed
// (the files that changed since a saved position); an expired position gives
// ErrPageTokenExpired.
package drive
