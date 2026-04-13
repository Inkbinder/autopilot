//go:build integration

package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/docker/docker/client"

	"github.com/Inkbinder/autopilot/internal/copilot"
	"github.com/Inkbinder/autopilot/internal/model"
	"github.com/Inkbinder/autopilot/internal/runstate"
	"github.com/Inkbinder/autopilot/internal/workflow"
	workspacepkg "github.com/Inkbinder/autopilot/internal/workspace"
)

type integrationRunStoreRecorder struct {
	mu      sync.Mutex
	creates []runstate.CreateRunParams
	updates []runstate.UpdateRunParams
	events  []runstate.AuditEvent
}

func (recorder *integrationRunStoreRecorder) CreateRun(_ context.Context, params runstate.CreateRunParams) (int64, error) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.creates = append(recorder.creates, params)
	return int64(len(recorder.creates)), nil
}

func (recorder *integrationRunStoreRecorder) UpdateRun(_ context.Context, params runstate.UpdateRunParams) error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.updates = append(recorder.updates, params)
	return nil
}

func (recorder *integrationRunStoreRecorder) InsertAuditEvent(_ context.Context, event runstate.AuditEvent) error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.events = append(recorder.events, event)
	return nil
}

func (recorder *integrationRunStoreRecorder) snapshot() ([]runstate.CreateRunParams, []runstate.UpdateRunParams, []runstate.AuditEvent) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	creates := append([]runstate.CreateRunParams(nil), recorder.creates...)
	updates := append([]runstate.UpdateRunParams(nil), recorder.updates...)
	events := append([]runstate.AuditEvent(nil), recorder.events...)
	return creates, updates, events
}

func TestOrchestratorDockerSiblingContainerRunsFakeACPServer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping docker-backed integration test in short mode")
	}
	requireDockerAccess(t)

	workspaceRoot := integrationWorkspaceRoot(t)
	workflowPath := writeDockerACPWorkflowFile(t, workspaceRoot)
	_, config, err := workflow.LoadAndResolve(workflowPath, nil)
	if err != nil {
		t.Fatalf("LoadAndResolve() error = %v", err)
	}

	store := &integrationRunStoreRecorder{}
	provider, err := workspacepkg.NewDockerProviderWithOptions(config.Workspace, workspacepkg.ProviderOptions{AuditWriter: store})
	if err != nil {
		t.Fatalf("NewDockerProviderWithOptions() error = %v", err)
	}
	manager, err := workspacepkg.NewManager(config, provider)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	copilotClient, err := copilot.NewClientWithOptions(config, copilot.ClientOptions{AuditWriter: store, StreamExecutor: provider})
	if err != nil {
		t.Fatalf("NewClientWithOptions() error = %v", err)
	}

	issue := model.Issue{ID: "1", Identifier: "octo/widgets#1", Title: "ACP integration", State: "Open", Labels: []string{"autopilot:ready"}}
	tracker := &fakeTracker{candidates: []model.Issue{issue}, statesByID: map[string]model.Issue{
		"1": {ID: issue.ID, Identifier: issue.Identifier, Title: issue.Title, State: "Closed"},
	}}
	orchestrator, err := New(workflowPath, Options{Builder: fakeBuilder{tracker: tracker, workspace: manager, copilot: copilotClient}, RunStore: store})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer orchestrator.shutdown(context.Background())

	workspacePath := filepath.Join(workspaceRoot, workspacepkg.SanitizeWorkspaceKey(issue.Identifier))
	t.Cleanup(func() {
		_ = manager.RemoveForIssue(context.Background(), issue.Identifier)
	})

	orchestrator.tick(context.Background())
	orchestrator.wg.Wait()

	if got := strings.TrimSpace(readIntegrationFile(t, filepath.Join(workspacePath, "fake-acp-argv.txt"))); got != "--acp --stdio" {
		t.Fatalf("fake ACP argv = %q, want %q", got, "--acp --stdio")
	}
	if got := strings.TrimSpace(readIntegrationFile(t, filepath.Join(workspacePath, "fake-acp-cwd.txt"))); got != "/workspace" {
		t.Fatalf("fake ACP cwd = %q, want %q", got, "/workspace")
	}
	if got := strings.TrimSpace(readIntegrationFile(t, filepath.Join(workspacePath, "fake-acp-prompt.txt"))); got != "Integration prompt for octo/widgets#1" {
		t.Fatalf("fake ACP prompt = %q, want %q", got, "Integration prompt for octo/widgets#1")
	}

	snapshot := orchestrator.Snapshot()
	if snapshot.CopilotTotals.InputTokens != 10 || snapshot.CopilotTotals.OutputTokens != 5 || snapshot.CopilotTotals.TotalTokens != 15 {
		t.Fatalf("copilot totals = %#v, want 10/5/15", snapshot.CopilotTotals)
	}

	creates, updates, events := store.snapshot()
	if len(creates) != 1 {
		t.Fatalf("create count = %d, want 1", len(creates))
	}
	if len(updates) < 2 {
		t.Fatalf("update count = %d, want at least 2", len(updates))
	}
	lastUpdate := updates[len(updates)-1]
	if lastUpdate.Status != runstate.StatusSuccess {
		t.Fatalf("last status = %q, want success", lastUpdate.Status)
	}
	if lastUpdate.EndTime == nil {
		t.Fatal("expected final run update to set EndTime")
	}

	if !hasAuditEvent(events, "copilot_cli_start", func(payload map[string]any) bool {
		return payload["workspace_path"] == workspacePath && payload["execution_dir"] == "/workspace" && payload["success"] == true
	}) {
		t.Fatalf("missing copilot_cli_start event with workspace_path=%q and execution_dir=/workspace: %#v", workspacePath, events)
	}
	if !hasAuditEvent(events, "llm_prompt", func(payload map[string]any) bool {
		return payload["prompt"] == "Integration prompt for octo/widgets#1"
	}) {
		t.Fatalf("missing llm_prompt event for prompt %q: %#v", "Integration prompt for octo/widgets#1", events)
	}
	if !hasAuditEvent(events, "llm_response", func(payload map[string]any) bool {
		return payload["method"] == "agent/update"
	}) {
		t.Fatalf("missing llm_response event for fake ACP notification: %#v", events)
	}
}

