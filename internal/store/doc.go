// Package store owns the SQLite state database (a rebuildable cache) and its
// migrations. Open applies the pragmas and runs the embedded goose migrations;
// the returned *sql.DB must be owned by a single goroutine. Repo is that owner
// for the files table: OpenRepo starts the goroutine, and typed methods
// (Upsert, Get, GetByDriveID, List, Delete) talk to it over a channel.
package store
