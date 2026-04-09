---
name: land
description:
  Land a PR by monitoring conflicts, review feedback, and checks, then
  squash-merging when green; use when asked to land, merge, or shepherd a PR to
  completion.
---

# Land

## Goals

- Ensure the PR is conflict-free with main.
- Keep CI green and fix failures when they occur.
- Squash-merge the PR once checks pass and blocking feedback is addressed.
- Do not yield until the PR is merged unless genuinely blocked.
- Delete the remote branch only if repo policy permits it.

## Preconditions

- `gh` CLI is authenticated.
- You are on the PR branch with a clean working tree.

## Steps

1. Locate the PR for the current branch.
2. Confirm the local validation gate is green before any push.
3. If the working tree has uncommitted changes, commit with the `commit` skill and push with the `push` skill before proceeding.
4. Check mergeability and conflicts against main.
5. If conflicts exist, use the `pull` skill to fetch/merge `origin/main` and resolve conflicts, then use the `push` skill to publish the updated branch.
6. Ensure human review comments are acknowledged and any required fixes are handled before merging.
7. If the repo uses automated Copilot review comments or review requests, treat them as blocking until acknowledged or addressed.
8. Watch checks until complete.
9. If checks fail, inspect logs, fix the issue, commit with the `commit` skill, push with the `push` skill, and re-run checks.
10. When all checks are green and review feedback is addressed, squash-merge using the PR title/body unless repo policy requires a different merge method.
11. **Context guard:** Before implementing review feedback, confirm it does not conflict with the user’s stated intent or task context. If it conflicts, respond inline with a justification and ask before changing code.
12. **Pushback template:** When disagreeing, reply inline with acknowledge + rationale + alternative.
13. **Ambiguity gate:** When ambiguity blocks progress, use the clarification flow. Do not implement until ambiguity is resolved.
14. **Per-comment mode:** For each review comment, choose one of: accept, clarify, or push back. Reply before changing code.
15. **Reply before change:** Always respond with intended action before pushing code changes.

## Commands

```
# Ensure branch and PR context
branch=$(git branch --show-current)
pr_number=$(gh pr view --json number -q .number)
pr_title=$(gh pr view --json title -q .title)
pr_body=$(gh pr view --json body -q .body)

# Check mergeability and conflicts
mergeable=$(gh pr view --json mergeable -q .mergeable)

if [ "$mergeable" = "CONFLICTING" ]; then
  # Run the `pull` skill to handle fetch + merge + conflict resolution.
  # Then run the `push` skill to publish the updated branch.
fi

# Preferred: use the Async Watch Helper below. The manual loop is a fallback.
python3 ./land_watch.py

# After the watcher reports green checks and no blocking feedback:
gh pr merge --squash --subject "$pr_title" --body "$pr_body"
```

## Async Watch Helper

Preferred: use the asyncio watcher to monitor review comments, CI, and head updates in parallel:

```
python3 ./land_watch.py
```

Exit codes:

- 2: Review comments detected (address feedback)
- 3: CI checks failed or never appeared
- 4: PR head updated (pull / amend / retrigger as needed)
- 5: Merge conflicts detected

## Failure Handling

- If checks fail, pull details with `gh pr checks` and `gh run view --log`, then fix locally, commit with the `commit` skill, push with the `push` skill, and re-run the watch.
- Use judgment to identify flaky failures. If a failure is clearly a flake, document that rationale before proceeding.
- If mergeability is `UNKNOWN`, wait and re-check.
- Do not merge while review comments, requested changes, or automated Copilot findings are outstanding.
- Do not enable auto-merge unless repo policy explicitly requires it.
- If the remote PR branch advanced due to your own prior push or merge, avoid redundant merges; pull the latest state, rerun validation, and continue.

## Review Handling

- Human review comments are blocking and must be addressed before merge.
- If the repo uses automated Copilot review comments, treat them like reviewer feedback.
  - Expected optional trigger: `@copilot review`
  - Expected optional issue-comment header: `## Copilot Review — <persona>`
  - If the repo does not use this workflow, these steps are a no-op.
- Fetch review comments via `gh api` and reply inline where possible.
- Use review comment endpoints (not issue comments) to find inline feedback:
  - List PR review comments:
    ```
    gh api repos/{owner}/{repo}/pulls/<pr_number>/comments
    ```
  - PR issue comments:
    ```
    gh api repos/{owner}/{repo}/issues/<pr_number>/comments
    ```
  - Reply to a specific review comment:
    ```
    gh api -X POST /repos/{owner}/{repo}/pulls/<pr_number>/comments \
      -f body='[copilot] <response>' -F in_reply_to=<comment_id>
    ```
- All GitHub comments generated by this agent should be prefixed with `[copilot]`.
- For inline review feedback, reply with intended fixes before pushing code.
- For automated Copilot review issue comments, reply in the issue thread with `[copilot]` and state whether you will address or defer the feedback.
- If feedback requires changes:
  - acknowledge first
  - implement fixes
  - commit and push
  - reply with fix details and commit sha in the same thread or issue comment you used for acknowledgement
- Only request a new automated Copilot review when the repo supports it and there is at least one new commit since the last request.

Example root-level update after a batch of fixes:

```
[copilot] Changes since last review:
- <short bullets of deltas>
Commits: <sha>, <sha>
Tests: <commands run>
```

## Scope + PR Metadata

- The PR title and description should reflect the full scope of the change, not just the most recent fix.
- If review feedback expands scope, decide whether to include it now or defer it.
- Correctness issues raised in review comments should be addressed or explicitly validated as non-issues.
- Classify review comments as correctness, design, style, clarification, or scope.
- For correctness feedback, provide concrete validation (test, log, or reasoning) before closing it.
- Prefer a single consolidated root-level `[copilot]` update after a batch of fixes instead of many small updates.
