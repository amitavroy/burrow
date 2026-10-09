// Package drive wraps the Google Drive client and the OAuth login flow.
// Upload mirrors a file's rel_path as nested folders under MySync, creating
// them lazily and caching their IDs in the state database. An Uploader reuses
// one Drive service and MySync lookup to send many files. Upload finds an
// existing file by its rel_path tag and updates it in place, so a changed file
// keeps its Drive file ID. Content is streamed in 8 MB resumable chunks, so
// memory use does not grow with file size.
package drive
