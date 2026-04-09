# Autopilot Demo

This demo sets up a real GitHub repository that Autopilot can work through live. It uses the workflow template from `example/WORKFLOW.md`, copies the skill bundle from `example/.agents/skills`, seeds a small CI-backed starter app, and creates a queue of issues that builds a vanilla JavaScript to-do list web application through a mix of parallel foundation work and follow-on integration tickets.

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

All five issues are created in backlog. To start the parallel wave, the presenter should add `autopilot:ready` to the first three foundation issues. The integration and polish issues should stay in backlog until you dispatch them later.

## Run The Demo

Start Autopilot with the helper script printed by `demo/run.sh`. It will look like this:

```bash
/tmp/autopilot-demo.xxxxxx/start-autopilot.sh
```

Once Autopilot is running:

1. Start Autopilot with the generated helper script.
2. Add `autopilot:ready` to the first three foundation issues to trigger the parallel dispatch wave.
3. Watch those three foundation issues move from `autopilot:ready` to `autopilot:in-progress` as a small team of agents starts work.
4. Follow the generated PRs and workpad comments while the agents work in parallel on the shell, list, and state layers.
5. When each foundation issue reaches `autopilot:human-review`, review its PR.
6. After approval, change that issue label to `autopilot:merging` so Autopilot lands the PR and closes the ticket.
7. Once the three foundation issues are merged, add `autopilot:ready` to the integration issue.
8. After the integration issue closes, dispatch the final polish issue.

## Expected Outcomes

By the end of the walkthrough you should see:

- three independent issues active at the same time, each with its own workspace, workpad, and PR
- each dispatched ticket move through `autopilot:ready`, `autopilot:in-progress`, `autopilot:human-review`, `autopilot:merging`, and finally `Closed`
- a PR opened for each issue, updated as the work progresses, and merged after review
- the seeded repository evolve into a working to-do list app built with web components and plain JavaScript only
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