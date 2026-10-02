---
name: do-work
description: Work on the next incomplete task from a plan doc in docs/plans/
disable-model-invocation: true
argument-hint: <plan-name-or-number>
---

You do exactly one task from a plan document, then stop.

## Step 1 — Resolve the plan file

The argument names a plan under `docs/plans/` — a number (`021`), a number-prefixed slug (`021-category-taxonomy`), or a full filename. Resolve it to a single file in `docs/plans/`:

- If the argument is a bare number or number prefix, match the file whose name starts with it (e.g. `021` or `021-category-taxonomy` both match `docs/plans/021-category-taxonomy.md`).
- If nothing matches, or more than one file matches, list the close candidates and ask the user to disambiguate rather than guessing.

Read the whole file — the `## To-do` table plus every other section (Context, Decisions, New/changed files, Testing plan, Verification). The table tells you *which* task is next; the rest of the doc tells you *how* to do it correctly.

## Step 2 — Find the next incomplete task

The `## To-do` table has rows like `| [ ] | 3 | Add \`Category\` model |`. Find the **first** row still marked `[ ]` (not `[x]`).

- If every row is already `[x]`, tell the user the plan is fully complete and stop — do not pick a task on your own.
- Tasks are meant to be done in order because each depends on the ones before it. If the first unchecked row depends on something that doesn't actually exist yet in the codebase (an earlier task was checked off but not really done), stop and tell the user rather than trying to patch over it.

## Step 3 — Do only that one task

Implement exactly the scope of that single row — no more. Follow the codebase's own conventions:

- Follow CLAUDE.md and the `.ai/rules` index for any files you touch (read the matching rule files before writing code, per the standing project instructions).
- Use the plan's own "New/changed files" code samples as the intended shape, but reconcile them with what already exists in the repo (e.g. an `artisan make:*` stub, sibling files' actual conventions) rather than blindly overwriting.
- If the task is a test-writing/running step, follow `testing-best-practices` and actually run the tests.
- If the task is a formatting/verification step already covered by earlier tasks' work, just run it.

Do not start the next task, even if it looks trivial or tightly coupled — leave it for the next invocation.

## Step 4 — Mark the task done

Edit the plan file: flip that row's `[ ]` to `[x]`.

## Step 5 — Format and report

Run `vendor/bin/pint --dirty --format agent` if any PHP files changed. Run any narrowly-scoped tests the task calls for.

Then stop. Report: which task you did, the files you changed, and any tests you ran (pass/fail). Wait for the user to review and explicitly approve. Do not ask about committing in the same turn as reporting completion — only bring up committing after the user has confirmed the work looks good, and never commit without the user's explicit go-ahead for that specific commit.
