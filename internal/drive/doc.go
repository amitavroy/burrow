// Package drive wraps the Google Drive client and the OAuth login flow.
// Upload mirrors a file's rel_path as nested folders under MySync, creating
// them lazily and caching their IDs in the state database.
package drive
