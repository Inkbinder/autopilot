# Autopilot Demo

This demo sets up a real GitHub repository that Autopilot can work through live. It uses the workflow template from `example/WORKFLOW.md`, copies the skill bundle from `example/.agents/skills`, seeds a small CI-backed starter app, and creates a queue of issues that builds a vanilla JavaScript to-do list web application through a mix of parallel foundation work and follow-on integration tickets. The integration ticket is also seeded with GitHub issue dependencies on the first three tickets so the dependency-aware scheduler is visible during the demo.

## Prerequisites

- `gh` authenticated with permission to create repositories, labels, issues, pull requests, and merges under the target owner.
- `git`, `bash`, `python3`, `node`, and `npm`.
- A real GitHub Copilot CLI install available as `copilot`, already authenticated on the machine that will run Autopilot.
- `autopilot` available in `PATH`.

If `autopilot` is not installed yet, install it first with:

```bash
go install github.com/Inkbinder/autopilot/cmd/autopilot@latest
```

If you plan to keep the demo repository private, make sure Git can authenticate to GitHub from non-interactive shells. A reliable path is:

```bash
gh auth setup-git
```

## Setup

Run the setup script from this repository:

```bash
./demo/run.sh --owner YOUR_GITHUB_OWNER
```

Optional flags:

- `--repo NAME` uses a fixed repository name instead of the timestamped default.
- `--output-root DIR` keeps the generated local files in a known directory.
- `--workspace-root DIR` changes where Autopilot workspaces will be created.
- `--port PORT` changes the suggested local status port.
- `--public` creates a public demo repository.

The script creates:

- a GitHub repository seeded with the example skill bundle and a deliberately minimal static starter
- a rendered workflow file that points at the new repository and a local workspace root
- a `start-autopilot.sh` helper that exports `GITHUB_TOKEN` from `gh auth token` and launches Autopilot
- a five-issue demo queue from `demo/demo-issues.json`

All five issues are created in backlog. The integration issue is also created with GitHub issue dependencies on the first three foundation issues. To start the demo, add `autopilot:ready` to issues 1 through 4. The rendered workflow caps itself at five concurrent agents, but only the three unblocked foundation issues should dispatch immediately.

## Run The Demo

Start Autopilot with the helper script printed by `demo/run.sh`. It will look like this:

```bash
/tmp/autopilot-demo.xxxxxx/start-autopilot.sh
```

Once Autopilot is running:

1. Start Autopilot with the generated helper script.
2. Add `autopilot:ready` to the first four issues to trigger the initial dispatch wave.
3. Watch the three foundation issues move from `autopilot:ready` to `autopilot:in-progress` while the already-ready integration issue stays undispatched because it is blocked by those three GitHub issue dependencies.
4. Use that moment to call out that the workflow allows five concurrent agents, but dependency awareness keeps only three slots busy.
5. Follow the generated PRs and workpad comments while the agents work in parallel on the shell, list, and state layers.
6. When each foundation issue reaches `autopilot:human-review`, review its PR.
7. After approval, change that issue label to `autopilot:merging` so Autopilot lands the PR and closes the ticket.
8. Once the three foundation issues are merged, Autopilot can pick up the already-ready integration issue.
9. After the integration issue closes, dispatch the final polish issue.

## Expected Outcomes

By the end of the walkthrough you should see:

- three independent issues active at the same time, each with its own workspace, workpad, and PR, even though the demo workflow allows five concurrent agents
- the already-ready integration issue remains undispatched until its three blockers close
- each dispatched ticket move through `autopilot:ready`, `autopilot:in-progress`, `autopilot:human-review`, `autopilot:merging`, and finally `Closed`
- a PR opened for each issue, updated as the work progresses, and merged after review
- the seeded repository evolves into a working to-do list app built with web components and plain JavaScript only
- tests passing in CI on each PR because the demo template includes a lightweight GitHub Actions workflow

After the final issue closes, clone or open the demo repository and verify the result locally:

```bash
npm install
npm test
npx serve . -l 4173
```

The finished application should provide a basic but usable to-do flow, including task creation, task state changes, filtering, and final polish suitable for a live engineering-lead demo.

## Cleanup

Delete the demo repository when you are finished:

```bash
gh repo delete OWNER/REPO --yes
```

Then remove the local output directory that `demo/run.sh` created.