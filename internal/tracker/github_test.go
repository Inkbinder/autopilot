package tracker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Inkbinder/autopilot/internal/workflow"
)

func TestGitHubClientFetchCandidateIssuesFiltersLabelsAndPaginates(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}
		var payload struct {
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("json.Unmarshal() error = %v", err)
		}
		if !strings.Contains(string(body), "blockedBy(first: 50)") {
			t.Fatalf("expected blockedBy selection in query, body = %s", string(body))
		}
		after, _ := payload.Variables["after"].(string)
		response := map[string]any{
			"data": map[string]any{
				"search": map[string]any{
					"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
					"nodes":    []map[string]any{},
				},
			},
		}
		if after == "" {
			response["data"].(map[string]any)["search"] = map[string]any{
				"pageInfo": map[string]any{"hasNextPage": true, "endCursor": "cursor-1"},
				"nodes": []map[string]any{
					issueNodeFixture("1", 101, "Ready issue", []string{"Autopilot:Ready"}, "OPEN", blockerNodeFixture("blocker-1", 77, "OPEN")),
					issueNodeFixture("2", 102, "Blocked issue", []string{"Autopilot:Ready", "autopilot:blocked"}, "OPEN"),
				},
			}
		} else {
			response["data"].(map[string]any)["search"] = map[string]any{
				"pageInfo": map[string]any{"hasNextPage": false, "endCursor": "cursor-2"},
				"nodes": []map[string]any{
					issueNodeFixture("3", 103, "Second ready issue", []string{"autopilot:ready"}, "OPEN"),
				},
			}
		}
		writeTrackerJSON(t, writer, response)
	}))
	defer server.Close()

	client, err := NewGitHubClient(workflow.Config{Tracker: workflow.TrackerConfig{
		Kind:           "github",
		Endpoint:       server.URL,
		APIKey:         "token",
		Repository:     "octo/widgets",
		ActiveStates:   []string{"Open"},
		DispatchLabels: []string{"autopilot:ready"},
		ExcludedLabels: []string{"autopilot:blocked"},
	}}, server.Client())
	if err != nil {
		t.Fatalf("NewGitHubClient() error = %v", err)
	}
	issues, err := client.FetchCandidateIssues(context.Background())
	if err != nil {
		t.Fatalf("FetchCandidateIssues() error = %v", err)
	}
	if got := len(issues); got != 2 {
		t.Fatalf("candidate issues = %d, want 2", got)
	}
	if issues[0].Identifier != "octo/widgets#101" || issues[1].Identifier != "octo/widgets#103" {
		t.Fatalf("unexpected issue identifiers: %#v", issues)
	}
	if issues[0].Labels[0] != "autopilot:ready" {
		t.Fatalf("labels should normalize to lowercase, got %#v", issues[0].Labels)
	}
	if len(issues[0].BlockedBy) != 1 {
		t.Fatalf("blocked_by length = %d, want 1", len(issues[0].BlockedBy))
	}
	if issues[0].BlockedBy[0].Identifier == nil || *issues[0].BlockedBy[0].Identifier != "octo/widgets#77" {
		t.Fatalf("unexpected blocker identifier: %#v", issues[0].BlockedBy)
	}
	if issues[0].BlockedBy[0].State == nil || *issues[0].BlockedBy[0].State != "Open" {
		t.Fatalf("unexpected blocker state: %#v", issues[0].BlockedBy)
	}
	if calls.Load() != 2 {
		t.Fatalf("graphql call count = %d, want 2", calls.Load())
	}
}

func TestGitHubClientFetchIssuesByStatesEmptySkipsAPICall(t *testing.T) {
	t.Parallel()
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer server.Close()
	client, err := NewGitHubClient(workflow.Config{Tracker: workflow.TrackerConfig{
		Kind: "github", Endpoint: server.URL, APIKey: "token", Repository: "octo/widgets",
	}}, server.Client())
	if err != nil {
		t.Fatalf("NewGitHubClient() error = %v", err)
	}
	issues, err := client.FetchIssuesByStates(context.Background(), nil)
	if err != nil {
		t.Fatalf("FetchIssuesByStates() error = %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("issues length = %d, want 0", len(issues))
	}
	if called {
		t.Fatal("expected no API call when states are empty")
	}
}

func TestGitHubClientFetchIssueStatesByIDsUsesNodeQuery(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}
		if !strings.Contains(string(body), "query IssueStatesByIDs($ids: [ID!]!)") {
			t.Fatalf("expected node ID query, body = %s", string(body))
		}
		if !strings.Contains(string(body), "blockedBy(first: 50)") {
			t.Fatalf("expected blockedBy selection in query, body = %s", string(body))
		}
		writeTrackerJSON(t, writer, map[string]any{
			"data": map[string]any{
				"nodes": []map[string]any{
					issueNodeFixture("node-1", 41, "One", []string{"Autopilot:Ready"}, "CLOSED", blockerNodeFixture("blocker-2", 7, "OPEN")),
					{"__typename": "PullRequest", "id": "pr-1"},
				},
			},
		})
	}))
	defer server.Close()
	client, err := NewGitHubClient(workflow.Config{Tracker: workflow.TrackerConfig{
		Kind: "github", Endpoint: server.URL, APIKey: "token", Repository: "octo/widgets",
	}}, server.Client())
	if err != nil {
		t.Fatalf("NewGitHubClient() error = %v", err)
	}
	issues, err := client.FetchIssueStatesByIDs(context.Background(), []string{"node-1", "pr-1"})
	if err != nil {
		t.Fatalf("FetchIssueStatesByIDs() error = %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("issues length = %d, want 1", len(issues))
	}
	if issues[0].State != "Closed" {
		t.Fatalf("state = %q, want Closed", issues[0].State)
	}
	if issues[0].UpdatedAt == nil || issues[0].CreatedAt == nil {
		t.Fatal("expected timestamps to be parsed")
	}
	if len(issues[0].BlockedBy) != 1 {
		t.Fatalf("blocked_by length = %d, want 1", len(issues[0].BlockedBy))
	}
}

func issueNodeFixture(id string, number int, title string, labels []string, state string, blockers ...map[string]any) map[string]any {
	labelNodes := make([]map[string]any, 0, len(labels))
	for _, label := range labels {
		labelNodes = append(labelNodes, map[string]any{"name": label})
	}
	return map[string]any{
		"__typename": "Issue",
		"id":         id,
		"number":     number,
		"title":      title,
		"body":       "body",
		"state":      state,
		"url":        "https://example.test/issue",
		"createdAt":  time.Date(2026, 1, number%28+1, 10, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"updatedAt":  time.Date(2026, 1, number%28+1, 11, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"repository": map[string]any{
			"name":  "widgets",
			"owner": map[string]any{"login": "octo"},
		},
		"labels":    map[string]any{"nodes": labelNodes},
		"blockedBy": map[string]any{"nodes": blockers},
	}
}

func blockerNodeFixture(id string, number int, state string) map[string]any {
	return map[string]any{
		"id":     id,
		"number": number,
		"state":  state,
		"repository": map[string]any{
			"name":  "widgets",
			"owner": map[string]any{"login": "octo"},
		},
	}
}

func writeTrackerJSON(t *testing.T, writer http.ResponseWriter, payload any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(payload); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
}
