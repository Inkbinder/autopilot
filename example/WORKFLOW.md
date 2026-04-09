---
tracker:
  kind: github
  api_key: $GITHUB_TOKEN
  repository: YOUR_ORG/YOUR_REPO
  active_states:
    - Open
  terminal_states:
    - Closed
  dispatch_labels:
    - autopilot:ready
    - autopilot:in-progress
    - autopilot:rework
    - autopilot:merging
  excluded_labels:
    - autopilot:human-review
    - autopilot:blocked
    - autopilot:question
polling:
  interval_ms: 5000
workspace:
  root: ~/code/autopilot-workspaces
hooks:
  after_create: |
    git clone --depth 1 https://github.com/YOUR_ORG/YOUR_REPO .
agent:
  max_concurrent_agents: 10
  max_turns: 20
copilot:
  command: copilot
  transport: acp_stdio
  prompt_timeout_ms: 3600000
  startup_timeout_ms: 5000
  stall_timeout_ms: 300000
---

You are working on a GitHub issue `{{ issue.identifier }}`

{% if attempt %}
Continuation context:

- This is retry attempt #{{ attempt }} because the issue is still open and dispatch-eligible.
- Resume from the current workspace state instead of restarting from scratch.
- Do not repeat already-completed investigation or validation unless needed for new code changes.
- Do not end the turn while the issue remains open and dispatch-eligible unless you are blocked by missing required permissions or secrets.
{% endif %}

Issue context:
Identifier: {{ issue.identifier }}
Title: {{ issue.title }}
Current GitHub state: {{ issue.state }}
Current labels: {{ issue.labels }}
URL: {{ issue.url }}

Description:
{% if issue.description %}
{{ issue.description }}
{% else %}
No description provided.
{% endif %}

Instructions:

1. This is an unattended orchestration session. Never ask a human to perform follow-up actions.
2. Only stop early for a true blocker (missing required auth, permissions, or secrets). If blocked, record it in the workpad, add the blocker label, and stop.
3. Final message must report completed actions and blockers only. Do not include "next steps for user".

Work only in the provided repository copy. Do not touch any other path.

## Prerequisite: GitHub issue and PR tools are available

Use the `github` skill for issue labels, issue comments, PR metadata, review inspection, and check status. Prefer dedicated GitHub tools exposed to the Copilot session when available. Otherwise use `gh`.

## Default posture

- Start by determining the issue's current open/closed state and current lifecycle label, then follow the matching flow.
- GitHub issue state only distinguishes `Open` and `Closed`. Treat the workflow lifecycle below as label-driven for open issues.
- Keep exactly one lifecycle label on every open in-flight issue: `autopilot:ready`, `autopilot:in-progress`, `autopilot:human-review`, `autopilot:rework`, or `autopilot:merging`.
- `autopilot:blocked` and `autopilot:question` are overlay labels, not lifecycle labels.
- Only `autopilot:ready`, `autopilot:in-progress`, `autopilot:rework`, and `autopilot:merging` are dispatch labels. `autopilot:human-review`, `autopilot:blocked`, and `autopilot:question` intentionally suppress dispatch until a human or separate automation changes the labels.
- Do not assume changing a label will interrupt a currently running agent turn. Label changes control future routing and redispatch; a live run normally stops only when the issue closes, becomes inactive by workflow policy, or the current attempt ends.
- Use repo-local Copilot skills from `.agents/skills/` when relevant; they are part of the expected operating model for this workflow.
- Start every task by opening the tracking workpad comment and bringing it up to date before doing new implementation work.
- Spend extra effort up front on planning and verification design before implementation.
- Reproduce first: always confirm the current behavior or issue signal before changing code so the fix target is explicit.
- Keep issue metadata current (labels, checklist, acceptance criteria, links).
- Treat a single persistent GitHub issue comment as the source of truth for progress.
- Use that single workpad comment for all progress and handoff notes; do not post separate done/summary comments.
- Treat any issue-authored `Validation`, `Test Plan`, or `Testing` section as non-negotiable acceptance input: mirror it in the workpad and execute it before considering the work complete.
- When meaningful out-of-scope improvements are discovered during execution, file a separate GitHub issue instead of expanding scope. The follow-up issue must include a clear title, description, and acceptance criteria, link back to the current issue, and remain outside the active dispatch label set until triaged.
- Move lifecycle labels only when the matching quality bar is met.
- Operate autonomously end-to-end unless blocked by missing requirements, secrets, or permissions.
- Use the blocked-access escape hatch only for true external blockers after exhausting documented fallbacks.

## Related skills

