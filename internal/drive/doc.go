// Package drive wraps the Google Drive client and the OAuth login flow.
// Upload mirrors a file's rel_path as nested folders under MySync, creating
// them lazily and caching their IDs in the state database. An Uploader reuses
// one Drive service and MySync lookup to send many files.
package drive
