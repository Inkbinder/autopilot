package copilot

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Inkbinder/autopilot/internal/runstate"
	"github.com/Inkbinder/autopilot/internal/workflow"
	"github.com/Inkbinder/autopilot/internal/workspace"
)

type auditEventRecorder struct {
	mu     sync.Mutex
	events []runstate.AuditEvent
}

func (recorder *auditEventRecorder) CreateRun(context.Context, runstate.CreateRunParams) (int64, error) {
	return 0, nil
}

func (recorder *auditEventRecorder) UpdateRun(context.Context, runstate.UpdateRunParams) error {
	return nil
}

func (recorder *auditEventRecorder) InsertAuditEvent(_ context.Context, event runstate.AuditEvent) error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.events = append(recorder.events, event)
	return nil
}

func (recorder *auditEventRecorder) snapshot() []runstate.AuditEvent {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]runstate.AuditEvent(nil), recorder.events...)
}

func TestACPStdioClientWritesAuditEvents(t *testing.T) {
	t.Parallel()
	workspacePath := t.TempDir()
	script := writeExecutableScript(t, `#!/usr/bin/env bash
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*)
      echo '{"id":1,"result":{"ok":true}}'
      ;;
    *'"method":"session/new"'*)
      echo '{"id":2,"result":{"sessionId":"session-audit"}}'
      ;;
    *'"method":"session/prompt"'*)
      echo '{"method":"session/update","params":{"sessionId":"session-audit","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"working"}}}}'
      echo '{"id":3,"result":{"stopReason":"end_turn","input_tokens":10,"output_tokens":5,"total_tokens":15}}'
      ;;
  esac
done
`)
	recorder := &auditEventRecorder{}
	provider, err := workspace.NewLocalProvider(workflow.WorkspaceConfig{Root: workspacePath})
	if err != nil {
		t.Fatalf("NewLocalProvider() error = %v", err)
	}
	client, err := NewClientWithOptions(workflow.Config{Copilot: workflow.CopilotConfig{Transport: "acp_stdio"}}, ClientOptions{AuditWriter: recorder, StreamExecutor: provider})
	if err != nil {
		t.Fatalf("NewClientWithOptions() error = %v", err)
	}
	ctx := runstate.WithMetadata(context.Background(), runstate.Metadata{RunID: 42, IssueID: "1", Repo: "octo/widgets"})
	session, err := client.StartSession(ctx, StartRequest{
		WorkspacePath: workspacePath,
		Copilot: workflow.CopilotConfig{
			Command:        script,
			Transport:      "acp_stdio",
			StartupTimeout: time.Second,
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer session.Close(context.Background())
	if err := session.RunPrompt(ctx, "do work", 1); err != nil {
		t.Fatalf("RunPrompt() error = %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for len(recorder.snapshot()) < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	events := recorder.snapshot()
	if len(events) < 3 {
		t.Fatalf("audit event count = %d, want at least 3", len(events))
	}
	actions := map[string]runstate.AuditEvent{}
	for _, event := range events {
		actions[event.ActionType] = event
	}
	for _, actionType := range []string{"copilot_cli_start", "llm_prompt", "llm_response"} {
		if _, ok := actions[actionType]; !ok {
			t.Fatalf("missing audit action %q in %#v", actionType, events)
		}
	}
	var promptPayload map[string]any
	if err := json.Unmarshal([]byte(actions["llm_prompt"].Payload), &promptPayload); err != nil {
		t.Fatalf("json.Unmarshal(llm_prompt) error = %v", err)
	}
	if promptPayload["prompt"] != "do work" {
		t.Fatalf("prompt payload = %#v, want do work", promptPayload["prompt"])
	}
}
