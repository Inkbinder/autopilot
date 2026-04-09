package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"autopilot/internal/workflow"
)

type recordingRunner struct {
	calls []runnerCall
	err   error
}

type runnerCall struct {
	Dir    string
	Script string
}

func (runner *recordingRunner) Run(_ context.Context, workingDir string, script string) (ScriptResult, error) {
	runner.calls = append(runner.calls, runnerCall{Dir: workingDir, Script: script})
	if runner.err != nil {
		return ScriptResult{}, runner.err
	}
	return ScriptResult{}, nil
}

func TestManagerCreateForIssueSanitizesAndRunsAfterCreateOnce(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "workspaces")
	runner := &recordingRunner{}
	manager, err := NewManager(workflow.Config{
		Workspace: workflow.WorkspaceConfig{Root: root},
		Hooks: workflow.HooksConfig{AfterCreate: "echo create"},
	}, runner)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	workspace, err := manager.CreateForIssue(context.Background(), "octo/widgets#123")
	if err != nil {
		t.Fatalf("CreateForIssue() error = %v", err)
	}
	if workspace.WorkspaceKey != "octo_widgets_123" {
		t.Fatalf("workspace key = %q", workspace.WorkspaceKey)
	}
	if !workspace.CreatedNow {
		t.Fatal("expected first create to mark CreatedNow")
	}
	if len(runner.calls) != 1 || runner.calls[0].Script != "echo create" {
		t.Fatalf("unexpected hook calls: %#v", runner.calls)
	}

	workspace, err = manager.CreateForIssue(context.Background(), "octo/widgets#123")
	if err != nil {
		t.Fatalf("CreateForIssue() second call error = %v", err)
	}
	if workspace.CreatedNow {
		t.Fatal("expected reused workspace to have CreatedNow=false")
	}
	if len(runner.calls) != 1 {
		t.Fatalf("after_create should run once, got %d calls", len(runner.calls))
	}
}

func TestManagerPrepareForRunRemovesTransientArtifactsAndRunsBeforeRun(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "workspaces")
	runner := &recordingRunner{}
	manager, err := NewManager(workflow.Config{
		Workspace: workflow.WorkspaceConfig{Root: root},
		Hooks: workflow.HooksConfig{BeforeRun: "echo before"},
	}, runner)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	workspace, err := manager.CreateForIssue(context.Background(), "octo/widgets#321")
	if err != nil {
		t.Fatalf("CreateForIssue() error = %v", err)
	}
	for _, transient := range []string{"tmp", ".elixir_ls"} {
		path := filepath.Join(workspace.Path, transient)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", transient, err)
		}
	}
	if err := manager.PrepareForRun(context.Background(), workspace); err != nil {
		t.Fatalf("PrepareForRun() error = %v", err)
	}
	for _, transient := range []string{"tmp", ".elixir_ls"} {
		if _, err := os.Stat(filepath.Join(workspace.Path, transient)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected %s to be removed, stat err = %v", transient, err)
		}
	}
	if len(runner.calls) != 1 || runner.calls[0].Script != "echo before" {
		t.Fatalf("unexpected hook calls: %#v", runner.calls)
	}
}

func TestManagerFailsOnWorkspaceCollisionWithFile(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "workspaces")
	manager, err := NewManager(workflow.Config{Workspace: workflow.WorkspaceConfig{Root: root}}, &recordingRunner{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	collisionPath := filepath.Join(root, "octo_widgets_999")
	if err := os.WriteFile(collisionPath, []byte("collision"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	_, err = manager.CreateForIssue(context.Background(), "octo/widgets#999")
	if err == nil {
		t.Fatal("CreateForIssue() error = nil, want error")
	}
}