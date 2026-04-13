package orchestrator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Inkbinder/autopilot/internal/copilot"
	"github.com/Inkbinder/autopilot/internal/model"
	"github.com/Inkbinder/autopilot/internal/workflow"
)

type fakeBuilder struct {
	tracker   *fakeTracker
	workspace *fakeWorkspace
	copilot   *fakeCopilot
}

func (builder fakeBuilder) Build(_ workflow.Config) (IssueTracker, WorkspaceManager, copilot.Client, error) {
	return builder.tracker, builder.workspace, builder.copilot, nil
}

type fakeTracker struct {
	mu              sync.Mutex
	candidates      []model.Issue
	terminalIssues  []model.Issue
	statesByID      map[string]model.Issue
	stateFetchCount int
}

func (tracker *fakeTracker) FetchCandidateIssues(context.Context) ([]model.Issue, error) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return append([]model.Issue(nil), tracker.candidates...), nil
}

func (tracker *fakeTracker) FetchIssuesByStates(context.Context, []string) ([]model.Issue, error) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return append([]model.Issue(nil), tracker.terminalIssues...), nil
}

func (tracker *fakeTracker) FetchIssueStatesByIDs(_ context.Context, issueIDs []string) ([]model.Issue, error) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.stateFetchCount++
	issues := make([]model.Issue, 0, len(issueIDs))
	for _, issueID := range issueIDs {
		if issue, ok := tracker.statesByID[issueID]; ok {
			issues = append(issues, issue)
		}
	}
	return issues, nil
}

type fakeWorkspace struct {
	root    string
	removed []string
}

func (workspace *fakeWorkspace) CreateForIssue(_ context.Context, issueIdentifier string) (model.Workspace, error) {
	path := filepath.Join(workspace.root, strings.ReplaceAll(strings.ReplaceAll(issueIdentifier, "/", "_"), "#", "_"))
	if err := os.MkdirAll(path, 0o755); err != nil {
		return model.Workspace{}, err
	}
	return model.Workspace{Path: path, WorkspaceKey: filepath.Base(path), CreatedNow: true}, nil
}

func (workspace *fakeWorkspace) PrepareForRun(context.Context, model.Workspace) error { return nil }
func (workspace *fakeWorkspace) RunAfterRunHook(context.Context, string) error        { return nil }
func (workspace *fakeWorkspace) RemoveForIssue(_ context.Context, issueIdentifier string) error {
	workspace.removed = append(workspace.removed, issueIdentifier)
	return nil
}
func (workspace *fakeWorkspace) Root() string                       { return workspace.root }
func (workspace *fakeWorkspace) ValidateWorkspacePath(string) error { return nil }

type fakeCopilot struct {
	prompts     []string
	onEvent     copilot.EventHandler
	silent      bool
	rateLimited bool
}

func (client *fakeCopilot) StartSession(_ context.Context, request copilot.StartRequest) (copilot.Session, error) {
	client.onEvent = request.OnEvent
	if client.onEvent != nil {
		client.onEvent(copilot.Event{Event: "session_started", Timestamp: time.Now().UTC(), SessionID: "fake-session", Turn: 0})
	}
	return &fakeSession{client: client}, nil
}

type fakeSession struct {
	client *fakeCopilot
}

func (session *fakeSession) ID() string        { return "fake-session" }
func (session *fakeSession) Transport() string { return "acp_stdio" }
func (session *fakeSession) ProcessID() *int   { return nil }
func (session *fakeSession) RunPrompt(_ context.Context, prompt string, turn int) error {
	session.client.prompts = append(session.client.prompts, prompt)
	if session.client.onEvent != nil {
		if session.client.rateLimited {
			session.client.onEvent(copilot.Event{Event: "notification", Timestamp: time.Now().UTC(), SessionID: "fake-session", Turn: turn, Message: `{"sessionId":"fake-session","update":{"content":{"text":"Error: Sorry, you've hit a rate limit that restricts the number of Copilot model requests you can make within a specific time period. Please try again in 4 hours.","type":"text"},"sessionUpdate":"agent_message_chunk"}}`})
		}
		if !session.client.silent {
			session.client.onEvent(copilot.Event{Event: "notification", Timestamp: time.Now().UTC(), SessionID: "fake-session", Turn: turn, Message: "working"})
		}
		session.client.onEvent(copilot.Event{Event: "prompt_completed", Timestamp: time.Now().UTC(), SessionID: "fake-session", Turn: turn, Usage: &copilot.UsageTotals{InputTokens: 10 * turn, OutputTokens: 5 * turn, TotalTokens: 15 * turn}})
	}
	return nil
}
func (session *fakeSession) Close(context.Context) error { return nil }

