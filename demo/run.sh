#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: demo/run.sh [--owner OWNER] [--repo NAME] [--output-root DIR] [--workspace-root DIR] [--port PORT] [--public]

Creates a GitHub repository for the Autopilot demo, seeds it with the example
skill bundle and a CI-ready starter app, renders a workflow from
example/WORKFLOW.md, and creates the demo issues from demo/demo-issues.json.

Options:
  --owner OWNER         GitHub owner that will receive the demo repository.
                        Defaults to the authenticated user.
  --repo NAME           Repository name. Defaults to autopilot-demo-<timestamp>.
  --output-root DIR     Directory for generated local artifacts. Defaults to a
                        new temporary directory.
  --workspace-root DIR  Workspace root to write into the generated workflow.
                        Defaults to <output-root>/workspaces.
  --port PORT           Suggested Autopilot status port. Default: 19090.
  --public              Create the demo repository as public instead of private.
  -h, --help            Show this help.

Requirements:
  - gh authenticated with permission to create repositories, labels, and issues
  - git, bash, sed, and python3
  - a real GitHub Copilot CLI install available as `copilot` before you start
    Autopilot
  - an `autopilot` binary in PATH, or installable via `go install`

The generated workflow keeps the real `copilot` command from example/WORKFLOW.md.
This setup script does not start Autopilot for you; it writes a helper script
that exports GITHUB_TOKEN from `gh auth token` and launches Autopilot against
the rendered workflow.
EOF
}

log() {
  printf '[demo] %s\n' "$*" >&2
}

