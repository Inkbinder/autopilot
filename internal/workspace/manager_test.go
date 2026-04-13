package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Inkbinder/autopilot/internal/workflow"
)

type recordingProvider struct {
	root          string
	setupCalls    []string
	executeCalls  []providerCall
	teardownCalls []string
	setupErr      error
	executeErr    error
	teardownErr   error
}

type providerCall struct {
	Command string
	Args    []string
	Dir     string
}

func (provider *recordingProvider) WorkspacePath(issueIdentifier string) string {
	return filepath.Join(provider.root, SanitizeWorkspaceKey(issueIdentifier))
}

func (provider *recordingProvider) Setup(issueIdentifier string, _ WorkspaceConfig) (string, error) {
	provider.setupCalls = append(provider.setupCalls, issueIdentifier)
	if provider.setupErr != nil {
		return "", provider.setupErr
	}
	workspacePath := provider.WorkspacePath(issueIdentifier)
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		return "", err
	}
	return workspacePath, nil
}

func (provider *recordingProvider) Execute(command string, args []string, dir string) (string, error) {
	provider.executeCalls = append(provider.executeCalls, providerCall{Command: command, Args: append([]string(nil), args...), Dir: dir})
	if provider.executeErr != nil {
		return "", provider.executeErr
	}
	return "", nil
}

func (provider *recordingProvider) ExecuteStream(context.Context, string, []string, string) (ExecutionStream, error) {
	return nil, errors.New("not implemented")
}

func (provider *recordingProvider) Teardown(issueIdentifier string) error {
	provider.teardownCalls = append(provider.teardownCalls, issueIdentifier)
	if provider.teardownErr != nil {
		return provider.teardownErr
	}
	return os.RemoveAll(provider.WorkspacePath(issueIdentifier))
}

func TestManagerCreateForIssueSanitizesAndRunsAfterCreateOnce(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "workspaces")
	provider := &recordingProvider{root: root}
	manager, err := NewManager(workflow.Config{
		Workspace: workflow.WorkspaceConfig{Root: root},
		Hooks:     workflow.HooksConfig{AfterCreate: "echo create"},
	}, provider)
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
	if len(provider.executeCalls) != 1 {
		t.Fatalf("unexpected hook calls: %#v", provider.executeCalls)
	}
	if provider.executeCalls[0].Command != "sh" || len(provider.executeCalls[0].Args) != 2 || provider.executeCalls[0].Args[1] != "echo create" {
		t.Fatalf("unexpected hook call: %#v", provider.executeCalls[0])
	}

	workspace, err = manager.CreateForIssue(context.Background(), "octo/widgets#123")
	if err != nil {
		t.Fatalf("CreateForIssue() second call error = %v", err)
	}
	if workspace.CreatedNow {
		t.Fatal("expected reused workspace to have CreatedNow=false")
	}
	if len(provider.executeCalls) != 1 {
		t.Fatalf("after_create should run once, got %d calls", len(provider.executeCalls))
	}
}

func TestManagerPrepareForRunRemovesTransientArtifactsAndRunsBeforeRun(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "workspaces")
	provider := &recordingProvider{root: root}
	manager, err := NewManager(workflow.Config{
		Workspace: workflow.WorkspaceConfig{Root: root},
		Hooks:     workflow.HooksConfig{BeforeRun: "echo before"},
	}, provider)
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
	if len(provider.executeCalls) != 1 {
		t.Fatalf("unexpected hook calls: %#v", provider.executeCalls)
	}
	if provider.executeCalls[0].Command != "sh" || len(provider.executeCalls[0].Args) != 2 || provider.executeCalls[0].Args[1] != "echo before" {
		t.Fatalf("unexpected hook call: %#v", provider.executeCalls[0])
	}
}

func TestManagerFailsOnWorkspaceCollisionWithFile(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "workspaces")
	provider := &recordingProvider{root: root}
	manager, err := NewManager(workflow.Config{Workspace: workflow.WorkspaceConfig{Root: root}}, provider)
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
	if len(provider.setupCalls) != 0 {
		t.Fatalf("expected setup to be skipped on collision, got %d calls", len(provider.setupCalls))
	}
}

func TestManagerTearsDownWorkspaceWhenAfterCreateFails(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "workspaces")
	provider := &recordingProvider{root: root, executeErr: errors.New("boom")}
	manager, err := NewManager(workflow.Config{
		Workspace: workflow.WorkspaceConfig{Root: root},
		Hooks:     workflow.HooksConfig{AfterCreate: "echo create"},
	}, provider)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	_, err = manager.CreateForIssue(context.Background(), "octo/widgets#123")
	if err == nil {
		t.Fatal("CreateForIssue() error = nil, want error")
	}
	if len(provider.teardownCalls) != 1 || provider.teardownCalls[0] != "octo/widgets#123" {
		t.Fatalf("unexpected teardown calls: %#v", provider.teardownCalls)
	}
	if _, statErr := os.Stat(provider.WorkspacePath("octo/widgets#123")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected workspace to be removed, stat err = %v", statErr)
	}
}
