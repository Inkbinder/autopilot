package tracker

import (
	"context"
	"fmt"
	"net/http"

	"autopilot/internal/model"
	"autopilot/internal/workflow"
)

type Client interface {
	FetchCandidateIssues(ctx context.Context) ([]model.Issue, error)
	FetchIssuesByStates(ctx context.Context, stateNames []string) ([]model.Issue, error)
	FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) ([]model.Issue, error)
}

func NewClient(config workflow.Config, httpClient *http.Client) (Client, error) {
	switch config.Tracker.Kind {
	case "github":
		return NewGitHubClient(config, httpClient)
	default:
		return nil, wrap(ErrUnsupportedTrackerKind, fmt.Errorf("tracker.kind=%s", config.Tracker.Kind))
	}
}