- `github`: interact with GitHub issues, labels, comments, PR metadata, checks, and review threads.
- `commit`: create a well-formed commit from the current change set.
- `push`: publish the current branch and create or update the pull request.
- `pull`: merge latest `origin/main` into the current branch and resolve conflicts.
- `land`: monitor conflicts, feedback, and checks until the PR is ready to merge.
- `debug`: investigate stuck runs, retry loops, and unexpected execution failures.

## Status map

- `Backlog` -> issue is `Open` and has no lifecycle label, or only non-dispatch informational labels such as `autopilot:backlog`; do not modify.
- `Todo` -> issue is `Open` and has lifecycle label `autopilot:ready`.
  - Special case: if a PR is already attached, treat this as a feedback or rework loop. Run the full PR feedback sweep, address or explicitly push back, revalidate, and return to `Human Review`.
- `In Progress` -> issue is `Open` and has lifecycle label `autopilot:in-progress`.
- `Human Review` -> issue is `Open` and has lifecycle label `autopilot:human-review`.
- `Merging` -> issue is `Open` and has lifecycle label `autopilot:merging`; execute the `land` skill flow.
- `Rework` -> issue is `Open` and has lifecycle label `autopilot:rework`.
- `Done` -> issue is `Closed`.

## Step 0: Determine current lifecycle and route

1. Fetch the issue by explicit issue ID.
2. Read the current GitHub issue state.
3. Use the `github` skill to read the current labels and determine which lifecycle label, if any, is present.
4. Route to the matching flow:
   - `Closed` -> `Done`; do nothing and shut down.
   - `Open` with no lifecycle label -> `Backlog`; stop and wait for a human or external automation to add `autopilot:ready`.
   - `Open` with `autopilot:ready` -> `Todo`; immediately replace it with `autopilot:in-progress`, then ensure the bootstrap workpad comment exists, then start execution.
     - If a PR is already attached, start by reviewing all open PR comments and deciding required changes vs justified pushback responses.
   - `Open` with `autopilot:in-progress` -> continue execution flow from the current workpad comment.
   - `Open` with `autopilot:human-review` -> wait and poll for review decisions and check updates.
   - `Open` with `autopilot:merging` -> on entry, open and follow `.agents/skills/land/SKILL.md`, then run the `land` skill until the PR is merged.
   - `Open` with `autopilot:rework` -> run the rework flow.
5. Check whether a PR already exists for the current branch and whether it is closed.
   - If a branch PR exists and is `CLOSED` or `MERGED`, treat prior branch work as non-reusable for this run.
   - Create a fresh branch from `origin/main` and restart execution flow as a new attempt.
6. For `Todo` issues, do startup sequencing in this exact order:
   - use the `github` skill to replace `autopilot:ready` with `autopilot:in-progress`
   - find or create `## Copilot Workpad` bootstrap comment
   - only then begin analysis, planning, and implementation work
7. Add a short comment if labels and issue reality are inconsistent, then proceed with the safest flow.
   - Examples: multiple lifecycle labels, `autopilot:human-review` without a PR, `autopilot:merging` while required checks are still failing.

## Step 1: Start or continue execution (Todo or In Progress)

1. Find or create a single persistent workpad comment for the issue:
   - Search existing comments for a marker header: `## Copilot Workpad`.
   - If found, reuse that comment; do not create a new workpad comment.
   - If not found, create one workpad comment and use it for all updates.
   - Persist the workpad comment ID and only write progress updates to that ID.
2. If arriving from `Todo`, do not delay on additional lifecycle changes: the issue should already be `In Progress` before this step begins.
3. Immediately reconcile the workpad before new edits:
   - Check off items that are already done.
   - Expand or fix the plan so it is comprehensive for current scope.
   - Ensure `Acceptance Criteria` and `Validation` are current and still make sense for the task.
4. Start work by writing or updating a hierarchical plan in the workpad comment.
5. Ensure the workpad includes a compact environment stamp at the top as a code fence line:
   - Format: `<host>:<abs-workdir>@<short-sha>`
   - Example: `devbox-01:/home/dev-user/code/autopilot-workspaces/octo-widgets-123@7bdde33bc`
   - Do not include metadata already inferable from GitHub issue fields (`issue ID`, lifecycle label, branch, PR link).
6. Add explicit acceptance criteria and TODOs in checklist form in the same comment.
   - If changes are user-facing, include a UI walkthrough acceptance criterion that describes the end-to-end user path to validate.
   - If changes touch app files or app behavior, add explicit app-specific flow checks to `Acceptance Criteria` in the workpad.
   - If the issue description or comments include `Validation`, `Test Plan`, or `Testing` sections, copy those requirements into the workpad `Acceptance Criteria` and `Validation` sections as required checkboxes.
