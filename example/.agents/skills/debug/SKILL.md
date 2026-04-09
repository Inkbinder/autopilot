---
name: debug
description:
  Investigate stuck runs and execution failures by tracing Autopilot and
  Copilot logs with issue/session identifiers; use when runs stall, retry
  repeatedly, or fail unexpectedly.
---

# Debug

## Goals

- Find why a run is stuck, retrying, or failing.
- Correlate a GitHub issue identity to a Copilot session quickly.
- Read the right logs and status endpoints in the right order to isolate root cause.

## Primary Signals

- Process logs: Autopilot emits structured JSON logs to stderr by default.
  - Read them from your process manager, container logs, shell redirect, or whatever file sink your deployment uses.
  - Example sinks: `journalctl`, Docker logs, or a redirected file such as `/var/log/autopilot.log`.
- Optional HTTP status API when `server.port` or `--port` is enabled:
  - `GET /api/v1/state`
  - `POST /api/v1/refresh`
  - `GET /api/v1/<issue_identifier>`
- Optional dashboard at `/` when the HTTP server is enabled.

## Correlation Keys

- `issue_identifier`: human issue key in Autopilot form (example: `octo/widgets#123`)
- `issue_id`: GitHub node id or tracker-internal id
- `session_id`: Copilot session id reported by the runtime

## Quick Triage

1. Confirm scheduler/worker symptoms for the issue.
2. Find recent log lines for the issue (`issue_identifier` first).
3. Extract `session_id` from matching lines.
4. Trace that `session_id` across startup, prompt completion/failure, retries, and stall handling.
5. Decide class of failure: dispatch failure, workspace/hook failure, Copilot startup failure, prompt failure/timeout, transport failure, or retry loop.

## Commands

```bash
# Set these for your environment
logfile=/path/to/autopilot.log
identifier='owner/repo#123'

# 1) Narrow by issue identifier
rg -n '"issue_identifier":"'"$identifier"'"' "$logfile"*

# 2) If needed, narrow by tracker id
rg -n '"issue_id":"<github-node-id>"' "$logfile"*

# 3) Pull session ids seen for that issue
rg -o '"session_id":"[^"]+"' "$logfile"* | sort -u

# 4) Trace one session end-to-end
session_id='<copilot-session-id>'
rg -n '"session_id":"'"$session_id"'"' "$logfile"*

# 5) Focus on failure/retry signals
rg -n 'candidate fetch failed|dispatch validation failed|prompt_failed|prompt_timeout|startup_timeout|transport_exit|transport_error|stalled|retry' "$logfile"*

# 6) Optional: inspect HTTP status API when enabled
curl -s http://127.0.0.1:PORT/api/v1/state | jq
curl -s "http://127.0.0.1:PORT/api/v1/owner%2Frepo%23123" | jq
curl -s -X POST http://127.0.0.1:PORT/api/v1/refresh | jq
```

## Investigation Flow

1. Locate the issue slice:
   - Search by `issue_identifier` first.
   - If needed, refine with `issue_id`.
2. Establish timeline:
   - Identify the first log line that clearly ties the issue to a Copilot session.
   - Follow the same `session_id` through startup, prompt runs, completion, or failure.
3. Classify the problem:
   - Dispatch/config failure: workflow reload or candidate fetch errors.
   - Workspace failure: hook errors, invalid cwd, or clone/setup failures.
   - Copilot startup failure: startup timeout, transport handshake failure, or missing CLI.
   - Prompt/runtime failure: prompt timeout, prompt cancellation, prompt failure.
   - Retry loop: repeated retries with the same issue and no durable progress.
4. Validate scope:
   - Determine whether the failure is isolated to one issue or repeating across multiple issues.
5. Capture evidence:
   - Save key log lines with timestamps, `issue_identifier`, `issue_id`, and `session_id`.
   - Record the probable root cause and exact failing stage.

## Reading Copilot Session Logs

Autopilot uses structured logs keyed by issue identity and session identity. Read them as a lifecycle:

1. Issue claimed or dispatched.
2. Copilot session attached or started.
3. Prompt runs and session events.
4. Terminal event:
   - success / normal completion
   - prompt/runtime failure
   - transport exit/error
   - stall detection / retry scheduling

For one specific session investigation, keep the trace narrow:

1. Capture one `session_id` for the issue.
2. Build a timestamped slice for only that session.
3. Mark the exact failing stage.
4. Pair findings with `issue_identifier` and `issue_id` from nearby lines to avoid mixing concurrent retries.

## Notes

- Prefer `rg` over `grep` for speed on large logs.
- If logs are only available through systemd, Docker, or another supervisor, query that source directly rather than assuming a fixed file path.
- Use the optional HTTP API when you need current in-memory runtime state rather than historical log evidence.
