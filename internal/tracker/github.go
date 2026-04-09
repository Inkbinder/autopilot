package tracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Inkbinder/autopilot/internal/model"
	"github.com/Inkbinder/autopilot/internal/workflow"
)

const defaultPageSize = 50

type GitHubClient struct {
	endpoint       string
	token          string
	owner          string
	repositoryName string
	activeStates   []string
	dispatchLabels []string
	excludedLabels []string
	httpClient     *http.Client
	pageSize       int
}

func NewGitHubClient(config workflow.Config, httpClient *http.Client) (*GitHubClient, error) {
	if strings.TrimSpace(config.Tracker.APIKey) == "" {
		return nil, wrap(ErrMissingTrackerAPIKey, fmt.Errorf("tracker.api_key is required"))
	}
	owner, repository, ok := strings.Cut(strings.TrimSpace(config.Tracker.Repository), "/")
	if !ok || strings.TrimSpace(owner) == "" || strings.TrimSpace(repository) == "" {
		return nil, wrap(ErrMissingRepository, fmt.Errorf("tracker.repository must be <owner>/<repo>"))
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	} else if httpClient.Timeout == 0 {
		clone := *httpClient
		clone.Timeout = 30 * time.Second
		httpClient = &clone
	}
	return &GitHubClient{
		endpoint:       strings.TrimSpace(config.Tracker.Endpoint),
		token:          strings.TrimSpace(config.Tracker.APIKey),
		owner:          strings.TrimSpace(owner),
		repositoryName: strings.TrimSpace(repository),
		activeStates:   cloneStrings(config.Tracker.ActiveStates),
		dispatchLabels: normalizeLower(config.Tracker.DispatchLabels),
		excludedLabels: normalizeLower(config.Tracker.ExcludedLabels),
		httpClient:     httpClient,
		pageSize:       defaultPageSize,
	}, nil
}

func (client *GitHubClient) FetchCandidateIssues(ctx context.Context) ([]model.Issue, error) {
	issues, err := client.FetchIssuesByStates(ctx, client.activeStates)
	if err != nil {
		return nil, err
	}
	filtered := make([]model.Issue, 0, len(issues))
	for _, issue := range issues {
		if matchesLabels(issue.Labels, client.dispatchLabels, client.excludedLabels) {
			filtered = append(filtered, issue)
		}
	}
	return filtered, nil
}

func (client *GitHubClient) FetchIssuesByStates(ctx context.Context, stateNames []string) ([]model.Issue, error) {
	githubStates := gitHubStateQueries(stateNames)
	if len(githubStates) == 0 {
		return []model.Issue{}, nil
	}
	issuesByID := map[string]model.Issue{}
	for _, stateQuery := range githubStates {
		searchQuery := fmt.Sprintf("repo:%s/%s is:issue sort:created-asc %s", client.owner, client.repositoryName, stateQuery)
		cursor := ""
		for {
			response := repositorySearchResponse{}
			if err := client.graphQL(ctx, repositorySearchQuery, map[string]any{
				"query": searchQuery,
				"first": client.pageSize,
				"after": nullableString(cursor),
			}, &response); err != nil {
				return nil, err
			}
			for _, node := range response.Search.Nodes {
				if node.Typename != "Issue" {
					continue
				}
				issue := normalizeIssueNode(client.owner, client.repositoryName, node)
				issuesByID[issue.ID] = issue
			}
			if !response.Search.PageInfo.HasNextPage {
				break
			}
			if strings.TrimSpace(response.Search.PageInfo.EndCursor) == "" {
				return nil, wrap(ErrGitHubMissingEndCursor, fmt.Errorf("missing endCursor with hasNextPage=true"))
			}
			cursor = response.Search.PageInfo.EndCursor
		}
	}
	issues := make([]model.Issue, 0, len(issuesByID))
	for _, issue := range issuesByID {
		issues = append(issues, issue)
	}
	sort.SliceStable(issues, func(left int, right int) bool {
		if issues[left].CreatedAt == nil || issues[right].CreatedAt == nil {
			return issues[left].Identifier < issues[right].Identifier
		}
		if issues[left].CreatedAt.Equal(*issues[right].CreatedAt) {
			return issues[left].Identifier < issues[right].Identifier
		}
		return issues[left].CreatedAt.Before(*issues[right].CreatedAt)
	})
	return issues, nil
}

