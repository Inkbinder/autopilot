---
name: github
description: |
  Use GitHub issue, pull request, label, comment, review, and check operations
  during Autopilot sessions. Use when updating lifecycle labels, editing the
  Copilot workpad comment, linking PRs, reading review feedback, or checking
  merge readiness.
---

# GitHub Operations

Use this skill for GitHub issue and pull request work during Autopilot/Copilot sessions.

## Primary tools

- Prefer dedicated GitHub tools exposed to the Copilot session when available.
- Otherwise use `gh` CLI for GitHub reads and writes.
- Use `gh api` for comment editing and endpoints that are not covered by higher-level `gh` commands.
- Keep reads and writes narrowly scoped; request only the fields you need.

## Conventions

- Autopilot lifecycle labels live on open issues:
  - `autopilot:ready`
  - `autopilot:in-progress`
  - `autopilot:human-review`
  - `autopilot:rework`
  - `autopilot:merging`
- Overlay labels are not lifecycle labels:
  - `autopilot:blocked`
  - `autopilot:question`
- The workpad comment marker is `## Copilot Workpad`.
- Prefer normal GitHub issue/PR linking over ad-hoc “see PR” comments.

## Quick parsing helpers

GitHub issue identifiers in Autopilot prompts usually look like `owner/repo#123`.

```bash
identifier="owner/repo#123"
repo="${identifier%#*}"
number="${identifier##*#}"
```

## Common workflows

### View an issue by identifier

```bash
identifier="owner/repo#123"
repo="${identifier%#*}"
number="${identifier##*#}"

gh issue view "$number" --repo "$repo" \
  --json id,number,title,body,state,url,labels,comments,assignees
```

### Read or change lifecycle labels

Read labels:

```bash
gh issue view "$number" --repo "$repo" --json labels
```

Move `ready` -> `in-progress`:

```bash
gh issue edit "$number" --repo "$repo" \
  --remove-label autopilot:ready \
  --add-label autopilot:in-progress
```

Move `in-progress` -> `human-review`:

```bash
gh issue edit "$number" --repo "$repo" \
  --remove-label autopilot:in-progress \
  --add-label autopilot:human-review
```

If multiple lifecycle labels are present, remove the extras immediately and leave exactly one.

### Find the workpad comment

List issue comments and search for the `## Copilot Workpad` marker:

```bash
gh api "repos/$repo/issues/$number/comments" \
  --jq '.[] | {id, created_at, updated_at, body}'
```

Create a workpad comment:

```bash
gh api "repos/$repo/issues/$number/comments" \
  -f body="$(cat /tmp/workpad.md)"
```

Edit an existing workpad comment:

```bash
comment_id="1234567890"
gh api -X PATCH "repos/$repo/issues/comments/$comment_id" \
  -f body="$(cat /tmp/workpad.md)"
```

### Add a short issue comment

```bash
gh issue comment "$number" --repo "$repo" --body "Short progress update"
```

### Close or reopen an issue

```bash
gh issue close "$number" --repo "$repo"
gh issue reopen "$number" --repo "$repo"
```

### Locate the PR for the current branch

```bash
gh pr view --json number,url,state,title,body,headRefName,baseRefName
```

If you need to locate a PR by branch explicitly:

```bash
branch=$(git branch --show-current)
gh pr list --head "$branch" --json number,url,state,title
```

### Read PR feedback from all channels

Top-level PR discussion:

```bash
gh pr view --comments
```

Review summaries and states:

```bash
gh pr view --json reviews
```

Inline review comments:

```bash
pr_number=$(gh pr view --json number -q .number)
gh api "repos/{owner}/{repo}/pulls/$pr_number/comments"
```

### Reply to an inline review comment

```bash
pr_number=$(gh pr view --json number -q .number)
comment_id="2710521800"
gh api -X POST \
  "repos/{owner}/{repo}/pulls/$pr_number/comments" \
  -f body='[copilot] Planned fix: <what you will change>' \
  -F in_reply_to="$comment_id"
```

### Check CI and inspect failures

Summary:

```bash
gh pr checks
```

Watch until complete:

```bash
gh pr checks --watch
```

Inspect recent runs:

```bash
branch=$(git branch --show-current)
gh run list --branch "$branch"
gh run view <run-id> --log
```

### Link the PR to the issue

Preferred: update the PR body so it references the issue directly, for example with `Refs owner/repo#123` or `Closes owner/repo#123` when appropriate.

```bash
pr_number=$(gh pr view --json number -q .number)
gh pr edit "$pr_number" --body-file /tmp/pr_body.md
```

Fallback: add a cross-link comment on the issue or PR if direct body updates are temporarily unavailable.

## Usage rules

- Use the narrowest operation that fits the task.
- Prefer `gh issue edit` for labels and state changes, and `gh api` only when higher-level commands are insufficient.
- Keep exactly one lifecycle label on every open in-flight issue.
- Update the `## Copilot Workpad` comment in place instead of creating extra progress comments.
- Treat human review comments, explicit bot findings, and failing checks as blocking for handoff and merge.
- Prefer direct GitHub issue/PR linkage over loose URL comments.
