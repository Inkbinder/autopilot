---
tracker:
  kind: github
  api_key: $GITHUB_TOKEN
  repository: "__SMOKE_REPOSITORY__"
  active_states:
    - Open
  terminal_states:
    - Closed
  dispatch_labels:
    - autopilot:ready
polling:
  interval_ms: 1000
workspace:
  root: "__SMOKE_WORKSPACE_ROOT__"
hooks:
  after_create: |
    git clone "__SMOKE_SOURCE_REPO__" .
agent:
  max_concurrent_agents: 2
  max_turns: 1
  max_retry_backoff_ms: 5000
copilot:
  command: "__SMOKE_COPILOT_COMMAND__"
  transport: acp_stdio
  startup_timeout_ms: 5000
  prompt_timeout_ms: 60000
  stall_timeout_ms: 0
server:
  port: __SMOKE_PORT__
---

This is a disposable Autopilot smoke test for {{ issue.identifier }}.

Operate only inside the provided workspace. Do not ask for user input.
The smoke harness will close issues to end the run.