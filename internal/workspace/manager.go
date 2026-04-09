package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Inkbinder/autopilot/internal/model"
	"github.com/Inkbinder/autopilot/internal/workflow"
)

var workspaceKeyPattern = regexp.MustCompile(`[^A-Za-z0-9._-]`)

type ScriptRunner interface {
	Run(ctx context.Context, workingDir string, script string) (ScriptResult, error)
}

type ScriptResult struct {
	Stdout string
	Stderr string
}

type Manager struct {
	root   string
	hooks  workflow.HooksConfig
	runner ScriptRunner
}

func NewManager(config workflow.Config, runner ScriptRunner) (*Manager, error) {
	root := config.Workspace.Root
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("workspace.root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if runner == nil {
		runner = execScriptRunner{}
	}
	return &Manager{root: absRoot, hooks: config.Hooks, runner: runner}, nil
}

func (manager *Manager) Root() string {
	return manager.root
}

func (manager *Manager) CreateForIssue(ctx context.Context, issueIdentifier string) (model.Workspace, error) {
	if err := os.MkdirAll(manager.root, 0o755); err != nil {
		return model.Workspace{}, err
	}
	workspace := model.Workspace{
		WorkspaceKey: SanitizeWorkspaceKey(issueIdentifier),
	}
	workspace.Path = filepath.Join(manager.root, workspace.WorkspaceKey)
	if err := manager.ValidateWorkspacePath(workspace.Path); err != nil {
		return model.Workspace{}, err
	}
	stat, err := os.Stat(workspace.Path)
	if err == nil {
		if !stat.IsDir() {
			return model.Workspace{}, fmt.Errorf("workspace path exists and is not a directory: %s", workspace.Path)
		}
		return workspace, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return model.Workspace{}, err
	}
	if err := os.MkdirAll(workspace.Path, 0o755); err != nil {
		return model.Workspace{}, err
	}
	workspace.CreatedNow = true
	if manager.hooks.AfterCreate != "" {
		if _, err := manager.runHook(ctx, "after_create", workspace.Path, manager.hooks.AfterCreate); err != nil {
			_ = os.RemoveAll(workspace.Path)
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
	workspacePath := filepath.Join(manager.root, SanitizeWorkspaceKey(issueIdentifier))
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
	return os.RemoveAll(workspacePath)
}

func (manager *Manager) ValidateWorkspacePath(workspacePath string) error {
	absWorkspacePath, err := filepath.Abs(workspacePath)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(manager.root, absWorkspacePath)
	if err != nil {
		return err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("workspace path is outside root: %s", workspacePath)
	}
	return nil
}

func SanitizeWorkspaceKey(issueIdentifier string) string {
	sanitized := workspaceKeyPattern.ReplaceAllString(issueIdentifier, "_")
	if strings.TrimSpace(sanitized) == "" {
		return "workspace"
	}
	return sanitized
}

func (manager *Manager) runHook(parent context.Context, hookName string, workingDir string, script string) (ScriptResult, error) {
	timeout := manager.hooks.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	result, err := manager.runner.Run(ctx, workingDir, script)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return result, fmt.Errorf("%s timed out after %s", hookName, timeout)
		}
		return result, fmt.Errorf("%s failed: %w", hookName, err)
	}
	return result, nil
}

type execScriptRunner struct{}

func (execScriptRunner) Run(ctx context.Context, workingDir string, script string) (ScriptResult, error) {
	command := exec.CommandContext(ctx, "sh", "-lc", script)
	command.Dir = workingDir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return ScriptResult{Stdout: stdout.String(), Stderr: stderr.String()}, err
}
