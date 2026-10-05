// Package store owns the SQLite state database (a rebuildable cache) and its
// migrations. Open applies the pragmas and runs the embedded goose migrations;
// the returned *sql.DB must be owned by a single goroutine.
package store
