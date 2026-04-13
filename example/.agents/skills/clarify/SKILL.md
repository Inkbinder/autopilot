---
name: clarify
description: |
  Ask clarification questions on a GitHub issue when requirements, scope,
  acceptance criteria, product intent, or review feedback are ambiguous. Use
  when multiple reasonable implementations exist and the agent must update the
  workpad, add `autopilot:question`, and stop instead of guessing.
---

# Clarify

Use this skill when you cannot continue correctly without a human decision.

## Goals

- Avoid implementation-defined assumptions.
- Record the ambiguity in the persistent workpad comment.
- Ask concise numbered questions on the GitHub issue.
- Add `autopilot:question` without changing the lifecycle label.
- Stop the current attempt after the issue is safely parked.

## Use when

- The issue, comments, or review feedback allow multiple reasonable implementations.
- Required behavior, UX, scope boundaries, acceptance criteria, or data handling are unclear.
- A review thread asks for clarification that cannot be answered from repository evidence or prior issue discussion.
- A human decision is required to continue correctly.

Do not use this skill for missing auth, permissions, or unavailable tools. Use the blocked flow for those cases.

## Required outputs

- Update the existing `## Copilot Workpad` comment in place with:
  - a short ambiguity summary
  - why it blocks a correct implementation or review response
  - the exact numbered questions asked on the issue
- Post one concise GitHub issue comment that:
  - starts with `[copilot]`
  - summarizes the ambiguity in one or two sentences
  - asks only the minimum numbered questions needed to unblock a correct decision
  - states that `autopilot:question` was added to pause redispatch
- Add the `autopilot:question` label.
- Leave the current lifecycle label unchanged.
- End the current attempt without writing speculative code or speculative review replies.

## Procedure

1. Confirm the ambiguity is real.
   - Exhaust direct evidence first: issue body, comments, workpad, repository docs, code, tests, and PR feedback.
   - If repository conventions or explicit acceptance criteria already imply the answer, continue without asking, but note the rationale in the workpad.
2. Update the `## Copilot Workpad` comment in place.
   - Add a short note in `### Notes` describing the ambiguity and why it blocks a correct implementation.
   - Include the exact numbered questions you will ask.
3. Post one concise issue comment.
   - Prefer explicit options when possible.
   - Ask only the minimum set of questions needed to continue correctly.
   - If `autopilot:question` is already present and the same questions were already asked, avoid posting a duplicate comment.
4. Add the overlay label.
   - Use `gh issue edit ... --add-label autopilot:question` or the equivalent GitHub tool.
   - Do not remove or change the current lifecycle label.
5. Stop.
   - End the current attempt after verifying the workpad and issue comment reflect the same open questions.
   - Do not continue implementation until a human removes `autopilot:question` or otherwise resolves the ambiguity.

## Comment template

```md
[copilot] I need clarification before I can continue without making assumptions.

<one-sentence ambiguity summary>

Questions:
1. ...
2. ...

I added `autopilot:question` to pause redispatch until this is clarified.
```

## Commands

```bash
identifier="owner/repo#123"
repo="${identifier%#*}"
number="${identifier##*#}"

gh issue comment "$number" --repo "$repo" --body-file /tmp/clarify.md
gh issue edit "$number" --repo "$repo" --add-label autopilot:question
```

## Rules

- Keep clarification comments concise and decision-oriented.
- Prefer a small numbered list over a long narrative.
- Ask about observable behavior or decision criteria, not implementation details, unless the implementation choice is the requirement.
- Do not combine `autopilot:question` with `autopilot:blocked` unless missing access or tools are also preventing progress.
- If the ambiguity is raised by PR review feedback, reference that context in the issue comment and workpad before stopping.