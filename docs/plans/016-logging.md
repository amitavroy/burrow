# Ticket 14: Logging (GH-14)

Source: `requirements.md` section 14, ticket 14 (M3 Robust uploads), and section 10. Depends on 1, done. Unblocks the daemon work (21, 23) and the error table (18).
Done when: **log lines carry path, job ID and Drive file ID.**
Final location: `docs/plans/016-logging.md` (same format as `docs/plans/archive/015-*.md`).

## To-do

Vertical slices; review after each. Every slice leaves `make vet test` green.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: logger setup** | `cmd/syncd/log.go`: `setupLogger(stderr) (closeFn, error)` builds a `slog` text handler and installs it with `slog.SetDefault`. `SYNC_LOG` (`debug`, `info`, `warn`, `error`; bad value is a usage error, exit 2) sets the level and sends output to stderr. Unset: level `info`, output to a `lumberjack` file `burrow/logs/syncd.log` in the `adrg/xdg` data dir (Local on Windows), size-based rotation, 10 backups, dir mode 0700. `store.LogDir()` or equivalent computes the path without creating it, like `store.DefaultPath`. Add `gopkg.in/natefinch/lumberjack.v2`. Wire it into `main` only (not `run`), so tests keep a discard logger. Tests: level parsing table, bad value rejected, file output goes to the temp dir, rotation config, `closeFn` flushes. **Review point.** | [x] |
| 2 | **Slice 2: log the sync path** | Use `slog.Default()` in `internal/sync` and `internal/drive`. Attributes: `path` (rel_path), `job` (per-file sequence number within a run, a stand-in until the queue in ticket 17 gives real job IDs), `drive_file_id`. Events: info on upload, update and skip-refresh; warn on a per-file failure; error on an abort; debug on the MD5 pre-check and Drive calls. Never log tokens, auth headers, request bodies or file contents (CLAUDE.md). Tests: capture with a `slog` handler into a buffer; assert `path`, `job` and `drive_file_id` on the upload and update lines, and that a canary token never appears. **Review point (demo: `SYNC_LOG=debug syncd sync --root tree` shows lines with all three attributes).** | [ ] |
| 3 | **Slice 3: docs** | Wiki new "Logging" section (levels, where output goes, rotation, attributes, the never-log rule), README "Logging" section, package docs, `=> Done` for ticket 14 in `requirements.md`. **Review point.** | [ ] |

## Context

No logging exists yet: commands print results to stdout and errors to stderr, and nothing records what a long run did. The daemon (tickets 21 and 23) and the `sync_errors` table (18) need structured logs first. Logs are diagnostics only; user-facing errors will come from `sync_errors`, never from parsing log files.

## Decisions

1. **`log/slog` text handler, `slog.SetDefault` once in `main`.** Packages call `slog.Default()` instead of taking a logger parameter, which keeps `Sync` and `Uploader` signatures unchanged. Tests install their own handler.
2. **Where output goes.** `SYNC_LOG` set means development: text to stderr at that level. Unset means production: `info` to a rotating file. stderr stays free for command output (the sync summary) by default. This is the one interpretation of section 10 that needs your call (see below).
3. **Rotation.** `lumberjack`, 10 MB files, 10 backups, compressed. The "debug logging toggle" in settings belongs to the tray UI (ticket 36); only the environment variable exists now.
4. **Job ID.** There is no queue yet, so `job` is a per-run counter per file. Ticket 17 replaces it with the real job ID without changing the attribute name.
5. **Never log** tokens, auth headers, request bodies, SQL arguments or file contents. Only rel_paths, Drive file IDs, sizes and error text.
6. **Panic recovery logging** is ticket 23, not this one.
7. **Close and flush** the file on exit via the returned `closeFn`.

## Files to create/modify

| File | Change |
| --- | --- |
| `cmd/syncd/log.go` (+ `log_test.go`) | `setupLogger`, level parsing |
| `cmd/syncd/main.go` | call it from `main` |
| `internal/store/path.go` | log directory helper |
| `internal/sync/sync.go`, `internal/drive/upload.go` (+ tests) | log calls |
| `go.mod`, `go.sum` | `lumberjack.v2` |
| `README.md`, `docs/wiki/index.md`, `requirements.md`, package docs | Docs and status |

## Verification

1. `make vet test` under `-race`.
2. `SYNC_LOG=debug bin/syncd sync --root tree`: stderr shows lines with `path`, `job` and `drive_file_id`.
3. Without `SYNC_LOG`: stderr shows only command output and the lines land in `~/.local/share/burrow/logs/syncd.log`.
4. `SYNC_LOG=bogus bin/syncd version` exits 2 with a clear message.
5. `grep -i "token\|authorization" ~/.local/share/burrow/logs/syncd.log` finds nothing after a login and a sync.
