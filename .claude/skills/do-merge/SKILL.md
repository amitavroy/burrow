---
name: do-merge
description: Rebase the current ticket branch onto latest main, push it, then fast-forward merge it into main and push
disable-model-invocation: false
---

Sync the current ticket branch with main and merge it in. Follow these steps exactly, in order.

## Step 1 — Confirm we're on a ticket branch

Run `git branch --show-current`.

- If the current branch is `main` (or `master`), stop here. Tell the user there is no ticket branch checked out, so there is nothing to merge, and do nothing else.
- Otherwise, treat this branch as the ticket branch for the rest of the flow. Remember its name.

## Step 2 — Stash uncommitted changes

Run `git status --porcelain` to check for uncommitted changes (staged, unstaged, or untracked).

If there are any, stash them (including untracked files) with a descriptive message:

```
git stash push -u -m "do-merge: autostash before rebase on <branch>"
```

Remember whether a stash was created — it needs to be restored in Step 7. If the working tree is clean, skip stashing.

## Step 3 — Update main

```
git checkout main
git pull
```

## Step 4 — Rebase the ticket branch onto main

```
git checkout <branch>
git rebase main
```

If the rebase stops due to conflicts, do not attempt to resolve them automatically. Stop and tell the user which files conflict, and that they can resolve and `git rebase --continue`, or `git rebase --abort` to cancel. Do not proceed to later steps until the rebase is cleanly finished.

## Step 5 — Push the rebased branch

The rebase rewrote this branch's history, so the push must be forced. Before running it, use AskUserQuestion to confirm:

> "About to force-push `<branch>` after the rebase. Continue?"

Options:
- "Yes, force-push"
- "Stop here"

If confirmed:

```
git push --force-with-lease
```

If the user chooses to stop, leave everything as-is (branch rebased locally, not yet pushed) and tell them how to resume (re-run `/do-merge`, or push manually).

## Step 6 — Merge into main

```
git checkout main
```

Before merging, use AskUserQuestion to confirm:

> "About to fast-forward merge `<branch>` into `main` and push. Continue?"

Options:
- "Yes, merge and push"
- "Stop here"

If confirmed:

```
git merge <branch> --ff-only
git push
```

If the `--ff-only` merge fails (main moved again in the meantime), stop and tell the user — do not fall back to a non-fast-forward merge or rebase again automatically.

If the user chooses to stop at the confirmation, leave `<branch>` pushed but unmerged and report that.

## Step 7 — Restore stashed changes

If a stash was created in Step 2:

```
git checkout <branch>
git stash pop
```

If popping the stash produces conflicts, stop and tell the user the stash is still present (`git stash list`) and let them resolve it manually — do not attempt automatic conflict resolution.

## Step 8 — Delete the merged branch and return to main

Use AskUserQuestion to confirm:

> "`<branch>` has been merged into main. Delete it locally and on origin, and switch back to main?"

Options:
- "Yes, delete and switch to main"
- "No, keep the branch"

If confirmed:

```
git checkout main
git branch -d <branch>
git push origin --delete <branch>
```

- If `git checkout main` fails because of leftover uncommitted changes (e.g. from the restored stash conflicting with main), stop and tell the user to commit or stash those changes first — do not force the checkout, and do not delete the branch while it's still checked out elsewhere.
- `git branch -d` (not `-D`) is intentional — it only deletes a branch git considers fully merged. If it refuses, stop and report why rather than forcing it with `-D`.
- If the remote delete fails (e.g. already gone, or no permission), report that but leave the local deletion in place.

If the user chooses "No, keep the branch", skip this step entirely — stay on `<branch>` and do not switch to main.

## Step 9 — Report

Summarize what happened: whether a stash was created/restored, the rebase result, whether the branch was pushed, whether the ff-only merge into main succeeded and was pushed, whether the branch was deleted locally/remotely, and the final branch the user is on.