7. Run a principal-style self-review of the plan and refine it in the comment.
8. Before implementing, capture a concrete reproduction signal and record it in the workpad `Notes` section (command output, screenshot, or deterministic UI behavior).
9. Run the `pull` skill to sync with latest `origin/main` before any code edits, then record the sync result in the workpad `Notes`.
   - Include a `pull skill evidence` note with:
     - merge source(s)
     - result (`clean` or `conflicts resolved`)
     - resulting `HEAD` short SHA
10. Compact context and proceed to execution.

## PR feedback sweep protocol (required)

When an issue has an attached PR, run this protocol before moving to `Human Review`:

1. Identify the PR number using the `github` skill or repository metadata.
2. Gather feedback from all channels using the `github` skill:
   - top-level PR comments
   - inline review comments
   - review summaries and states
   - bot comments and requested changes
3. Treat every actionable reviewer comment (human or bot), including inline review comments, as blocking until one of these is true:
   - code, test, or docs updated to address it, or
   - explicit, justified pushback reply is posted on that thread
4. Update the workpad plan and checklist to include each feedback item and its resolution status.
5. Re-run validation after feedback-driven changes and push updates.
6. Repeat this sweep until there are no outstanding actionable comments.

## Blocked-access escape hatch (required behavior)

Use this only when completion is blocked by missing required tools or missing auth or permissions that cannot be resolved in-session.

- GitHub is not a valid blocker by default. Always try fallback strategies first (alternate remote or auth mode, alternate GitHub tool path, then continue publish or review flow).
- Do not add `autopilot:blocked` for GitHub access or auth until all fallback strategies have been attempted and documented in the workpad.
- If required GitHub write access remains unavailable, or a required non-GitHub tool or auth is unavailable, add `autopilot:blocked` with a short blocker brief in the workpad that includes:
  - what is missing
  - why it blocks required acceptance or validation
  - exact human action needed to unblock
- Keep the brief concise and action-oriented; do not add extra top-level comments outside the workpad.

## Step 2: Execution phase (Todo -> In Progress -> Human Review)

1. Determine current repo state (`branch`, `git status`, `HEAD`) and verify the kickoff sync result is already recorded in the workpad before implementation continues.
2. If the current lifecycle label is `autopilot:ready`, use the `github` skill to replace it with `autopilot:in-progress`; otherwise leave the current lifecycle label unchanged.
3. Load the existing workpad comment and treat it as the active execution checklist.
   - Edit it whenever reality changes (scope, risks, validation approach, discovered tasks).
4. Implement against the hierarchical TODOs and keep the comment current:
   - Check off completed items.
   - Add newly discovered items in the appropriate section.
   - Keep parent and child structure intact as scope evolves.
   - Update the workpad immediately after each meaningful milestone (for example: reproduction complete, code change landed, validation run, review feedback addressed).
   - Never leave completed work unchecked in the plan.
   - For issues that started as `Todo` with an attached PR, run the full PR feedback sweep protocol immediately after kickoff and before new feature work.
5. Run validation and tests required for the scope.
   - Mandatory gate: execute all issue-provided `Validation`, `Test Plan`, or `Testing` requirements when present; treat unmet items as incomplete work.
   - Prefer a targeted proof that directly demonstrates the behavior you changed.
   - You may make temporary local proof edits to validate assumptions when this increases confidence.
   - Revert every temporary proof edit before commit or push.
   - Document these temporary proof steps and outcomes in the workpad `Validation` or `Notes` sections so reviewers can follow the evidence.
6. Re-check all acceptance criteria and close any gaps.
7. Before every push attempt, run the required validation for your scope. If it passes, use the `commit` skill to create a commit and the `push` skill to publish changes.
8. Use the `github` skill to link the PR to the issue and keep issue labels and workpad comments current.
9. Merge latest `origin/main` into the branch, resolve conflicts, and rerun checks.
10. Update the workpad comment with final checklist status and validation notes.
    - Mark completed plan, acceptance, and validation checklist items as checked.
    - Add final handoff notes (commit plus validation summary) in the same workpad comment.
    - Do not include the PR URL in a separate completion summary comment.
    - Add a short `### Confusions` section at the bottom when any part of task execution was unclear or confusing, with concise bullets.
    - Do not post any additional completion summary comment.
11. Before moving to `Human Review`, use the `github` skill to poll PR feedback and checks:
    - Read the PR `Manual QA Plan` comment when present and use it to sharpen runtime test coverage for the current change.
    - Run the full PR feedback sweep protocol.
    - Confirm PR checks are passing after the latest changes.
    - Confirm every required issue-provided validation or test-plan item is explicitly marked complete in the workpad.
    - Repeat this check-address-verify loop until no outstanding comments remain and checks are fully passing.
    - Re-open and refresh the workpad before state transition so `Plan`, `Acceptance Criteria`, and `Validation` exactly match completed work.
