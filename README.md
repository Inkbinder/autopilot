# autopilot
Autopilot turns project work into isolated, autonomous implementation runs, allowing teams to manage work instead of watching coding agents.

It is a long-running Go service that polls GitHub issues, creates per-issue workspaces, and runs GitHub Copilot sessions from a repo-owned `WORKFLOW.md`. The service loads the workflow on startup, watches it for changes while running, persists run history and audit events beside the workflow file, and can expose a local status dashboard and JSON API.

**Build**

Autopilot targets Go 1.26.

If you want the binary without cloning the repository, install it with:

```bash
go install github.com/Inkbinder/autopilot/cmd/autopilot@latest
```

To pin an exact release, replace `latest` with a tag such as `v0.1.0`. The binary is installed to `$(go env GOBIN)` or, if `GOBIN` is unset, `$(go env GOPATH)/bin`, so make sure that directory is on your `PATH`.

For development and test work in this repository, start with:

- Go 1.26
- `git`
- `bash`
- `curl`
- `sed`

If you only plan to build the binary or run `go test ./...`, that is enough. If you also want to run the disposable smoke harness in [smoke-test/run.sh](smoke-test/run.sh), you also need:

- `gh` authenticated against GitHub
- permission to create issues and create/delete repositories under the target owner
- `GITHUB_TOKEN` exported, or `gh auth token` available for the script to reuse

For the smoke harness, the `gh` authentication must include the `delete_repo` scope so the disposable repository can be removed during cleanup. If you authenticated without it, run:

```bash
gh auth refresh -h github.com -s delete_repo
```

The smoke harness uses [smoke-test/fake-copilot.sh](smoke-test/fake-copilot.sh), so a real Copilot CLI is not required for that path.

For a live walkthrough against a disposable GitHub repository instead of the fake Copilot smoke path, use [demo/run.sh](demo/run.sh) and follow [demo/README.md](demo/README.md) for its additional setup requirements.

```bash
go build ./cmd/autopilot
```

To place the binary in a predictable location:

```bash
mkdir -p bin
go build -o ./bin/autopilot ./cmd/autopilot
```

**Run**

For standard use, especially if you are starting from [example/WORKFLOW.md](example/WORKFLOW.md) and the bundled skills under [example/.agents/skills](example/.agents/skills), make sure the Autopilot host has:

- a valid `WORKFLOW.md` file
- a writable host workspace root, either set in `workspace.root` or available through the default `/tmp/autopilot_workspaces`
- if `workspace.provider` is `docker`, access to a local Docker daemon plus a pullable or preloaded image referenced by `workspace.image`
- a GitHub repository configured in `tracker.repository` using `owner/repo` format
- network access to the GitHub API for the configured repository
- `git`, because the example workflow clones the target repository and the bundled pull, commit, and push skills assume normal git operations inside the workspace
- `gh` authenticated against GitHub, because the bundled GitHub, push, and land skills use it for issue, PR, label, check, and merge operations
- `python3`, because the bundled land skill runs `land_watch.py`
- the GitHub Copilot CLI installed and authenticated as `copilot`, or an alternate command configured through `copilot.command`
- a GitHub token supplied through `GITHUB_TOKEN` or `tracker.api_key`

A good starting point is [example/WORKFLOW.md](example/WORKFLOW.md). Copy it to the repository root as `WORKFLOW.md`, replace `YOUR_ORG/YOUR_REPO`, and update the workspace settings and `hooks.after_create` clone command for your environment. If you switch that workflow to `workspace.provider: docker`, also set `workspace.image`; hooks and Copilot sessions then run inside that container, so the image needs the same tools and auth setup your workflow expects. If you installed Autopilot with `go install`, clone or download [example/WORKFLOW.md](example/WORKFLOW.md) and the bundled skills under [example/.agents/skills](example/.agents/skills) separately before first run. The example workflow uses the default `acp_stdio` Copilot transport, dispatches issues carrying the default lifecycle labels `autopilot:ready`, `autopilot:in-progress`, `autopilot:rework`, and `autopilot:merging`, excludes issues marked `autopilot:human-review`, `autopilot:blocked`, or `autopilot:question`, and follows GitHub issue dependencies automatically so issues with open blockers are not started until those blockers reach terminal states.

