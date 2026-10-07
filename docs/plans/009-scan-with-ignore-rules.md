# Ticket 9: Scan with ignore rules (`syncd scan --dry-run`) (GH-8)

Source: `requirements.md` section 14, ticket 9 (M2 One-shot sync). Depends on 1 (done).
Done when: **`syncd scan --dry-run` lists files that would upload.**
Final location after approval: `docs/plans/009-scan-with-ignore-rules.md` (same format as `docs/plans/007-*.md`).

## To-do

Vertical slices; review after each. Every slice leaves `syncd scan --dry-run` runnable.

| # | Slice | Step | Done |
| --- | --- | --- | --- |
| 1 | **Slice 1: scan lists files** | `internal/sync/scan.go`: `Scan(root)` with `filepath.WalkDir`, regular files only, slash-form `rel_path`, sorted. `cmd/syncd/main.go`: `scan` command with `--dry-run` and `--root` (default `~/MySync`), one `rel_path` per line, exit codes. Tests on a temp tree. **Review point (demo: lists a real folder).** | [x] |
| 2 | **Slice 2: default ignores** | Pick the gitignore library (see Decisions 5), `go get` it. `internal/sync/ignore.go`: matcher with the built-in defaults `.git`, `node_modules`, `*.tmp`, `~$*`; `Scan` skips ignored directories whole (`fs.SkipDir`). Tests for each default, nested matches and a directory skip. **Review point (demo: `.git` and `node_modules` vanish from the listing).** | [x] |
| 3 | **Slice 3: `.syncignore`** | Read `<root>/.syncignore` if present and add its rules to the matcher (same syntax, negation, `**`, trailing `/` for directories). The file itself is ignored by the scan so it is never uploaded. Tests for each syntax case, a missing file, and an unreadable one. **Review point.** | [ ] |
| 4 | **Slice 4: edge cases and output** | Skip symlinks and non-regular files; report unreadable entries without aborting; a missing or non-directory root exits 1 with a clear message; print a count line to stderr. Tests through `run(args, ...)`. **Review point.** | [ ] |
| 5 | **Slice 5: docs** | README ("Scanning" with the usage block), `docs/wiki/index.md` (new "Scanning" section, ADR note on the matcher choice), `internal/sync/doc.go`; mark ticket 9 `=> Done` in `requirements.md`. **Review point.** | [ ] |

## Context

Tickets 11 (one-shot sync) and 22 (startup reconciliation) both start from "walk the root, apply ignore rules, get a list of candidate files". This ticket builds that list on its own, before the DB or Drive are involved, so the same function serves the CLI dry run now and the queue later. It is deliberately independent of tickets 7 and 8: it reads only the local folder.

Drive is the source of truth and the root is the only thing scanned. The scan never writes, so a dry run is always safe.

## Decisions

1. **Where it lives:** `internal/sync` (watcher, debounce, queue, uploader live there too). The package has no code yet apart from `doc.go`. The `scan` command only parses flags and prints.
2. **`Scan(root string) ([]Entry, error)`** returns `Entry{RelPath string, Size int64, MTime int64}`, sorted by `RelPath`. `RelPath` is slash-form, relative to the root, and never contains `..` or a machine-specific part. Size and mtime are returned now because ticket 11 and the MD5 skip need them, and the dry run can print them.
3. **Root:** `--root DIR`, default `~/MySync` (Linux; the home dir via `os.UserHomeDir`). No config file exists yet, so a persisted root is later (tickets 28 and 36). A missing or non-directory root exits 1 with a message; it is never created.
4. **`--dry-run` is required.** Without it the command prints usage and exits 2, because actually uploading is ticket 11. This stops anyone mistaking the command for a sync. Output is one `rel_path` per line on stdout, so it pipes cleanly, and a summary (`N files, M ignored`) on stderr.
5. **Matcher:** a gitignore-style library, as requirements section 3 names (`go-gitignore`). Pick in slice 2 by trying the candidates against the same table of cases and taking the first that handles negation (`!`), `**`, anchored patterns (`/foo`), directory-only patterns (`dir/`) and is pure Go with few dependencies. Candidates: `github.com/sabhiram/go-gitignore`, `github.com/go-git/go-git/v5/plumbing/format/gitignore`. Record the choice and the reason as ADR-005 in the wiki. A hand-written matcher is rejected: gitignore semantics are easy to get subtly wrong.
6. **Default ignores:** `.git`, `node_modules`, `*.tmp`, `~$*` (requirements section 7), applied before any `.syncignore` rule, and prefixed so `.syncignore` negation cannot re-include `.git`. Editors' temp files (`~$*`, `*.tmp`) are the reason: they appear and vanish on every save.
7. **`.syncignore`:** optional file at the root with the same syntax. It is itself ignored by the scan, so it is never uploaded. Whether it should sync to a new machine is a question for ticket 28 (`config.json` in Drive), not this ticket. A missing file is fine; an unreadable one is an error naming the file.
8. **Directories:** an ignored directory returns `fs.SkipDir`, so `node_modules` is never walked. Directories themselves are not listed, only files, because Drive folders are created lazily from `rel_path` (ticket 10).
9. **Symlinks and special files:** skipped (CLAUDE.md defers symlinks). The check is `d.Type().IsRegular()`. A symlinked directory is not followed, since `WalkDir` does not follow symlinks.
10. **Errors during the walk:** an unreadable file or directory is reported on stderr and skipped, and the scan continues; the command still exits 0 unless the root itself fails. One locked file must not hide the rest of the tree.
11. **Case and separators:** matching runs on slash-form paths on every OS. Case sensitivity follows the library; Windows case-insensitivity is noted but not solved here.
12. **Never log file contents or tokens.** Paths are fine in output; nothing here touches secrets.

