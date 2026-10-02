---
name: do-create-ticket
description: Turn a requirement into a docs/plans/ design doc — sourced either from the current conversation or from an existing GitHub ticket, whichever the user points at. Creates a new GitHub issue first only when the source is chat (no ticket exists yet). Asks follow-up questions if the requirement has gaps. Orders the to-do list in vertical, user-visible slices rather than horizontal layers. Stops once the plan doc is written — does not implement anything.
license: MIT
compatibility: Requires gh CLI authenticated against the project's GitHub repo.
metadata:
  author: project
  version: "2.1"
---

Turn a requirement into a `docs/plans/` design doc. The requirement can come from either of two places:

- **The current conversation** — an idea, bug report, or feature request discussed in this session (whether or not it was already fully worked out into an agreed plan).
- **An existing GitHub ticket** — when the user points at one (an issue number like `#45`/`GH-45`, or an issue URL).

The skill's primary job is to **understand the requirement**, asking follow-up questions where the source material is thin, then produce the plan document. It does not implement anything — once the plan doc is saved, the skill's job is done and it waits for the user to decide what happens next.

---

**Steps**

1. **Determine the source**

   Check what the user said when invoking this skill:
   - If they reference an existing GitHub ticket (`#45`, `GH-45`, an issue URL, or similar), the source is that ticket. Fetch it:
     ```bash
     gh issue view <number> --json title,body,url,comments,labels
     ```
   - Otherwise, the source is the current conversation — use whatever's already been discussed.
   - If it's genuinely unclear which is meant, ask before proceeding.