If you want a guided end-to-end demo before wiring Autopilot to your own repository, [demo/run.sh](demo/run.sh) provisions a disposable GitHub repository, renders a workflow, seeds a dependency-linked issue queue, and prints a helper script that starts Autopilot against that repository. The full walkthrough is in [demo/README.md](demo/README.md).

Run an installed binary with the default root workflow file:

```bash
autopilot
```

Run a built binary:

```bash
./bin/autopilot
```

From a repository checkout, run without installing:

```bash
go run ./cmd/autopilot
```

Run an installed binary with an explicit workflow path:

```bash
autopilot ./path/to/WORKFLOW.md
```

From a repository checkout, run with an explicit workflow path:

```bash
go run ./cmd/autopilot ./path/to/WORKFLOW.md
```

Override the local status server port from the command line with an installed binary:

```bash
autopilot -port 8080 ./WORKFLOW.md
```

From a repository checkout, the equivalent command is:

```bash
go run ./cmd/autopilot -port 8080 ./WORKFLOW.md
```

Autopilot writes structured JSON logs to stderr and stores run history in `.autopilot/runs.db` next to the resolved `WORKFLOW.md`. That local SQLite store backs the dashboard's recent-runs view and the per-run audit history endpoints.

You can also set `server.port` in `WORKFLOW.md`. When a port is configured or overridden, Autopilot binds to `127.0.0.1` and exposes:

- `/` for the local dashboard and recent runs
- `/runs/<run-id>` for an HTML view of a persisted run
- `/api/v1/state` for a JSON snapshot
- `POST /api/v1/refresh` to trigger an immediate poll and reconcile cycle
- `/api/v1/<issue-identifier>` for per-issue status
- `/api/v1/runs` for recent persisted runs
- `/api/v1/runs/<run-id>` for run detail, including audit events

If `workspace.root` is omitted, Autopilot uses a temporary workspace root under `/tmp/autopilot_workspaces`. `workspace.root` also supports `$VAR` and `~` expansion. If `workspace.provider` is omitted, Autopilot uses the local provider. Set `workspace.provider: docker` to keep the host workspace under `workspace.root` while running hooks and Copilot sessions inside a long-lived per-issue container; when you do, `workspace.image` is required. The workflow file also controls polling interval, workspace hooks, global and per-state concurrency through `agent.max_concurrent_agents_by_state`, Copilot command, `copilot.cli_args`, optional `copilot.model`, and timeouts, plus optional OTLP trace export over HTTP through `telemetry.otel_endpoint`. When set, `telemetry.otel_endpoint` should be an `http://` or `https://` OTLP/HTTP endpoint, not a gRPC endpoint. The config validator accepts `acp_stdio`, `acp_tcp`, and `headless_http` transport names, but this build only implements `acp_stdio`; choosing another transport will fail during startup.

**Test**

For local development, the fast validation path is `go test ./...`. For an end-to-end disposable integration check, use [smoke-test/run.sh](smoke-test/run.sh), which creates a temporary GitHub repository and issue, runs Autopilot against [smoke-test/WORKFLOW-SMOKE.md](smoke-test/WORKFLOW-SMOKE.md), validates dependency-aware dispatch, workspace lifecycle, ACP stdio plumbing, and the local status API, then cleans up the remote repo and local files when the run succeeds.

Run the full test suite:

```bash
go test ./...
```

If you want a quick local verification pass before sending changes:

```bash
go test ./... && go build ./cmd/autopilot
```

Run the disposable smoke test:

```bash
smoke-test/run.sh
```

If you need to inspect artifacts after a failure, use `smoke-test/run.sh --keep-local` to preserve the generated temp directory or `smoke-test/run.sh --keep-remote` to leave the disposable GitHub repository in place.

**Contribute**

Keep changes small and tied to the current behavior contract:

- add or update Go tests when changing orchestration, workflow parsing, tracker behavior, or workspace lifecycle code
- update [example/WORKFLOW.md](example/WORKFLOW.md) when you change supported workflow fields or defaults
- update [SPEC.md](SPEC.md) when you change the documented service contract, runtime APIs, or workflow semantics
- run `go test ./...` before opening a change
- keep documentation changes in the same change set when behavior or configuration changes