## Shape

```go
// internal/sync/scan.go
type Entry struct {
	RelPath string // slash form, relative to the root
	Size    int64
	MTime   int64  // Unix nanoseconds, same unit as store.File
}

func Scan(root string) ([]Entry, error)

// internal/sync/ignore.go
type Matcher struct{ /* wraps the chosen library */ }

func NewMatcher(root string) (*Matcher, error) // defaults + <root>/.syncignore
func (m *Matcher) Ignored(relPath string, isDir bool) bool
```

```
syncd scan --dry-run [--root DIR]
  -> NewMatcher(root): defaults + .syncignore
  -> filepath.WalkDir(root):
       ignored dir  -> fs.SkipDir
       ignored file -> counted, skipped
       symlink / non-regular -> skipped
       unreadable   -> stderr, skipped
       otherwise    -> Entry{rel_path, size, mtime}
  -> print rel_path per line (stdout), "N files, M ignored" (stderr)
```

## Files to create/modify

| File | Change |
| --- | --- |
| `internal/sync/scan.go` (+ test) | `Scan`, `Entry` |
| `internal/sync/ignore.go` (+ test) | `Matcher`, defaults, `.syncignore` |
| `internal/sync/doc.go` | Update the package comment |
| `cmd/syncd/main.go` | `scan` command with `--dry-run` and `--root` |
| `cmd/syncd/main_test.go` | Cases for `scan` |
| `go.mod`, `go.sum` | Add the gitignore library |
| `README.md`, `docs/wiki/index.md`, `requirements.md` | Docs and status |

Reused: the `run(args, stdout, stderr)` dispatch, the `put` command's `flag.FlagSet` and `--root` conventions, the 0 / 1 / 2 exit-code convention, and `relPathFor`'s rule that a `rel_path` never has `..`.

## Testing plan

Table-driven, with trees built under `t.TempDir()`; no real home dir (the CLI test passes `--root` explicitly and the default-root test overrides the home lookup through a package var, like `dbPath`).

- Scan returns only regular files, sorted, with slash-form paths, correct size and mtime.
- Each default ignore matches at the root and nested (`a/b/node_modules/x`, `report.tmp`, `~$draft.docx`), and `.git` is never walked (a test directory that would fail the test if read).
- `.syncignore` cases: plain name, glob, `**`, anchored `/foo`, `dir/`, negation `!keep.tmp`, comments and blank lines, CRLF line endings, a missing file, an unreadable file.
- A `.syncignore` rule cannot re-include `.git`.
- The `.syncignore` file itself does not appear in the listing.
- Symlinks to a file and to a directory are skipped; a FIFO is skipped where the OS supports it.
- An unreadable subdirectory (mode 000) is reported and the rest is still listed (skipped on Windows and when running as root).
- A missing root and a file passed as root exit 1; `scan` without `--dry-run` exits 2.
- Run `go test -race ./...`.

## Verification

1. `make vet test` passes under `-race`.
2. Make a sample tree with `.git/`, `node_modules/`, `a.tmp`, `~$b.docx`, a symlink and a few real files; `bin/syncd scan --dry-run --root <tree>` lists only the real files, and stderr shows the counts.
3. Add a `.syncignore` with `*.log` and `!keep.log`; the listing drops other logs and keeps `keep.log`.
4. `bin/syncd scan --dry-run` with no `--root` scans `~/MySync` (or says clearly that it does not exist), and creates nothing.
5. `bin/syncd scan` (no `--dry-run`) and `bin/syncd scan --dry-run --root /no/such/dir` fail with usage (2) and a clear error (1).
6. Scan a large tree (for example a checked-out repo with `node_modules`) and confirm `node_modules` is skipped quickly, never walked.