die() {
  log "ERROR: $*"
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

escape_sed_replacement() {
  printf '%s' "$1" | sed -e 's/[&|\\]/\\&/g'
}

ensure_git_identity() {
  local repo_dir="$1"
  local login user_id display_name

  if ! git -C "$repo_dir" config user.name >/dev/null; then
    display_name="$(gh api user --jq '.name // .login')"
    [[ -n "$display_name" && "$display_name" != "null" ]] || die "could not determine a git user.name from gh"
    git -C "$repo_dir" config user.name "$display_name"
  fi

  if ! git -C "$repo_dir" config user.email >/dev/null; then
    login="$(gh api user --jq .login)"
    user_id="$(gh api user --jq .id)"
    [[ -n "$login" && -n "$user_id" ]] || die "could not determine a git user.email from gh"
    git -C "$repo_dir" config user.email "${user_id}+${login}@users.noreply.github.com"
  fi
}

create_label() {
  local repo="$1"
  local name="$2"
  local color="$3"
  local description="$4"

  gh api --method POST "repos/$repo/labels" \
    -f name="$name" \
    -f color="$color" \
    -f description="$description" >/dev/null 2>&1 || true
}

create_demo_issues() {
  local issues_path="$1"
  local repo="$2"

  python3 - "$issues_path" "$repo" <<'PY'
import json
import os
import subprocess
import sys
import tempfile

issues_path, repo = sys.argv[1], sys.argv[2]

with open(issues_path, encoding="utf-8") as handle:
    issues = json.load(handle)

for index, issue in enumerate(issues, start=1):
    with tempfile.NamedTemporaryFile("w", encoding="utf-8", delete=False) as body_file:
        body_file.write(issue["body"].rstrip() + "\n")
        body_path = body_file.name
    try:
        command = [
            "gh",
            "issue",
            "create",
            "--repo",
            repo,
            "--title",
            issue["title"],
            "--body-file",
            body_path,
        ]
        for label in issue.get("labels", []):
            command.extend(["--label", label])
        result = subprocess.run(command, check=True, capture_output=True, text=True)
    finally:
        os.unlink(body_path)

    url = result.stdout.strip().splitlines()[-1]
    print(f"{index}. {issue['title']} -> {url}")
PY
}

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"

owner=""
repo_name=""
output_root=""
workspace_root=""
port=19090
visibility="private"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --owner)
      [[ $# -ge 2 ]] || die "missing value for --owner"
      owner="$2"
      shift 2
      ;;
    --repo)
      [[ $# -ge 2 ]] || die "missing value for --repo"
      repo_name="$2"
      shift 2
      ;;
    --output-root)
      [[ $# -ge 2 ]] || die "missing value for --output-root"
      output_root="$2"
      shift 2
      ;;
    --workspace-root)
      [[ $# -ge 2 ]] || die "missing value for --workspace-root"
      workspace_root="$2"
      shift 2
      ;;
    --port)
      [[ $# -ge 2 ]] || die "missing value for --port"
      port="$2"
      shift 2
      ;;
    --public)
      visibility="public"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

require_command gh
require_command git
require_command python3
require_command sed

if [[ -z "${GITHUB_TOKEN:-}" ]]; then
  GITHUB_TOKEN="$(gh auth token 2>/dev/null)" || die "GITHUB_TOKEN is not set and gh auth token failed"
  export GITHUB_TOKEN
fi

if [[ -z "${GH_TOKEN:-}" ]]; then
  export GH_TOKEN="$GITHUB_TOKEN"
fi

gh auth status >/dev/null 2>&1 || die "gh is not authenticated"

if ! gh auth setup-git >/dev/null 2>&1; then
  log "WARNING: gh auth setup-git failed; private repo clones from Autopilot workspaces may require manual git credential configuration"
fi

if ! command -v autopilot >/dev/null 2>&1; then
  log "WARNING: autopilot is not currently on PATH; install it before running the generated start-autopilot.sh"
fi

if ! command -v copilot >/dev/null 2>&1; then
  log "WARNING: copilot is not currently on PATH; the rendered workflow expects a real Copilot CLI install"
fi

if [[ -z "$owner" ]]; then
  owner="$(gh api user --jq .login)"
fi
[[ -n "$owner" ]] || die "could not determine GitHub owner"

if [[ -z "$repo_name" ]]; then
  repo_name="autopilot-demo-$(date +%Y%m%d%H%M%S)"
fi

if [[ -z "$output_root" ]]; then
  output_root="$(mktemp -d "${TMPDIR:-/tmp}/autopilot-demo.XXXXXX")"
else
  mkdir -p "$output_root"
  output_root="$(cd "$output_root" && pwd)"
fi

if [[ -z "$workspace_root" ]]; then
  workspace_root="$output_root/workspaces"
fi

mkdir -p "$workspace_root"
workspace_root="$(cd "$workspace_root" && pwd)"

full_repo="$owner/$repo_name"
source_repo="$output_root/$repo_name"
workflow_path="$output_root/WORKFLOW.generated.md"
start_script_path="$output_root/start-autopilot.sh"
issues_path="$script_dir/demo-issues.json"

[[ -f "$issues_path" ]] || die "missing demo issue seed file: $issues_path"
[[ -d "$script_dir/template" ]] || die "missing demo template directory: $script_dir/template"
[[ -f "$repo_root/example/WORKFLOW.md" ]] || die "missing example workflow template"
[[ -d "$repo_root/example/.agents" ]] || die "missing example skill bundle"

log "Creating demo repository $full_repo"
(
  cd "$output_root"
  gh repo create "$full_repo" "--$visibility" --add-readme --clone --description "Autopilot engineering demo repository" >/dev/null
)

[[ -d "$source_repo/.git" ]] || die "expected cloned repository at $source_repo"

ensure_git_identity "$source_repo"

log "Seeding starter application and example skills"
cp -R "$script_dir/template/." "$source_repo/"
cp -R "$repo_root/example/.agents" "$source_repo/"

(
  cd "$source_repo"
  git add -A
  if ! git diff --cached --quiet; then
    git commit -m "chore: seed autopilot demo"
    git push origin HEAD >/dev/null
  fi
)

log "Enabling issues and provisioning labels"
gh api --method PATCH "repos/$full_repo" -f has_issues=true >/dev/null
create_label "$full_repo" "demo" "1f6feb" "autopilot demo issue"
create_label "$full_repo" "autopilot:ready" "0e8a16" "queued for Autopilot dispatch"
create_label "$full_repo" "autopilot:in-progress" "fbca04" "actively handled by Autopilot"
create_label "$full_repo" "autopilot:human-review" "5319e7" "waiting for reviewer approval"
create_label "$full_repo" "autopilot:rework" "d93f0b" "needs another Autopilot implementation pass"
create_label "$full_repo" "autopilot:merging" "0052cc" "approved and ready for Autopilot landing"
create_label "$full_repo" "autopilot:blocked" "b60205" "blocked on an external dependency or decision"
create_label "$full_repo" "autopilot:question" "006b75" "waiting on a product clarification"

log "Creating demo issue queue"
issue_summary="$(create_demo_issues "$issues_path" "$full_repo")"

log "Rendering workflow from example/WORKFLOW.md"
sed \
  -e "s|repository: YOUR_ORG/YOUR_REPO|repository: $(escape_sed_replacement "$full_repo")|g" \
  -e "s|root: ~/code/autopilot-workspaces|root: $(escape_sed_replacement "$workspace_root")|g" \
  -e "s|git clone --depth 1 https://github.com/YOUR_ORG/YOUR_REPO \.|git clone --depth 1 https://github.com/$full_repo .|g" \
  "$repo_root/example/WORKFLOW.md" > "$workflow_path"

cat > "$start_script_path" <<EOF
#!/usr/bin/env bash
set -euo pipefail

autopilot_command="\${AUTOPILOT_COMMAND:-autopilot}"
workflow_path=$(printf '%q' "$workflow_path")
default_port=$(printf '%q' "$port")

if [[ -z "\${GITHUB_TOKEN:-}" ]]; then
  GITHUB_TOKEN="\$(gh auth token 2>/dev/null)" || {
    printf 'Unable to resolve GITHUB_TOKEN from gh auth. Export GITHUB_TOKEN and retry.\n' >&2
    exit 1
  }
  export GITHUB_TOKEN
fi

if [[ -z "\${GH_TOKEN:-}" ]]; then
  export GH_TOKEN="\$GITHUB_TOKEN"
fi

port="\${1:-\$default_port}"
exec "\$autopilot_command" -port "\$port" "\$workflow_path"
EOF
chmod +x "$start_script_path"

repo_url="https://github.com/$full_repo"

cat <<EOF
Demo repository: $repo_url
Local clone: $source_repo
Rendered workflow: $workflow_path
Workspace root: $workspace_root
Autopilot launcher: $start_script_path

Issue queue:
$issue_summary

Next steps:
1. Inspect the seeded repository and issue queue.
2. Start Autopilot with: $start_script_path
3. Add autopilot:ready to the first three foundation issues to trigger the parallel demo wave.
4. Review and merge those issues by moving each label from autopilot:human-review to autopilot:merging.
5. After the three foundation issues close, add autopilot:ready to the integration issue, then do the same for the final polish issue.
EOF