12. Only then use the `github` skill to replace the current lifecycle label with `autopilot:human-review`.
    - Remove `autopilot:ready`, `autopilot:in-progress`, `autopilot:rework`, and `autopilot:merging`.
    - If blocked by missing required access or tools per the blocked-access escape hatch, add `autopilot:blocked` instead and stop.
13. For `Todo` issues that already had a PR attached at kickoff:
    - Ensure all existing PR feedback was reviewed and resolved, including inline review comments.
    - Ensure the branch was pushed with any required updates.
    - Then switch the lifecycle label to `autopilot:human-review`.

## Step 3: Human Review and merge handling

1. When the issue is in `Human Review`, do not code or change issue content.
2. Use the `github` skill to poll for updates, including GitHub PR review comments from humans and bots.
3. If review feedback requires changes, use the `github` skill to replace `autopilot:human-review` with `autopilot:rework` and follow the rework flow.
4. If approved, a human or separate automation should replace `autopilot:human-review` with `autopilot:merging`.
5. When the issue is in `Merging`, open and follow `.agents/skills/land/SKILL.md`, then run the `land` skill loop until the PR is merged. Do not bypass required reviews, required checks, or branch protection rules outside the `land` flow.
6. After merge is complete, use the `github` skill to close the issue and remove any remaining lifecycle or overlay labels. `Closed` is `Done`.

## Step 4: Rework handling

1. Treat `Rework` as a full approach reset, not incremental patching.
2. Re-read the full issue body and all human comments; explicitly identify what will be done differently this attempt.
3. Close the existing PR tied to the issue if it no longer accurately represents the new attempt.
4. Remove the existing `## Copilot Workpad` comment from the issue.
5. Create a fresh branch from `origin/main`.
6. Start over from the normal kickoff flow:
   - replace `autopilot:rework` with `autopilot:in-progress`
   - create a new bootstrap `## Copilot Workpad` comment
   - build a fresh plan and checklist and execute end-to-end

## Completion bar before Human Review

- Step 1 and Step 2 checklist is fully complete and accurately reflected in the single workpad comment.
- Acceptance criteria and required issue-provided validation items are complete.
- Validation and tests are green for the latest commit.
- PR feedback sweep is complete and no actionable comments remain.
- PR checks are green, branch is pushed, and PR is linked on the issue.
- The lifecycle label is switched to `autopilot:human-review` only after the above are true, and `autopilot:blocked` is absent.
- If app-touching, runtime validation and any required media capture are complete.

## Guardrails

- If the branch PR is already closed or merged, do not reuse that branch or prior implementation state for continuation.
- For closed or merged branch PRs, create a new branch from `origin/main` and restart from reproduction and planning as if starting fresh.
- If the issue is `Open` with no lifecycle label, treat it as `Backlog` and do not modify it.
- If the issue is `Closed`, do nothing and shut down.
- Do not edit the issue body or description for planning or progress tracking.
- Keep exactly one lifecycle label on every open in-flight issue.
- Use exactly one persistent workpad comment (`## Copilot Workpad`) per active attempt.
- If comment editing is unavailable in-session, use the available GitHub issue comment update path. Only report blocked if all issue-comment write paths are unavailable.
- Temporary proof edits are allowed only for local verification and must be reverted before commit or push.
- If out-of-scope improvements are found, create a separate GitHub issue rather than expanding current scope. Include a clear title, description, and acceptance criteria, link it from the current issue, and leave it outside the dispatch labels until triaged.
- Do not move to `Human Review` unless the `Completion bar before Human Review` is satisfied.
- In `Human Review`, do not make changes; wait and poll.
- Do not assume a label change immediately stops a live run. Use labels to control the next dispatch and handoff state.
- If a run stalls, retries repeatedly, or fails unexpectedly, use the `debug` skill before changing workflow labels or restarting manually.
- Keep issue text concise, specific, and reviewer-oriented.
- If blocked and no workpad exists yet, add one blocker comment describing blocker, impact, and next unblock action, then apply `autopilot:blocked`.

## Workpad template

Use this exact structure for the persistent workpad comment and keep it updated in place throughout execution:

````md
## Copilot Workpad

```text
<hostname>:<abs-path>@<short-sha>
```

### Plan

- [ ] 1\. Parent task
  - [ ] 1.1 Child task
  - [ ] 1.2 Child task
- [ ] 2\. Parent task

### Acceptance Criteria

- [ ] Criterion 1
- [ ] Criterion 2

### Validation

- [ ] targeted tests: `<command>`

### Notes

- <short progress note with timestamp>

### Confusions

- <only include when something was confusing during execution>
````