2. **Understand the requirement**

   Read the source closely: what needs to be built, why, and any constraints or decisions already surfaced (in chat, or in the ticket's body/comments). Treat the source as a requirement to be understood, not necessarily a finished design — a ticket or a conversation may be thin, informal, or leave gaps (unclear scope, data model, UI, edge cases).

3. **Ask follow-up questions if needed**

   If there are open questions that materially affect the plan, ask them now (AskUserQuestion or plain chat questions) before drafting anything — don't guess at decisions the user should make. If the source is already detailed and unambiguous (e.g. a plan already fully agreed in this conversation, or a well-specified ticket), skip this — don't manufacture questions for the sake of it.

4. **If sourced from chat: draft and create the GitHub issue**

   Skip this entire step if the source is an existing ticket (step 1) — reuse its number/title/URL as-is, do not create a duplicate issue.

   Otherwise:
   - **Title**: imperative, concise, under 72 chars (e.g. "Add route + page to generate and display a user's quiz").
   - **Body**: a short Context paragraph, key **Decisions** as bullets grouped by area, a **Scope** list, an **Out of scope** list if relevant, and any prior ticket numbers the requirement builds on (e.g. "GH-7"). End with a line pointing at the full doc (fill in after step 5):
     ```
     Full design doc: `docs/plans/<NNN>-<slug>.md`
     ```
   - Show the drafted title/body to the user and proceed unless they redirect — never create the issue silently.
   - Create it:
     ```bash
     gh issue create --title "<title>" --body "<body>"
     ```
     Check `gh label list` for a fitting existing label and pass `--label "<label>"` if one clearly applies; don't invent new labels. Parse the printed URL to get the issue number.

5. **Determine the plan document's number and filename**

   The `docs/plans/` numbering is independent of the GitHub issue number — GitHub assigns issue numbers itself; you cannot and should not try to control those. The `docs/plans/` prefix is a separate, sequential counter you maintain yourself:

   - If `docs/plans/` does not exist yet: create it (`mkdir -p docs/plans`) and use `001` as the number.
   - Otherwise, list the files already in `docs/plans/` **and `docs/plans/archive/`** (closed-out tickets move there — see `do-close-ticket`), extract the leading numeric prefix from each (files look like `003-sm2-quiz-generator.md`), take the highest one, and add 1. **Always zero-pad the result to at least 3 digits** — after `009` comes `010`, not `10`; after `015` comes `016`. A quick way to get the next number, covering both locations:
     ```bash
     find docs/plans -maxdepth 2 -type f -name '[0-9]*.md' | xargs -n1 basename | grep -E '^[0-9]+-' | sed -E 's/^([0-9]+)-.*/\1/' | sort -n | tail -1
     ```
     then increment and format with `printf '%03d\n' <n>`.
   - Derive a kebab-case slug from the issue/ticket title (3-6 words, lowercase, hyphens, drop stop words if needed).
   - Filename: `docs/plans/<NNN>-<slug>.md`.

6. **Confirm the target filename before writing**

   Show the user the target filename (e.g. `Will write docs/plans/006-foo-bar.md`). Proceed unless they redirect — this is a lightweight sanity check, not a full re-review of the requirement.

7. **Order the to-do list into vertical slices, not horizontal layers**

   Structure the plan's to-do list so each slice cuts through every layer (migration → model → repository/service → controller/route → frontend) for one complete, user-visible piece of functionality, rather than grouping steps by layer across the whole feature.

   For a CRUD-shaped feature, that means: slice 1 delivers the listing/index page end-to-end (migration, model, read-path repository method, index controller/route, listing frontend page) before slice 2 touches create; slice 2 delivers create end-to-end before slice 3 touches edit; then edit; then delete. Do **not** plan "all repository methods, then all controllers, then the whole frontend" as separate phases — that defers anything demoable until the very end.

   Apply the same slicing logic to any multi-part feature, not just CRUD: order steps so the earliest slices produce something the user can look at and react to, and later slices build on that feedback instead of all landing in one final review. Group each slice's steps together and note in the to-do table (or a short preceding note) where one slice ends and the next begins, so it's clear the user can review and confirm after each slice rather than only at the end.

8. **Write the full plan document**

   Read one existing file in `docs/plans/` first (if any exist) to match its structure exactly — typically a "To-do" checklist table at the top (one row per implementation step, all unchecked for a new plan, ordered per step 7), then **Context**, **Decisions** (numbered, with rationale), concrete code/file snippets, a **Files to create/modify** table, a **Testing plan**, and a **Verification** section. Title the doc with the ticket reference (e.g. `# Feature Name (GH-45)`), matching existing convention. Write the *full* plan detail here, based on your understanding from steps 2-3 — not a condensed summary.

   Save it to `docs/plans/<NNN>-<slug>.md` from step 5.

9. **Report back and stop**

   Give the user the plan doc path, plus the issue URL/number if one was created in step 4 (or the existing ticket's URL/number if sourced from one). Nothing else happens automatically — do not start implementing, do not write code, do not open a PR. The task for this skill ends here; wait for the user to confirm what they want to happen next.

---

**Guardrails**

- Never create a GitHub issue silently — always show the drafted title/body first (step 4), and only create one at all when the source is chat, not an existing ticket.
- Don't skip follow-up questions when the requirement has real gaps — but don't manufacture questions when the source is already clear.
- This skill's job ends once the plan doc is saved. Never proceed to implementation, tests, or further code changes in the same run, even if the original request implied building the feature — only produce the plan and wait for explicit confirmation.
- The `docs/plans/` number and the GitHub issue number are two unrelated counters. Never substitute one for the other, and never try to make the plan doc number match the issue number.
- Always zero-pad the plan doc prefix to at least 3 digits, even past `009`.
- If `docs/plans/` doesn't exist, create it and start at `001` — don't ask the user where to put it.
- Match the structure of an existing `docs/plans/*.md` file rather than inventing a new layout each time; if none exist yet, use the structure described in step 8.
- If `gh` isn't authenticated or the repo has no GitHub remote, stop and surface the error rather than working around it — this applies whether fetching an existing ticket or creating a new one.
- When sourced from an existing ticket, never create a duplicate GitHub issue — only reference the existing one.
- Order the to-do list in vertical, user-visible slices (e.g. listing → create → edit → delete), never in horizontal layers (all repository work, then all backend, then all frontend) — the whole point is enabling review and course-correction after each slice instead of one big-bang delivery at the end.
