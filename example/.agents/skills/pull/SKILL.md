---
name: pull
description:
  Pull latest origin/main into the current local branch and resolve merge
  conflicts (aka update-branch). Use when Copilot needs to sync a feature
  branch with origin, perform a merge-based update (not rebase), and apply
  conflict resolution best practices.
---

# Pull

## Workflow

1. Verify `git status` is clean or commit/stash changes before merging.
2. Ensure rerere is enabled locally:
   - `git config rerere.enabled true`
   - `git config rerere.autoupdate true`
3. Confirm remotes and branches:
   - Ensure the `origin` remote exists.
   - Ensure the current branch is the one to receive the merge.
4. Fetch latest refs:
   - `git fetch origin`
5. Sync the remote feature branch first:
   - `git pull --ff-only origin $(git branch --show-current)`
   - This pulls branch updates made remotely before merging `origin/main`.
6. Merge in order:
   - Prefer `git -c merge.conflictstyle=zdiff3 merge origin/main` for clearer conflict context.
7. If conflicts appear, resolve them (see conflict guidance below), then:
   - `git add <files>`
   - `git commit` or `git merge --continue`
8. Verify with the repo’s documented validation commands.
9. Summarize the merge:
   - Call out the most challenging conflicts/files and how they were resolved.
   - Note any assumptions or follow-ups.

## Conflict Resolution Guidance

- Inspect context before editing:
  - Use `git status` to list conflicted files.
  - Use `git diff` or `git diff --merge` to see conflict hunks.
  - Use `git diff :1:path/to/file :2:path/to/file` and `git diff :1:path/to/file :3:path/to/file` to compare base vs ours/theirs.
  - With `merge.conflictstyle=zdiff3`, focus on the differing core rather than matching context.
  - Summarize the intent of both sides before editing.
- Prefer minimal, intention-preserving edits.
- Resolve one file at a time and rerun tests after each logical batch.
- Use `ours/theirs` only when one side clearly wins in full.
- For generated files, resolve source conflicts first, then regenerate artifacts.
- After resolving, ensure no conflict markers remain:
  - `git diff --check`

## When To Ask The User

Do not ask for input unless there is no safe, reversible alternative.

Ask only when:

- The correct resolution depends on product intent not inferable from code, tests, or documentation.
- The conflict crosses a user-visible API or migration where choosing incorrectly could break external consumers.
- The conflict requires selecting between two mutually exclusive designs with no clear local signal.
- The merge introduces data loss, schema changes, or irreversible side effects without an obvious safe default.
- The branch is not the intended target, or the remote/branch names do not exist and cannot be determined locally.

Otherwise, proceed with the merge, explain the decision briefly in notes, and leave a clear, reviewable history.