func TestOrchestratorTickDispatchesAndQueuesContinuationRetry(t *testing.T) {
	t.Parallel()
	workflowPath := writeWorkflowFile(t)
	issue := model.Issue{ID: "1", Identifier: "octo/widgets#1", Title: "Task", State: "Open", Labels: []string{"autopilot:ready"}, BlockedBy: []model.BlockerRef{{Identifier: stringPtr("octo/widgets#2"), State: stringPtr("Closed")}}}
	fakeTracker := &fakeTracker{candidates: []model.Issue{issue}, statesByID: map[string]model.Issue{"1": {ID: "1", Identifier: issue.Identifier, Title: issue.Title, State: "Closed"}}}
	fakeWorkspace := &fakeWorkspace{root: filepath.Join(t.TempDir(), "workspaces")}
	fakeCopilot := &fakeCopilot{}
	orchestrator, err := New(workflowPath, Options{Builder: fakeBuilder{tracker: fakeTracker, workspace: fakeWorkspace, copilot: fakeCopilot}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	orchestrator.tick(context.Background())
	orchestrator.wg.Wait()

	orchestrator.mu.Lock()
	if len(orchestrator.state.retryAttempts) != 1 {
		orchestrator.mu.Unlock()
		t.Fatalf("retry queue length = %d, want 1", len(orchestrator.state.retryAttempts))
	}
	if len(fakeCopilot.prompts) != 1 {
		orchestrator.mu.Unlock()
		t.Fatalf("prompts = %d, want 1", len(fakeCopilot.prompts))
	}
	if orchestrator.state.copilotTotals.TotalTokens != 15 {
		orchestrator.mu.Unlock()
		t.Fatalf("total tokens = %d, want 15", orchestrator.state.copilotTotals.TotalTokens)
	}
	orchestrator.mu.Unlock()
	_ = orchestrator.shutdown(context.Background())
}

func TestOrchestratorTickSkipsIssueWithActiveBlockers(t *testing.T) {
	t.Parallel()
	workflowPath := writeWorkflowFile(t)
	issue := model.Issue{ID: "1", Identifier: "octo/widgets#1", Title: "Task", State: "Open", Labels: []string{"autopilot:ready"}, BlockedBy: []model.BlockerRef{{Identifier: stringPtr("octo/widgets#2"), State: stringPtr("Open")}}}
	fakeTracker := &fakeTracker{candidates: []model.Issue{issue}}
	fakeWorkspace := &fakeWorkspace{root: filepath.Join(t.TempDir(), "workspaces")}
	fakeCopilot := &fakeCopilot{}
	orchestrator, err := New(workflowPath, Options{Builder: fakeBuilder{tracker: fakeTracker, workspace: fakeWorkspace, copilot: fakeCopilot}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	orchestrator.tick(context.Background())
	orchestrator.wg.Wait()

	if len(fakeCopilot.prompts) != 0 {
		t.Fatalf("prompts = %d, want 0", len(fakeCopilot.prompts))
	}
	orchestrator.mu.Lock()
	if len(orchestrator.state.running) != 0 {
		orchestrator.mu.Unlock()
		t.Fatalf("running length = %d, want 0", len(orchestrator.state.running))
	}
	if len(orchestrator.state.retryAttempts) != 0 {
		orchestrator.mu.Unlock()
		t.Fatalf("retry queue length = %d, want 0", len(orchestrator.state.retryAttempts))
	}
	orchestrator.mu.Unlock()
	_ = orchestrator.shutdown(context.Background())
}

func TestHandleRetryDispatchesClaimedIssue(t *testing.T) {
	t.Parallel()
	workflowPath := writeWorkflowFile(t)
	issue := model.Issue{ID: "1", Identifier: "octo/widgets#1", Title: "Task", State: "Open", Labels: []string{"autopilot:ready"}}
	fakeTracker := &fakeTracker{candidates: []model.Issue{issue}, statesByID: map[string]model.Issue{"1": issue}}
	fakeWorkspace := &fakeWorkspace{root: filepath.Join(t.TempDir(), "workspaces")}
	fakeCopilot := &fakeCopilot{}
	orchestrator, err := New(workflowPath, Options{Builder: fakeBuilder{tracker: fakeTracker, workspace: fakeWorkspace, copilot: fakeCopilot}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	orchestrator.mu.Lock()
	orchestrator.state.claimed[issue.ID] = struct{}{}
	orchestrator.state.retryAttempts[issue.ID] = &retryState{
		entry: model.RetryEntry{IssueID: issue.ID, Identifier: issue.Identifier, Attempt: 2, DueAt: time.Now().Add(time.Minute)},
		timer: time.NewTimer(time.Hour),
	}
	orchestrator.mu.Unlock()

	orchestrator.handleRetry(issue.ID)
	orchestrator.wg.Wait()

	orchestrator.mu.Lock()
	if len(fakeCopilot.prompts) != 1 {
		orchestrator.mu.Unlock()
		t.Fatalf("prompts = %d, want 1", len(fakeCopilot.prompts))
	}
	retry, ok := orchestrator.state.retryAttempts[issue.ID]
	if !ok {
		orchestrator.mu.Unlock()
		t.Fatalf("retry entry missing after continuation dispatch")
	}
	if retry.entry.Error != "" {
		orchestrator.mu.Unlock()
		t.Fatalf("retry error = %q, want empty continuation retry", retry.entry.Error)
	}
	orchestrator.mu.Unlock()
	_ = orchestrator.shutdown(context.Background())
}

func TestRunWorkerStopsSilentTurnLoop(t *testing.T) {
	t.Parallel()
	workflowPath := writeWorkflowFileWithMaxTurns(t, 3)
	issue := model.Issue{ID: "1", Identifier: "octo/widgets#1", Title: "Task", State: "Open", Labels: []string{"autopilot:merging"}}
	fakeTracker := &fakeTracker{candidates: []model.Issue{issue}, statesByID: map[string]model.Issue{"1": issue}}
	fakeWorkspace := &fakeWorkspace{root: filepath.Join(t.TempDir(), "workspaces")}
	fakeCopilot := &fakeCopilot{silent: true}
	orchestrator, err := New(workflowPath, Options{Builder: fakeBuilder{tracker: fakeTracker, workspace: fakeWorkspace, copilot: fakeCopilot}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	orchestrator.tick(context.Background())
	orchestrator.wg.Wait()

	orchestrator.mu.Lock()
	if len(fakeCopilot.prompts) != 1 {
		orchestrator.mu.Unlock()
		t.Fatalf("prompts = %d, want 1", len(fakeCopilot.prompts))
	}
	retry, ok := orchestrator.state.retryAttempts[issue.ID]
	if !ok {
		orchestrator.mu.Unlock()
		t.Fatalf("retry entry missing after silent turn")
	}
	if retry.entry.Error != "stalled session" {
		orchestrator.mu.Unlock()
		t.Fatalf("retry error = %q, want stalled session", retry.entry.Error)
	}
	orchestrator.mu.Unlock()
	_ = orchestrator.shutdown(context.Background())
}

func TestRunWorkerStopsOnRateLimitNotification(t *testing.T) {
	t.Parallel()
	workflowPath := writeWorkflowFileWithMaxTurns(t, 3)
	issue := model.Issue{ID: "1", Identifier: "octo/widgets#1", Title: "Task", State: "Open", Labels: []string{"autopilot:merging"}}
	fakeTracker := &fakeTracker{candidates: []model.Issue{issue}, statesByID: map[string]model.Issue{"1": issue}}
	fakeWorkspace := &fakeWorkspace{root: filepath.Join(t.TempDir(), "workspaces")}
	fakeCopilot := &fakeCopilot{rateLimited: true}
	orchestrator, err := New(workflowPath, Options{Builder: fakeBuilder{tracker: fakeTracker, workspace: fakeWorkspace, copilot: fakeCopilot}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	orchestrator.tick(context.Background())
	orchestrator.wg.Wait()

	orchestrator.mu.Lock()
	if len(fakeCopilot.prompts) != 1 {
		orchestrator.mu.Unlock()
		t.Fatalf("prompts = %d, want 1", len(fakeCopilot.prompts))
	}
	retry, ok := orchestrator.state.retryAttempts[issue.ID]
	if !ok {
		orchestrator.mu.Unlock()
		t.Fatalf("retry entry missing after rate limit event")
	}
	if !strings.Contains(retry.entry.Error, "hit a rate limit") {
		orchestrator.mu.Unlock()
		t.Fatalf("retry error = %q, want rate limit message", retry.entry.Error)
	}
	orchestrator.mu.Unlock()
	_ = orchestrator.shutdown(context.Background())
}

func TestHTTPHandlersServeStateRefreshAndIssueDetail(t *testing.T) {
	t.Parallel()
	workflowPath := writeWorkflowFile(t)
	orchestrator, err := New(workflowPath, Options{Builder: fakeBuilder{tracker: &fakeTracker{}, workspace: &fakeWorkspace{root: t.TempDir()}, copilot: &fakeCopilot{}}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	now := time.Now().UTC()
	orchestrator.mu.Lock()
	orchestrator.state.running["1"] = &runningEntry{
		Issue:         model.Issue{ID: "1", Identifier: "octo/widgets#1", Title: "Task", State: "Open"},
		WorkspacePath: "/tmp/workspaces/octo_widgets_1",
		StartedAt:     now,
		Session:       model.LiveSession{SessionID: "session-1", TurnCount: 2, LastAgentEvent: "prompt_completed", LastAgentMessage: "done", CopilotTotalTokens: 33},
		RecentEvents:  []IssueEvent{{At: now, Event: "prompt_completed", Message: "done"}},
	}
	orchestrator.state.retryAttempts["2"] = &retryState{entry: model.RetryEntry{IssueID: "2", Identifier: "octo/widgets#2", Attempt: 3, DueAt: now.Add(time.Minute), Error: "retry error"}, timer: time.NewTimer(time.Hour)}
	orchestrator.mu.Unlock()
	defer orchestrator.shutdown(context.Background())

	stateRequest := httptest.NewRequest(http.MethodGet, "/api/v1/state", nil)
	stateRecorder := httptest.NewRecorder()
	orchestrator.handleState(stateRecorder, stateRequest)
	if stateRecorder.Code != http.StatusOK {
		t.Fatalf("/api/v1/state status = %d", stateRecorder.Code)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(stateRecorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if snapshot.Counts.Running != 1 || snapshot.Counts.Retrying != 1 {
		t.Fatalf("unexpected counts: %#v", snapshot.Counts)
	}

	dashboardRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	dashboardRecorder := httptest.NewRecorder()
	orchestrator.handleDashboard(dashboardRecorder, dashboardRequest)
	if dashboardRecorder.Code != http.StatusOK {
		t.Fatalf("dashboard status = %d", dashboardRecorder.Code)
	}
	dashboardBody := dashboardRecorder.Body.String()
	if !strings.Contains(dashboardBody, "/api/v1/state") {
		t.Fatalf("dashboard missing state polling hook: %s", dashboardBody)
	}
	if !strings.Contains(dashboardBody, "Auto-refreshing every") {
		t.Fatalf("dashboard missing auto-refresh indicator: %s", dashboardBody)
	}

	refreshRequest := httptest.NewRequest(http.MethodPost, "/api/v1/refresh", nil)
	refreshRecorder := httptest.NewRecorder()
	orchestrator.handleRefresh(refreshRecorder, refreshRequest)
	if refreshRecorder.Code != http.StatusAccepted {
		t.Fatalf("/api/v1/refresh status = %d", refreshRecorder.Code)
	}

	issueRequest := httptest.NewRequest(http.MethodGet, "/api/v1/octo/widgets%231", nil)
	issueRecorder := httptest.NewRecorder()
	orchestrator.handleIssue(issueRecorder, issueRequest)
	if issueRecorder.Code != http.StatusOK {
		t.Fatalf("issue detail status = %d body=%s", issueRecorder.Code, issueRecorder.Body.String())
	}
	var detail IssueDetail
	if err := json.Unmarshal(issueRecorder.Body.Bytes(), &detail); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if detail.IssueIdentifier != "octo/widgets#1" {
		t.Fatalf("issue detail identifier = %q", detail.IssueIdentifier)
	}
	if detail.Running == nil || detail.Running.SessionID != "session-1" {
		t.Fatalf("unexpected running detail: %#v", detail.Running)
	}

	missingRequest := httptest.NewRequest(http.MethodGet, "/api/v1/octo/widgets%23999", nil)
	missingRecorder := httptest.NewRecorder()
	orchestrator.handleIssue(missingRecorder, missingRequest)
	if missingRecorder.Code != http.StatusNotFound {
		t.Fatalf("missing issue status = %d", missingRecorder.Code)
	}
}

func writeWorkflowFile(t *testing.T) string {
	t.Helper()
	return writeWorkflowFileWithMaxTurns(t, 1)
}

func writeWorkflowFileWithMaxTurns(t *testing.T, maxTurns int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	content := `---
tracker:
  kind: github
  repository: octo/widgets
  api_key: token
polling:
  interval_ms: 25
agent:
  max_turns: ` + strconv.Itoa(maxTurns) + `
copilot:
  command: fake
  transport: acp_stdio
  prompt_timeout_ms: 1000
  startup_timeout_ms: 1000
---
Implement {{ issue.identifier }}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func stringPtr(value string) *string {
	return &value
}
