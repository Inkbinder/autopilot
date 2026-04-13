package workspace

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Inkbinder/autopilot/internal/runstate"
	"github.com/Inkbinder/autopilot/internal/workflow"
)

var workspaceKeyPattern = regexp.MustCompile(`[^A-Za-z0-9._-]`)

type WorkspaceConfig = workflow.WorkspaceConfig

type ProviderOptions struct {
	AuditWriter runstate.Writer
	Logger      *slog.Logger
}

type WorkspaceProvider interface {
	Setup(issueID string, config WorkspaceConfig) (workspacePath string, err error)
	Execute(command string, args []string, dir string) (output string, err error)
	Teardown(issueID string) error
}

type contextAwareWorkspaceProvider interface {
	ExecuteContext(ctx context.Context, command string, args []string, dir string) (output string, err error)
}

type workspacePathProvider interface {
	WorkspacePath(issueID string) string
}

type LocalProvider struct {
	root        string
	auditWriter runstate.Writer
	logger      *slog.Logger
}

var _ WorkspaceProvider = (*LocalProvider)(nil)
var _ contextAwareWorkspaceProvider = (*LocalProvider)(nil)
var _ workspacePathProvider = (*LocalProvider)(nil)

func NewProvider(config WorkspaceConfig) (WorkspaceProvider, error) {
	return NewProviderWithOptions(config, ProviderOptions{})
}

func NewProviderWithOptions(config WorkspaceConfig, options ProviderOptions) (WorkspaceProvider, error) {
	switch normalizeProviderName(config.Provider) {
	case "local":
		return NewLocalProviderWithOptions(config, options)
	default:
		return nil, fmt.Errorf("unsupported workspace.provider: %s", config.Provider)
	}
}

func NewLocalProvider(config WorkspaceConfig) (*LocalProvider, error) {
	return NewLocalProviderWithOptions(config, ProviderOptions{})
}

func NewLocalProviderWithOptions(config WorkspaceConfig, options ProviderOptions) (*LocalProvider, error) {
	root := strings.TrimSpace(config.Root)
	if root == "" {
		return nil, fmt.Errorf("workspace.root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &LocalProvider{root: absRoot, auditWriter: options.AuditWriter, logger: options.Logger}, nil
}

func (provider *LocalProvider) Setup(issueID string, _ WorkspaceConfig) (string, error) {
	if err := os.MkdirAll(provider.root, 0o755); err != nil {
		return "", err
	}
	workspacePath := provider.WorkspacePath(issueID)
	if err := validateWorkspacePath(provider.root, workspacePath); err != nil {
		return "", err
	}
	stat, err := os.Stat(workspacePath)
	if err == nil {
		if !stat.IsDir() {
			return "", fmt.Errorf("workspace path exists and is not a directory: %s", workspacePath)
		}
		return workspacePath, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		return "", err
	}
	return workspacePath, nil
}

func (provider *LocalProvider) Execute(command string, args []string, dir string) (string, error) {
	return provider.ExecuteContext(context.Background(), command, args, dir)
}

func (provider *LocalProvider) ExecuteContext(ctx context.Context, command string, args []string, dir string) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("workspace command is required")
	}
	process := exec.CommandContext(ctx, command, args...)
	process.Dir = dir
	output, err := process.CombinedOutput()
	if auditErr := runstate.RecordAuditEvent(ctx, provider.auditWriter, "workspace_exec", map[string]any{
		"command": command,
		"args":    args,
		"dir":     dir,
		"output":  string(output),
		"success": err == nil,
		"error":   errorString(err),
	}); auditErr != nil {
		provider.logAuditFailure(ctx, auditErr)
	}
	return string(output), err
}

func (provider *LocalProvider) Teardown(issueID string) error {
	workspacePath := provider.WorkspacePath(issueID)
	if err := validateWorkspacePath(provider.root, workspacePath); err != nil {
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
	return os.RemoveAll(workspacePath)
}

func (provider *LocalProvider) WorkspacePath(issueID string) string {
	return filepath.Join(provider.root, SanitizeWorkspaceKey(issueID))
}

func normalizeProviderName(value string) string {
	provider := strings.ToLower(strings.TrimSpace(value))
	if provider == "" {
		return "local"
	}
	return provider
}

func validateWorkspacePath(root string, workspacePath string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	absWorkspacePath, err := filepath.Abs(workspacePath)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(absRoot, absWorkspacePath)
	if err != nil {
		return err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("workspace path is outside root: %s", workspacePath)
	}
	return nil
}

func (provider *LocalProvider) logAuditFailure(ctx context.Context, err error) {
	if provider.logger == nil {
		return
	}
	metadata, _ := runstate.MetadataFromContext(ctx)
	provider.logger.With(slog.String("repo", strings.TrimSpace(metadata.Repo)), slog.String("issue_id", strings.TrimSpace(metadata.IssueID))).Warn("workspace audit write failed", slog.Any("error", err))
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