func (client *GitHubClient) FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) ([]model.Issue, error) {
	if len(issueIDs) == 0 {
		return []model.Issue{}, nil
	}
	response := issueStatesResponse{}
	if err := client.graphQL(ctx, issueStatesQuery, map[string]any{"ids": issueIDs}, &response); err != nil {
		return nil, err
	}
	issues := make([]model.Issue, 0, len(response.Nodes))
	for _, node := range response.Nodes {
		if node == nil || node.Typename != "Issue" {
			continue
		}
		issues = append(issues, normalizeIssueNode(client.owner, client.repositoryName, *node))
	}
	return issues, nil
}

func (client *GitHubClient) graphQL(ctx context.Context, query string, variables map[string]any, out any) error {
	payload := map[string]any{"query": query, "variables": variables}
	body, err := json.Marshal(payload)
	if err != nil {
		return wrap(ErrGitHubUnknownPayload, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, bytes.NewReader(body))
	if err != nil {
		return wrap(ErrGitHubAPIRequest, err)
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/vnd.github+json")

	response, err := client.httpClient.Do(request)
	if err != nil {
		return wrap(ErrGitHubAPIRequest, err)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return wrap(ErrGitHubAPIRequest, err)
	}
	if response.StatusCode != http.StatusOK {
		return wrap(ErrGitHubAPIStatus, fmt.Errorf("status=%d body=%s", response.StatusCode, truncate(string(responseBody), 512)))
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return wrap(ErrGitHubUnknownPayload, err)
	}
	if len(envelope.Errors) > 0 {
		messages := make([]string, 0, len(envelope.Errors))
		for _, graphqlErr := range envelope.Errors {
			messages = append(messages, graphqlErr.Message)
		}
		return wrap(ErrGitHubGraphQLErrors, fmt.Errorf("%s", strings.Join(messages, "; ")))
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return wrap(ErrGitHubUnknownPayload, fmt.Errorf("missing data payload"))
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return wrap(ErrGitHubUnknownPayload, err)
	}
	return nil
}

type repositorySearchResponse struct {
	Search struct {
		Nodes    []issueNode `json:"nodes"`
		PageInfo struct {
			HasNextPage bool   `json:"hasNextPage"`
			EndCursor   string `json:"endCursor"`
		} `json:"pageInfo"`
	} `json:"search"`
}

type issueStatesResponse struct {
	Nodes []*issueNode `json:"nodes"`
}

type issueNode struct {
	Typename   string `json:"__typename"`
	ID         string `json:"id"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	State      string `json:"state"`
	URL        string `json:"url"`
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	BlockedBy struct {
		Nodes []*issueDependencyNode `json:"nodes"`
	} `json:"blockedBy"`
}

type issueDependencyNode struct {
	ID         string `json:"id"`
	Number     int    `json:"number"`
	State      string `json:"state"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
}

func normalizeIssueNode(defaultOwner string, defaultRepository string, node issueNode) model.Issue {
	owner := defaultOwner
	repository := defaultRepository
	if strings.TrimSpace(node.Repository.Owner.Login) != "" {
		owner = strings.TrimSpace(node.Repository.Owner.Login)
	}
	if strings.TrimSpace(node.Repository.Name) != "" {
		repository = strings.TrimSpace(node.Repository.Name)
	}
	labels := make([]string, 0, len(node.Labels.Nodes))
	for _, label := range node.Labels.Nodes {
		trimmed := strings.TrimSpace(strings.ToLower(label.Name))
		if trimmed != "" {
			labels = append(labels, trimmed)
		}
	}
	issue := model.Issue{
		ID:         node.ID,
		Identifier: fmt.Sprintf("%s/%s#%d", owner, repository, node.Number),
		Title:      node.Title,
		State:      strings.Title(strings.ToLower(node.State)),
		Labels:     labels,
		BlockedBy:  normalizeBlockedBy(owner, repository, node.BlockedBy.Nodes),
	}
	if strings.TrimSpace(node.Body) != "" {
		body := node.Body
		issue.Description = &body
	}
	if strings.TrimSpace(node.URL) != "" {
		url := node.URL
		issue.URL = &url
	}
	if createdAt, err := time.Parse(time.RFC3339, strings.TrimSpace(node.CreatedAt)); err == nil {
		issue.CreatedAt = &createdAt
	}
	if updatedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(node.UpdatedAt)); err == nil {
		issue.UpdatedAt = &updatedAt
	}
	return issue
}

func normalizeBlockedBy(defaultOwner string, defaultRepository string, nodes []*issueDependencyNode) []model.BlockerRef {
	blockers := make([]model.BlockerRef, 0, len(nodes))
	for _, node := range nodes {
		if node == nil {
			continue
		}
		blocker := model.BlockerRef{}
		if trimmed := strings.TrimSpace(node.ID); trimmed != "" {
			id := trimmed
			blocker.ID = &id
		}
		owner := defaultOwner
		repository := defaultRepository
		if trimmed := strings.TrimSpace(node.Repository.Owner.Login); trimmed != "" {
			owner = trimmed
		}
		if trimmed := strings.TrimSpace(node.Repository.Name); trimmed != "" {
			repository = trimmed
		}
		if node.Number > 0 {
			identifier := fmt.Sprintf("%s/%s#%d", owner, repository, node.Number)
			blocker.Identifier = &identifier
		}
		if trimmed := strings.TrimSpace(node.State); trimmed != "" {
			state := strings.Title(strings.ToLower(trimmed))
			blocker.State = &state
		}
		if blocker.ID == nil && blocker.Identifier == nil && blocker.State == nil {
			continue
		}
		blockers = append(blockers, blocker)
	}
	return blockers
}

func matchesLabels(issueLabels []string, dispatchLabels []string, excludedLabels []string) bool {
	labelSet := map[string]struct{}{}
	for _, label := range issueLabels {
		labelSet[strings.ToLower(strings.TrimSpace(label))] = struct{}{}
	}
	for _, excluded := range excludedLabels {
		if _, ok := labelSet[strings.ToLower(strings.TrimSpace(excluded))]; ok {
			return false
		}
	}
	if len(dispatchLabels) == 0 {
		return true
	}
	for _, dispatch := range dispatchLabels {
		if _, ok := labelSet[strings.ToLower(strings.TrimSpace(dispatch))]; ok {
			return true
		}
	}
	return false
}

func gitHubStateQueries(states []string) []string {
	queries := []string{}
	seen := map[string]struct{}{}
	for _, state := range states {
		normalized := strings.ToLower(strings.TrimSpace(state))
		var stateQuery string
		switch normalized {
		case "open":
			stateQuery = "state:open"
		case "closed":
			stateQuery = "state:closed"
		default:
			continue
		}
		if _, ok := seen[stateQuery]; ok {
			continue
		}
		seen[stateQuery] = struct{}{}
		queries = append(queries, stateQuery)
	}
	return queries
}

func normalizeLower(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(strings.ToLower(value))
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func cloneStrings(values []string) []string {
	clone := make([]string, len(values))
	copy(clone, values)
	return clone
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func truncate(value string, maxLength int) string {
	if len(value) <= maxLength {
		return value
	}
	if maxLength <= 3 {
		return value[:maxLength]
	}
	return value[:maxLength-3] + "..."
}

const repositorySearchQuery = `query SearchIssues($query: String!, $first: Int!, $after: String) {
  search(type: ISSUE, query: $query, first: $first, after: $after) {
    pageInfo {
      hasNextPage
      endCursor
    }
    nodes {
      __typename
      ... on Issue {
        id
        number
        title
        body
        state
        url
        createdAt
        updatedAt
        repository {
          name
          owner {
            login
          }
        }
				labels(first: 50) {
          nodes {
            name
          }
        }
				blockedBy(first: 50) {
					nodes {
						id
						number
						state
						repository {
							name
							owner {
								login
							}
						}
					}
				}
      }
    }
  }
}`

const issueStatesQuery = `query IssueStatesByIDs($ids: [ID!]!) {
  nodes(ids: $ids) {
    __typename
    ... on Issue {
      id
      number
      title
      body
      state
      url
      createdAt
      updatedAt
      repository {
        name
        owner {
          login
        }
      }
			labels(first: 50) {
        nodes {
          name
        }
      }
			blockedBy(first: 50) {
				nodes {
					id
					number
					state
					repository {
						name
						owner {
							login
						}
					}
				}
			}
    }
  }
}`
