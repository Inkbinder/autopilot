---
name: push
description:
  Push current branch changes to origin and create or update the corresponding
  pull request; use when asked to push, publish updates, or create or update a
  pull request.
---

# Push

## Prerequisites

- `gh` CLI is installed and available in `PATH`.
- `gh auth status` succeeds for GitHub operations in this repo.

## Goals

- Push current branch changes to `origin` safely.
- Create a PR if none exists for the branch, otherwise update the existing PR.
- Keep branch history clean when the remote has moved.

## Related Skills

- `pull`: use this when push is rejected or sync is not clean (non-fast-forward, merge conflict risk, or stale branch).

## Steps

1. Identify the current branch and confirm remote state.
2. Run the repo's required local validation before pushing.
   - Source the commands from `WORKFLOW.md`, `AGENTS.md`, `README`, CI config, or the current task context.
3. Push the branch to `origin` with upstream tracking if needed, using the remote URL already configured.
4. If push is rejected:
   - If the failure is a non-fast-forward or sync problem, run the `pull` skill to merge `origin/main`, resolve conflicts, rerun validation, and push again.
   - Use `--force-with-lease` only when history was intentionally rewritten.
   - If the failure is due to auth, permissions, or workflow restrictions on the configured remote, stop and surface the exact error instead of rewriting remotes or switching protocols.
5. Ensure a PR exists for the branch:
   - If no PR exists, create one.
   - If a PR exists and is open, update it.
   - If the branch is tied to a closed/merged PR, create a new branch + PR.
   - Write a PR title that clearly describes the current change outcome.
   - Reconsider the title on every branch update; edit it if the scope changed.
6. Write or update the PR body explicitly:
   - If `.github/pull_request_template.md` exists, fill every section with concrete content for this change.
   - Replace all placeholder comments.
   - Keep bullets/checkboxes where the template expects them.
   - If no template exists, write a concise body with summary, rationale, validation, and any reviewer notes.
   - When a PR already exists, refresh the body so it reflects the total PR scope, not just the newest commits.
7. If the repo has a PR body checker or formatter, run it and fix all reported issues.
8. Reply with the PR URL from `gh pr view`.

## Commands

```sh
# Identify branch
branch=$(git branch --show-current)

# Run the repo's required validation before pushing.
# Examples: make test, npm test, go test ./..., cargo test

# Initial push: respect the current origin remote.
git push -u origin HEAD

# If that failed because the remote moved, use the pull skill. After pull-skill
# resolution and re-validation, retry the normal push:
git push -u origin HEAD

# Only if history was rewritten locally:
git push --force-with-lease origin HEAD

# Ensure a PR exists
pr_state=$(gh pr view --json state -q .state 2>/dev/null || true)
if [ "$pr_state" = "MERGED" ] || [ "$pr_state" = "CLOSED" ]; then
  echo "Current branch is tied to a closed PR; create a new branch + PR." >&2
  exit 1
fi

pr_title="<clear PR title written for this change>"
tmp_pr_body=$(mktemp)

# Draft / refresh the PR body in $tmp_pr_body before create/edit.
# If a template exists, fill it completely.

if [ -z "$pr_state" ]; then
  gh pr create --title "$pr_title" --body-file "$tmp_pr_body"
else
  gh pr edit --title "$pr_title" --body-file "$tmp_pr_body"
fi

# If the repo has a PR body checker, run it here.

# Show PR URL for the reply
gh pr view --json url -q .url

rm -f "$tmp_pr_body"
```

## Notes

- Do not use `--force`; only use `--force-with-lease` as the last resort.
- Distinguish sync problems from remote auth/permission problems.
- Keep the PR title/body aligned with the latest diff and review context.
