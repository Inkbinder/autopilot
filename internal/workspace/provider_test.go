package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Inkbinder/autopilot/internal/workflow"
)

func TestLocalProviderSetupExecuteAndTeardown(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "workspaces")
	provider, err := NewLocalProvider(workflow.WorkspaceConfig{Root: root})
	if err != nil {
		t.Fatalf("NewLocalProvider() error = %v", err)
	}

	workspacePath, err := provider.Setup("octo/widgets#123", workflow.WorkspaceConfig{Root: root})
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	if filepath.Base(workspacePath) != "octo_widgets_123" {
		t.Fatalf("workspace path = %q", workspacePath)
	}

	output, err := provider.Execute("sh", []string{"-lc", "printf hello"}, workspacePath)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if output != "hello" {
		t.Fatalf("Execute() output = %q, want hello", output)
	}

	if err := provider.Teardown("octo/widgets#123"); err != nil {
		t.Fatalf("Teardown() error = %v", err)
	}
	if _, err := os.Stat(workspacePath); !os.IsNotExist(err) {
		t.Fatalf("expected workspace teardown, stat err = %v", err)
	}
}

func TestNewProviderRejectsUnsupportedProvider(t *testing.T) {
	t.Parallel()
	_, err := NewProvider(workflow.WorkspaceConfig{Provider: "docker", Root: t.TempDir()})
	if err == nil {
		t.Fatal("NewProvider() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "unsupported workspace.provider") {
		t.Fatalf("NewProvider() error = %v", err)
	}
}

func TestLocalProviderTeardownMissingWorkspaceIsNoop(t *testing.T) {
	t.Parallel()
	provider, err := NewLocalProvider(workflow.WorkspaceConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("NewLocalProvider() error = %v", err)
	}
	if err := provider.Teardown("octo/widgets#404"); err != nil {
		t.Fatalf("Teardown() error = %v", err)
	}
}
