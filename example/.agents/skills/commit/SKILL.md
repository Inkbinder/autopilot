---
name: commit
description:
  Create a well-formed git commit from current changes using Copilot session
  history for rationale and summary; use when asked to commit, prepare a
  commit message, or finalize staged work.
---

# Commit

## Goals

- Produce a commit that reflects the actual code changes and the session context.
- Follow common git conventions (type prefix, short subject, wrapped body).
- Include both summary and rationale in the body.

## Inputs

- Copilot session history for intent and rationale.
- `git status`, `git diff`, and `git diff --staged` for actual changes.
- Repo-specific commit conventions if documented.

## Steps

1. Read session history to identify scope, intent, and rationale.
2. Inspect the working tree and staged changes (`git status`, `git diff`, `git diff --staged`).
3. Stage intended changes, including new files (`git add -A`) after confirming scope.
4. Sanity-check newly added files; if anything looks random or likely ignored (build artifacts, logs, temp files), remove it from the index before committing.
5. If staging is incomplete or includes unrelated files, fix the index or ask for confirmation.
6. Choose a conventional type and optional scope that match the change (for example `feat(scope): ...`, `fix(scope): ...`, `refactor(scope): ...`).
7. Write a subject line in imperative mood, 72 characters or fewer, with no trailing period.
8. Write a body that includes:
   - summary of key changes
   - rationale and trade-offs
   - tests or validation run, or an explicit note if not run
9. If the repo or user requires a co-author trailer for AI-assisted work, use the repo-approved GitHub Copilot identity. Otherwise omit the trailer.
10. Wrap body lines at 72 characters.
11. Create the commit message with a here-doc or temp file and use `git commit -F <file>` so newlines are literal.
12. Commit only when the message matches the staged changes.

## Output

- A single commit created with `git commit` whose message reflects the session.

## Template

Type and scope are examples only; adjust to fit the repo and changes.

```
<type>(<scope>): <short summary>

Summary:
- <what changed>
- <what changed>

Rationale:
- <why>
- <why>

Tests:
- <command or "not run (reason)">

Optional trailer:
Co-authored-by: <repo-approved GitHub Copilot identity>
```
