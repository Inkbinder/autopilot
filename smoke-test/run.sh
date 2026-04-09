#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: smoke-test/run.sh [--owner OWNER] [--port PORT] [--public] [--keep-local] [--keep-remote]

Creates a disposable GitHub repository and issue, renders a smoke workflow,
starts Autopilot against it, validates dispatch and workspace behavior, then
closes the issue to trigger cleanup before deleting the repository and local
temporary files.

Options:
  --owner OWNER    GitHub owner to create the repository under.
                   Defaults to the authenticated user.
  --port PORT      Local Autopilot status port. Default: 18080.
  --public         Create the disposable repository as public instead of private.
  --keep-local     Keep the generated local temp directory after exit.
  --keep-remote    Keep the disposable GitHub repository after exit.
  -h, --help       Show this help.

Requirements:
  - gh authenticated with permission to create issues and repositories
  - a token available to Autopilot through GITHUB_TOKEN
  - git, go, curl, and bash

This smoke harness validates GitHub polling, workspace creation, ACP transport
plumbing, the local status API, and terminal-state cleanup. It uses a fake ACP
server instead of the real Copilot CLI so the run stays deterministic.
EOF
}

log() {
  printf '[smoke-test] %s\n' "$*" >&2
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

urlencode() {
  local value="$1"
  local output=""
  local index char hex

  for ((index = 0; index < ${#value}; index++)); do
    char="${value:index:1}"
    case "$char" in
      [a-zA-Z0-9.~_-])
        output+="$char"
        ;;
      *)
        printf -v hex '%%%02X' "'$char"
        output+="$hex"
        ;;
    esac
  done

  printf '%s' "$output"
}

print_log_tail() {
  if [[ -n "${autopilot_log:-}" && -f "$autopilot_log" ]]; then
    log "Autopilot log tail follows"
    tail -n 50 "$autopilot_log" >&2 || true
  fi
}

cleanup() {
  local exit_code=$?

  trap - EXIT

  if [[ -n "${autopilot_pid:-}" ]] && kill -0 "$autopilot_pid" 2>/dev/null; then
    kill "$autopilot_pid" 2>/dev/null || true
    wait "$autopilot_pid" 2>/dev/null || true
  fi

  if (( exit_code != 0 )); then
    print_log_tail
  fi

  if (( repo_created )) && (( ! keep_remote )) && [[ -n "${full_repo:-}" ]]; then
    log "Deleting disposable repository $full_repo"
    gh repo delete "$full_repo" --yes >/dev/null 2>&1 || log "Remote cleanup failed for $full_repo"
  elif (( repo_created )) && [[ -n "${full_repo:-}" ]]; then
    log "Keeping disposable repository $full_repo"
  fi

  if [[ -n "${tmp_root:-}" && -d "$tmp_root" ]] && (( ! keep_local )); then
    rm -rf "$tmp_root"
  elif [[ -n "${tmp_root:-}" && -d "$tmp_root" ]]; then
    log "Keeping local artifacts at $tmp_root"
  fi

  exit "$exit_code"
}

wait_for_api() {
  local deadline=$((SECONDS + 30))
  local state_url="http://127.0.0.1:$port/api/v1/state"

  while (( SECONDS < deadline )); do
    if curl -sf "$state_url" >/dev/null; then
      return 0
    fi
    if ! kill -0 "$autopilot_pid" 2>/dev/null; then
      die "Autopilot exited before the status API became ready"
    fi
    sleep 1
  done

  die "timed out waiting for $state_url"
}

wait_for_dispatch() {
  local deadline=$((SECONDS + 90))
  local state_url="http://127.0.0.1:$port/api/v1/state"
  local detail_url="http://127.0.0.1:$port/api/v1/$(urlencode "$issue_identifier")"
  local state_json detail_json

  while (( SECONDS < deadline )); do
    if ! kill -0 "$autopilot_pid" 2>/dev/null; then
      die "Autopilot exited before the smoke run dispatched"
    fi

    state_json="$(curl -sf "$state_url" 2>/dev/null || true)"
    detail_json="$(curl -sf "$detail_url" 2>/dev/null || true)"

    if [[ -d "$workspace_path/.git" ]] \
      && grep -Eq '"total_tokens":[1-9][0-9]*' <<<"$state_json" \
      && grep -q '"status":"running"' <<<"$detail_json"; then
      return 0
    fi

    curl -sf -X POST "http://127.0.0.1:$port/api/v1/refresh" >/dev/null 2>&1 || true
    sleep 1
  done

  die "timed out waiting for workspace creation and ACP activity"
}

wait_for_cleanup() {
  local deadline=$((SECONDS + 90))
  local detail_url="http://127.0.0.1:$port/api/v1/$(urlencode "$issue_identifier")"

  while (( SECONDS < deadline )); do
    if ! kill -0 "$autopilot_pid" 2>/dev/null; then
      die "Autopilot exited before terminal cleanup finished"
    fi

    if [[ ! -e "$workspace_path" ]] && ! curl -sf "$detail_url" >/dev/null 2>&1; then
      return 0
    fi

    curl -sf -X POST "http://127.0.0.1:$port/api/v1/refresh" >/dev/null 2>&1 || true
    sleep 1
  done

  die "timed out waiting for workspace cleanup after closing the issue"
}

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"

owner=""
port=18080
visibility="private"
keep_local=0
keep_remote=0

repo_created=0
tmp_root=""
full_repo=""
autopilot_pid=""
autopilot_log=""
issue_identifier=""
workspace_path=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --owner)
      [[ $# -ge 2 ]] || die "missing value for --owner"
      owner="$2"
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
    --keep-local)
      keep_local=1
      shift
      ;;
    --keep-remote)
      keep_remote=1
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

trap cleanup EXIT

require_command gh
require_command git
require_command go
require_command curl
require_command sed

if [[ -z "${GITHUB_TOKEN:-}" ]]; then
  GITHUB_TOKEN="$(gh auth token 2>/dev/null)" || die "GITHUB_TOKEN is not set and gh auth token failed"
  export GITHUB_TOKEN
fi

if [[ -z "${GH_TOKEN:-}" ]]; then
  export GH_TOKEN="$GITHUB_TOKEN"
fi

gh auth status >/dev/null 2>&1 || die "gh is not authenticated"

if [[ -z "$owner" ]]; then
  owner="$(gh api user --jq .login)"
fi
[[ -n "$owner" ]] || die "could not determine GitHub owner"

tmp_root="$(mktemp -d "${TMPDIR:-/tmp}/autopilot-smoke.XXXXXX")"
repo_name="autopilot-smoke-$(date +%Y%m%d%H%M%S)-$RANDOM"
full_repo="$owner/$repo_name"

source_repo="$tmp_root/$repo_name"
workspace_root="$tmp_root/workspaces"
workflow_path="$tmp_root/WORKFLOW-SMOKE.generated.md"
fake_copilot_path="$tmp_root/fake-copilot.sh"
autopilot_bin="$tmp_root/autopilot"
autopilot_log="$tmp_root/autopilot.log"

mkdir -p "$workspace_root"
cp "$script_dir/fake-copilot.sh" "$fake_copilot_path"
chmod +x "$fake_copilot_path"

log "Creating disposable repository $full_repo"
(
  cd "$tmp_root"
  gh repo create "$full_repo" "--$visibility" --add-readme --clone --description "Disposable Autopilot smoke test" >/dev/null
)
repo_created=1

[[ -d "$source_repo/.git" ]] || die "expected cloned source repository at $source_repo"

gh api --method PATCH "repos/$full_repo" -f has_issues=true >/dev/null
gh api --method POST "repos/$full_repo/labels" -f name='autopilot:ready' -f color='0e8a16' -f description='autopilot smoke dispatch' >/dev/null 2>&1 || true

issue_url="$(gh issue create --repo "$full_repo" --title "Autopilot smoke test" --body "Disposable smoke-test issue created by smoke-test/run.sh." --label "autopilot:ready")"
issue_number="${issue_url##*/}"
issue_identifier="$full_repo#$issue_number"
workspace_key="$(printf '%s' "$issue_identifier" | sed 's/[^A-Za-z0-9._-]/_/g')"
workspace_path="$workspace_root/$workspace_key"

log "Rendering smoke workflow"
sed \
  -e "s|__SMOKE_REPOSITORY__|$(escape_sed_replacement "$full_repo")|g" \
  -e "s|__SMOKE_WORKSPACE_ROOT__|$(escape_sed_replacement "$workspace_root")|g" \
  -e "s|__SMOKE_SOURCE_REPO__|$(escape_sed_replacement "$source_repo")|g" \
  -e "s|__SMOKE_COPILOT_COMMAND__|$(escape_sed_replacement "$fake_copilot_path")|g" \
  -e "s|__SMOKE_PORT__|$port|g" \
  "$script_dir/WORKFLOW-SMOKE.md" > "$workflow_path"

log "Building Autopilot from the current checkout"
(
  cd "$repo_root"
  go build -o "$autopilot_bin" ./cmd/autopilot
)

log "Starting Autopilot on port $port"
"$autopilot_bin" -port "$port" "$workflow_path" >"$autopilot_log" 2>&1 &
autopilot_pid=$!

wait_for_api
log "State API is ready"

curl -sf -X POST "http://127.0.0.1:$port/api/v1/refresh" >/dev/null

wait_for_dispatch
log "Dispatch validated: workspace cloned and ACP activity observed"

log "Closing smoke issue $issue_identifier to trigger terminal cleanup"
gh issue close "$issue_number" --repo "$full_repo" --comment "Closing disposable smoke issue after successful Autopilot dispatch." >/dev/null

wait_for_cleanup
log "Cleanup validated: workspace removed and issue released from tracking"
log "Smoke test passed for $full_repo"