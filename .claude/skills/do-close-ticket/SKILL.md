---
name: do-close-ticket
description: Verify a docs/plans/ ticket's To-do table is fully checked, update docs/wiki/index.md (the durable source of truth for future planning), close the linked GitHub issue, and archive the plan doc
disable-model-invocation: true
argument-hint: <plan-name-or-number>
---

Close out a finished `docs/plans/` ticket. This runs after every task in a plan is believed done: it gates on the plan's own To-do table, folds what was actually built into `docs/wiki/index.md`, closes the linked GitHub issue, and archives the plan doc. Follow these steps in order.

## Step 1 — Resolve the plan file

The argument names a plan under `docs/plans/` — a number (`021`), a number-prefixed slug (`021-category-taxonomy`), or a full filename. Resolve it to a single file:

- If the argument is a bare number or number prefix, match the file whose name starts with it.
- If nothing matches, or more than one file matches, list the close candidates and ask the user to disambiguate rather than guessing.

Read the whole file — the `## To-do` table plus every other section (Context, Decisions, Implementation notes if present, Files to create/modify, Testing plan, Verification).

## Step 2 — Confirm every To-do row is checked

Read the `## To-do` table. Every row must be `[x]`.

- If any row is still `[ ]`, stop. List the unfinished rows by number and task, tell the user to run `do-work` on them first, and go no further — no wiki write, no issue close, no archiving.
- Checked rows are trusted as-is. This step is a completeness gate on the plan doc itself, not a re-audit of the implementation against the codebase — don't grep the code or run tests to second-guess a checked row.

## Step 3 — Resolve the GH ticket number

The plan's H1 is titled `# Title (GH-N)` — read the number directly from it.

- If it's `GH-TBD` (a few early plans predate their issue), tell the user no ticket is linked. Skip the issue-closing part of Step 5, but still do the wiki update and archive step.

## Step 4 — Draft the wiki change: update in place, or append

`docs/wiki/index.md` is a single flat file of `# Topic` sections — never create a second file (existing convention: see plan `004-pinecone-concept-dedup`'s "Wiki documentation" section, which documents this exact rule for its own topic — check `docs/plans/` first and `docs/plans/archive/` if it's since been closed out). Read the whole file first, then decide which case applies:

- **Modifies existing documented behavior** — the plan's Decisions/Context describe changing something a heading already covers (e.g. a new threshold, an added branch in an existing decision flow, a renamed field). Edit that section in place so it reflects current behavior. Don't leave the old value/branch behind as if still true, and don't bolt on a second near-duplicate section — the file has no changelog concept, it only ever describes "how it works now."
- **Introduces new domain behavior** — nothing existing covers it. Append a new `# Topic` section matching the file's established shape: a short rule statement up front, a decision flow in plain ASCII arrows (not Mermaid — the file explicitly avoids that even when a plan's own working notes used Mermaid), a table for discrete states/outcomes where relevant, concrete (dated, if time-sensitive) examples or code/config snippets, and a short cross-reference note if it's easily confused with an existing section (the file already does this, e.g. "Not the same as due window").

Either way, source the content from the plan's Decisions/Context and any "Implementation notes (found while building)" section — write what the system *actually does now*, not a copy of the plan or a changelog of what changed. Show the user the exact section/diff before writing it.

## Step 5 — Write, close, and archive

- Apply the confirmed change to `docs/wiki/index.md` (in-place edit or append, per Step 4).
- If a real GH number was found in Step 3: draft a short closing comment (what shipped, one line pointing at the wiki section by heading name). Show it to the user, then confirm with AskUserQuestion:

  > "About to close GH-`<n>` with this comment. Continue?"

  Options:
  - "Yes, close it"
  - "No, skip closing"

  If confirmed:
  ```bash
  gh issue close <n> --comment "<comment>"
  ```
- Move the plan doc into the archive:
  ```bash
  mkdir -p docs/plans/archive
  git mv docs/plans/<file> docs/plans/archive/<file>
  ```
  Same filename, flat — no dated subfolder. The existing `NNN-` prefix already gives chronological ordering, so there's no need for the dated-folder scheme `openspec/changes/archive/` uses.

## Step 6 — Report and stop

Summarize: which plan was checked, whether it was already fully checked off, the wiki change made (heading, and whether it was an in-place edit or a new section), whether/which issue was closed, and the plan doc's new path under `docs/plans/archive/`.

Never touch the plan file's checkboxes (that's `do-work`'s job) or any implementation code — this skill only reads the plan to gate completeness, writes to `docs/wiki/index.md`, closes the issue, and moves the plan file.

---

**Guardrails**

- Never create a second file under `docs/wiki/` — always edit or append within `index.md`.
- If the plan modified behavior an existing wiki section already documents, edit that section in place — never leave a stale duplicate describing the old behavior.
- Never close the GitHub issue without showing the drafted comment and getting explicit confirmation first.
- Never write the wiki change, close the issue, or archive the plan if any To-do row is still unchecked — stop and report instead.
- Archive with `git mv`, not a plain move, so history follows the file.
- If the plan's title is `GH-TBD`, skip issue-closing but still do the wiki update and archive step.
- This skill never re-verifies checked To-do rows against the codebase (no grepping for symbols, no running tests) — it trusts the plan doc's own checkboxes.
