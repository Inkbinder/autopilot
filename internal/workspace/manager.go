package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Inkbinder/autopilot/internal/model"
	"github.com/Inkbinder/autopilot/internal/workflow"
)

type Manager struct {
	root            string
	workspaceConfig WorkspaceConfig
	hooks           workflow.HooksConfig
	provider        WorkspaceProvider
}

func NewManager(config workflow.Config, provider WorkspaceProvider) (*Manager, error) {
	workspaceConfig := config.Workspace
	workspaceConfig.Provider = normalizeProviderName(workspaceConfig.Provider)
	root := workspaceConfig.Root
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("workspace.root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	workspaceConfig.Root = absRoot
	if provider == nil {
		provider, err = NewProvider(workspaceConfig)
		if err != nil {
			return nil, err
		}
	}
	return &Manager{root: absRoot, workspaceConfig: workspaceConfig, hooks: config.Hooks, provider: provider}, nil
}

func (manager *Manager) Root() string {
	return manager.root
}

func (manager *Manager) CreateForIssue(ctx context.Context, issueIdentifier string) (model.Workspace, error) {
	workspacePath, pathKnown := manager.workspacePath(issueIdentifier)
	createdNow := false
	if pathKnown {
		if err := manager.ValidateWorkspacePath(workspacePath); err != nil {
			return model.Workspace{}, err
		}
		var err error
		createdNow, err = manager.workspaceNeedsCreation(workspacePath)
		if err != nil {
			return model.Workspace{}, err
		}
	}

	workspace := model.Workspace{WorkspaceKey: SanitizeWorkspaceKey(issueIdentifier)}
	path, err := manager.provider.Setup(issueIdentifier, manager.workspaceConfig)
	if err != nil {
		return model.Workspace{}, err
	}
	workspace.Path = path
	if err := manager.ValidateWorkspacePath(workspace.Path); err != nil {
		return model.Workspace{}, err
	}
	stat, err := os.Stat(workspace.Path)
	if err != nil {
		return model.Workspace{}, err
	}
	if !stat.IsDir() {
		return model.Workspace{}, fmt.Errorf("workspace path exists and is not a directory: %s", workspace.Path)
	}

	workspace.CreatedNow = createdNow
	if workspace.CreatedNow && manager.hooks.AfterCreate != "" {
		if _, err := manager.runHook(ctx, "after_create", workspace.Path, manager.hooks.AfterCreate); err != nil {
			_ = manager.provider.Teardown(issueIdentifier)
			return model.Workspace{}, err
		}
	}
	return workspace, nil
}

func (manager *Manager) PrepareForRun(ctx context.Context, workspace model.Workspace) error {
	if err := manager.ValidateWorkspacePath(workspace.Path); err != nil {
		return err
	}
	for _, transient := range []string{"tmp", ".elixir_ls"} {
		path := filepath.Join(workspace.Path, transient)
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	if manager.hooks.BeforeRun == "" {
		return nil
	}
	_, err := manager.runHook(ctx, "before_run", workspace.Path, manager.hooks.BeforeRun)
	return err
}

func (manager *Manager) RunAfterRunHook(ctx context.Context, workspacePath string) error {
	if manager.hooks.AfterRun == "" {
		return nil
	}
	_, err := manager.runHook(ctx, "after_run", workspacePath, manager.hooks.AfterRun)
	return err
}

func (manager *Manager) RemoveForIssue(ctx context.Context, issueIdentifier string) error {
	workspacePath, _ := manager.workspacePath(issueIdentifier)
	if err := manager.ValidateWorkspacePath(workspacePath); err != nil {
		return err
	}
	stat, err := os.Stat(workspacePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !stat.IsDir() {
		return fmt.Errorf("workspace path exists and is not a directory: %s", workspacePath)
	}
	if manager.hooks.BeforeRemove != "" {
		_, _ = manager.runHook(ctx, "before_remove", workspacePath, manager.hooks.BeforeRemove)
	}
	return manager.provider.Teardown(issueIdentifier)
}

func (manager *Manager) ValidateWorkspacePath(workspacePath string) error {
	return validateWorkspacePath(manager.root, workspacePath)
}

func SanitizeWorkspaceKey(issueIdentifier string) string {
	sanitized := workspaceKeyPattern.ReplaceAllString(issueIdentifier, "_")
	if strings.TrimSpace(sanitized) == "" {
		return "workspace"
	}
	return sanitized
}

func (manager *Manager) runHook(parent context.Context, hookName string, workingDir string, script string) (string, error) {
	timeout := manager.hooks.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	output, err := manager.executeCommand(ctx, "sh", []string{"-lc", script}, workingDir)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return output, fmt.Errorf("%s timed out after %s", hookName, timeout)
		}
		return output, fmt.Errorf("%s failed: %w", hookName, err)
	}
	return output, nil
}

func (manager *Manager) executeCommand(ctx context.Context, command string, args []string, workingDir string) (string, error) {
	if provider, ok := manager.provider.(contextAwareWorkspaceProvider); ok {
		return provider.ExecuteContext(ctx, command, args, workingDir)
	}

	type result struct {
		output string
		err    error
	}
	resultCh := make(chan result, 1)
	go func() {
		output, err := manager.provider.Execute(command, args, workingDir)
		resultCh <- result{output: output, err: err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-resultCh:
		return result.output, result.err
	}
}

func (manager *Manager) workspacePath(issueIdentifier string) (string, bool) {
	if provider, ok := manager.provider.(workspacePathProvider); ok {
		return provider.WorkspacePath(issueIdentifier), true
	}
	return filepath.Join(manager.root, SanitizeWorkspaceKey(issueIdentifier)), false
}

func (manager *Manager) workspaceNeedsCreation(workspacePath string) (bool, error) {
	stat, err := os.Stat(workspacePath)
	if err == nil {
		if !stat.IsDir() {
			return false, fmt.Errorf("workspace path exists and is not a directory: %s", workspacePath)
		}
		return false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return true, nil
}
