# autopilot
Autopilot turns project work into isolated, autonomous implementation runs, allowing teams to manage work instead of watching coding agents.

It is a long-running Go service that polls GitHub issues, creates per-issue workspaces, and runs GitHub Copilot sessions from a repo-owned `WORKFLOW.md`. The service loads the workflow on startup, watches it for changes while running, and can expose a local status dashboard and JSON API.

**Build**

Autopilot targets Go 1.26.

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
- a writable workspace root, either set in `workspace.root` or available through the default `/tmp/autopilot_workspaces`
- a GitHub repository configured in `tracker.repository` using `owner/repo` format
- network access to the GitHub API for the configured repository
- `git`, because the example workflow clones the target repository and the bundled pull, commit, and push skills assume normal git operations inside the workspace
- `gh` authenticated against GitHub, because the bundled GitHub, push, and land skills use it for issue, PR, label, check, and merge operations
- `python3`, because the bundled land skill runs `land_watch.py`
- the GitHub Copilot CLI installed and authenticated as `copilot`, or an alternate command configured through `copilot.command`
- a GitHub token supplied through `GITHUB_TOKEN` or `tracker.api_key`

A good starting point is [example/WORKFLOW.md](example/WORKFLOW.md). Copy it to the repository root as `WORKFLOW.md`, replace `YOUR_ORG/YOUR_REPO`, and update the workspace root and `hooks.after_create` clone command for your environment. The example workflow uses the default `acp_stdio` Copilot transport, dispatches issues with labels such as `autopilot:ready`, and skips issues marked `autopilot:human-review`, `autopilot:blocked`, or `autopilot:question`.

Run with the default root workflow file:

```bash
go run ./cmd/autopilot
```

Run a built binary:

```bash
./bin/autopilot
```

Run with an explicit workflow path:

```bash
go run ./cmd/autopilot ./path/to/WORKFLOW.md
```

Override the local status server port from the command line:

```bash
go run ./cmd/autopilot -port 8080 ./WORKFLOW.md
```

You can also set `server.port` in `WORKFLOW.md`. When a port is configured or overridden, Autopilot binds to `127.0.0.1` and exposes:

- `/` for the local dashboard
- `/api/v1/state` for a JSON snapshot
- `/api/v1/refresh` to trigger an immediate poll and reconcile cycle
- `/api/v1/<issue-identifier>` for per-issue status

If `workspace.root` is omitted, Autopilot uses a temporary workspace root under `/tmp/autopilot_workspaces`. The workflow file also controls polling interval, workspace hooks, concurrency, Copilot transport, and timeouts.

**Test**

For local development, the fast validation path is `go test ./...`. For an end-to-end disposable integration check, use [smoke-test/run.sh](smoke-test/run.sh), which creates a temporary GitHub repository and issue, runs Autopilot against [smoke-test/WORKFLOW-SMOKE.md](smoke-test/WORKFLOW-SMOKE.md), then cleans up the remote repo and local files when the run succeeds.

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

**Contribute**

Keep changes small and tied to the current behavior contract:

- add or update Go tests when changing orchestration, workflow parsing, tracker behavior, or workspace lifecycle code
- update [example/WORKFLOW.md](example/WORKFLOW.md) when you change supported workflow fields or defaults
- update [SPEC.md](SPEC.md) when you change the documented service contract, runtime APIs, or workflow semantics
- run `go test ./...` before opening a change
- keep documentation changes in the same change set when behavior or configuration changes
