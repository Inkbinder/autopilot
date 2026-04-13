package orchestrator

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/Inkbinder/autopilot/internal/copilot"
	"github.com/Inkbinder/autopilot/internal/model"
	"github.com/Inkbinder/autopilot/internal/tracker"
	"github.com/Inkbinder/autopilot/internal/workflow"
	workspacepkg "github.com/Inkbinder/autopilot/internal/workspace"
)

type IssueTracker interface {
	FetchCandidateIssues(ctx context.Context) ([]model.Issue, error)
	FetchIssuesByStates(ctx context.Context, stateNames []string) ([]model.Issue, error)
	FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) ([]model.Issue, error)
}

type WorkspaceManager interface {
	CreateForIssue(ctx context.Context, issueIdentifier string) (model.Workspace, error)
	PrepareForRun(ctx context.Context, workspace model.Workspace) error
	RunAfterRunHook(ctx context.Context, workspacePath string) error
	RemoveForIssue(ctx context.Context, issueIdentifier string) error
	Root() string
	ValidateWorkspacePath(workspacePath string) error
}

type DependencyBuilder interface {
	Build(config workflow.Config) (IssueTracker, WorkspaceManager, copilot.Client, error)
}

type DefaultDependencyBuilder struct {
	HTTPClient *http.Client
}

func (builder DefaultDependencyBuilder) Build(config workflow.Config) (IssueTracker, WorkspaceManager, copilot.Client, error) {
	trackerClient, err := tracker.NewClient(config, builder.HTTPClient)
	if err != nil {
		return nil, nil, nil, err
	}
	workspaceProvider, err := workspacepkg.NewProvider(config.Workspace)
	if err != nil {
		return nil, nil, nil, err
	}
	workspaceManager, err := workspacepkg.NewManager(config, workspaceProvider)
	if err != nil {
		return nil, nil, nil, err
	}
	copilotClient, err := copilot.NewClient(config)
	if err != nil {
		return nil, nil, nil, err
	}
	return trackerClient, workspaceManager, copilotClient, nil
}

type Options struct {
	Logger       *slog.Logger
	Builder      DependencyBuilder
	PortOverride *int
}
