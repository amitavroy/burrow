---
name: do-commit
description: Commit staged/unstaged changes with an auto-generated message and optional ticket reference
disable-model-invocation: false
---

Commit the current working tree changes. Follow these steps exactly:

## Step 1 — Determine the ticket ID

Work out which GH ticket the current changes belong to by checking these sources, in order. Stop at the first one that gives a confident, unambiguous answer:

1. **Current branch name** — if it embeds a ticket reference (e.g. `gh-41-early-access-admin`, `feature/GH-41-...`), use it.
2. **Recent commit history for the files being changed** — run `git log --oneline -10 -- <changed files>` (use the files from Step 2's `git status`). If recent commits touching the same files consistently reference a ticket in their subject (e.g. `(GH-41)`), that's almost certainly the ticket still being worked on.
3. **A `docs/plans/*.md` or `docs/plans/archive/*.md` design doc related to the diff** — if one of the changed files is a plan doc, or an unchanged plan doc (active or already archived by `do-close-ticket`) clearly matches the feature being touched, check its title/header for a ticket reference (design docs in this repo are titled like `# Some Feature (GH-41)`).
4. **An active OpenSpec change folder** under `openspec/changes/` whose name embeds a ticket reference (e.g. `gh17-fetch-text-markdown` → `GH-17`, matching a pattern like `gh-?(\d+)` at the start of the folder name). Identify the relevant folder from context (the change currently being implemented, or inferred from files touched in the diff) rather than guessing among unrelated changes.

- If a ticket ID is found this way, normalize it to the `GH-42` style and skip asking the user.
- If the sources disagree, or none confidently identify a ticket, fall back to asking the user once with AskUserQuestion:

> "Do you have a git ticket ID to include in this commit?"

Options:
- "Yes, I have a ticket ID"
- "No ticket"

If the user selects "Yes", ask a second AskUserQuestion:

> "Enter your ticket ID (e.g. GH-42, PROJ-123):"

Provide an "Other" option so the user can type the value freely.

## Step 2 — Inspect the working tree

Run these in parallel:
- `git status` — identify changed, staged, and untracked files
- `git diff HEAD` — see all unstaged and staged changes

Do NOT include files that look like secrets (.env, credentials, private keys).

## Step 3 — Draft the commit message

Read the diff and understand, at a high level, what was actually done — not a file-by-file listing. Write a commit message with a subject and a body:

- **Subject line**: one line, imperative mood, ≤72 chars, no trailing period — a single clear sentence describing what this commit does.
- Use conventional commit prefixes: `feat:`, `fix:`, `chore:`, `docs:`, `refactor:`, `test:`
- If a ticket ID was found or provided, append it at the end of the subject line in parentheses, e.g. `feat: add content crawl endpoint (GH-42)`
- **Body**: one blank line after the subject, then bullet points covering the high-level things that were done (new behavior, files/classes added, notable decisions). Do not list every changed file — group related changes into one bullet and skip anything trivial (formatting, minor renames). Keep each bullet short; the goal is that someone skimming the bullets understands what happened without reading the diff.
- **Archive commits close the ticket:** if the diff moves a change folder into `openspec/changes/archive/` (i.e. this commit archives a completed OpenSpec change), the underlying ticket is done — add a closing line at the end of the body so GitHub auto-closes the issue on merge: `Closes #<number>`, using the numeric part of the ticket ID (e.g. ticket `GH-24` → `Closes #24`). Only add this when a ticket ID is present and the commit is an archive commit; skip it for regular implementation commits on the same ticket.
- Keep it concise — caveman style, not verbose. Bullets over prose.

### Sign-off

Run `git log -5 --format='%B'` to check how recent commits in this repo sign off. Reuse that exact convention. Do not invent a different sign-off or skip it just because the diff is small — if recent commits carry a `Co-Authored-By:` (or similar) trailer, every new commit must carry the same one. If no recent commit has a trailer to copy, fall back to the attribution line given in the current session's instructions, if any.

## Step 4 — Ask for confirmation

Show the drafted commit message (subject and body) to the user, then use AskUserQuestion to ask:

> "Does this commit message look good?"

Options:
- "Looks good, commit it"
- "I want to change the message"

If the user selects "I want to change the message", ask a follow-up AskUserQuestion:

> "Enter your preferred commit message:"

Provide an "Other" option so the user can type freely. Use their message as the subject line (still append the ticket ID if one was provided and it is not already present). Keep the drafted body unless the user's replacement text also includes a body.

## Step 5 — Stage and commit

Stage relevant files by name (avoid `git add -A` to prevent accidentally including sensitive files).

Create the commit using a HEREDOC, using the sign-off trailer determined in Step 3:

```
git commit -m "$(cat <<'EOF'
<subject line>

<body>

<sign-off trailer>
EOF
)"
```

## Step 6 — Confirm

Run `git log --oneline -1` and report the final commit hash and message to the user.