func integrationWorkspaceRoot(t *testing.T) string {
	t.Helper()
	if hostRoot := strings.TrimSpace(os.Getenv("AUTOPILOT_HOST_ROOT")); hostRoot != "" {
		sharedBase := filepath.Join(hostRoot, ".autopilot-integration")
		if err := os.MkdirAll(sharedBase, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", sharedBase, err)
		}
		root, err := os.MkdirTemp(sharedBase, "workspaces-*")
		if err != nil {
			t.Fatalf("MkdirTemp(%q) error = %v", sharedBase, err)
		}
		t.Cleanup(func() {
			_ = os.RemoveAll(root)
		})
		return root
	}
	return t.TempDir()
}

func requireDockerAccess(t *testing.T) {
	t.Helper()
	dockerClient, err := client.NewClientWithOpts(client.WithHost("unix:///var/run/docker.sock"), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("skipping docker-backed integration test: docker client unavailable: %v", err)
	}
	defer dockerClient.Close()
	if _, err := dockerClient.Ping(context.Background()); err != nil {
		t.Skipf("skipping docker-backed integration test: docker daemon unavailable: %v", err)
	}
}

func writeDockerACPWorkflowFile(t *testing.T, workspaceRoot string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "WORKFLOW.integration.md")
	content := `---
tracker:
  kind: github
  repository: octo/widgets
  api_key: token
polling:
  interval_ms: 25
workspace:
  provider: docker
  root: ` + strconv.Quote(workspaceRoot) + `
  image: "ubuntu:24.04"
hooks:
  after_create: |
    cat > fake-acp.sh <<'EOF'
    #!/usr/bin/env bash
    set -euo pipefail

    printf '%s\n' "$*" > fake-acp-argv.txt
    while IFS= read -r line; do
      request_id="$(printf '%s\n' "$line" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')"
      case "$line" in
        *'"method":"initialize"'*)
          printf '{"id":%s,"result":{"ok":true}}\n' "$request_id"
          ;;
        *'"method":"session/new"'*)
          cwd="$(printf '%s\n' "$line" | sed -n 's/.*"cwd":"\([^"]*\)".*/\1/p')"
          printf '%s\n' "$cwd" > fake-acp-cwd.txt
          printf '{"id":%s,"result":{"sessionId":"fake-session"}}\n' "$request_id"
          ;;
        *'"method":"session/prompt"'*)
          prompt="$(printf '%s\n' "$line" | sed -n 's/.*"text":"\([^"]*\)".*/\1/p')"
          printf '%s\n' "$prompt" > fake-acp-prompt.txt
          printf '{"method":"agent/update","params":{"message":"working from container"}}\n'
          printf '{"id":%s,"result":{"stopReason":"end_turn","input_tokens":10,"output_tokens":5,"total_tokens":15}}\n' "$request_id"
          ;;
      esac
    done
    EOF
    chmod +x fake-acp.sh
agent:
  max_turns: 1
copilot:
  command: ./fake-acp.sh
  transport: acp_stdio
  model: gpt-5.4
  prompt_timeout_ms: 5000
  startup_timeout_ms: 5000
---
Integration prompt for {{ issue.identifier }}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func readIntegrationFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	return string(contents)
}

func hasAuditEvent(events []runstate.AuditEvent, actionType string, match func(map[string]any) bool) bool {
	for _, event := range events {
		if event.ActionType != actionType {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
			continue
		}
		if match(payload) {
			return true
		}
	}
	return false
}
