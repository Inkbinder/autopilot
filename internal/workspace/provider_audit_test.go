package workspace

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Inkbinder/autopilot/internal/runstate"
	"github.com/Inkbinder/autopilot/internal/workflow"
)

type auditRecorder struct {
	events []runstate.AuditEvent
}

func (recorder *auditRecorder) CreateRun(context.Context, runstate.CreateRunParams) (int64, error) {
	return 0, nil
}

func (recorder *auditRecorder) UpdateRun(context.Context, runstate.UpdateRunParams) error {
	return nil
}

func (recorder *auditRecorder) InsertAuditEvent(_ context.Context, event runstate.AuditEvent) error {
	recorder.events = append(recorder.events, event)
	return nil
}

func TestLocalProviderExecuteContextAuditsCommandOutput(t *testing.T) {
	t.Parallel()
	recorder := &auditRecorder{}
	root := filepath.Join(t.TempDir(), "workspaces")
	provider, err := NewLocalProviderWithOptions(workflow.WorkspaceConfig{Root: root}, ProviderOptions{AuditWriter: recorder})
	if err != nil {
		t.Fatalf("NewLocalProviderWithOptions() error = %v", err)
	}
	workspacePath, err := provider.Setup("octo/widgets#77", workflow.WorkspaceConfig{Root: root})
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	ctx := runstate.WithMetadata(context.Background(), runstate.Metadata{RunID: 7, IssueID: "77", Repo: "octo/widgets"})
	if _, err := provider.ExecuteContext(ctx, "sh", []string{"-lc", "printf audit-ok"}, workspacePath); err != nil {
		t.Fatalf("ExecuteContext() error = %v", err)
	}
	if len(recorder.events) != 1 {
		t.Fatalf("audit event count = %d, want 1", len(recorder.events))
	}
	if recorder.events[0].ActionType != "workspace_exec" {
		t.Fatalf("action_type = %q, want workspace_exec", recorder.events[0].ActionType)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(recorder.events[0].Payload), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if payload["command"] != "sh" {
		t.Fatalf("payload.command = %#v, want sh", payload["command"])
	}
	if payload["output"] != "audit-ok" {
		t.Fatalf("payload.output = %#v, want audit-ok", payload["output"])
	}
	if payload["success"] != true {
		t.Fatalf("payload.success = %#v, want true", payload["success"])
	}
}
