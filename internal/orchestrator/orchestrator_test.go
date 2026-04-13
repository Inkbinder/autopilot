package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/Inkbinder/autopilot/internal/runstate"
	"github.com/Inkbinder/autopilot/internal/workflow"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type fakeBuilder struct {
	tracker   IssueTracker
	workspace WorkspaceManager
	copilot   copilot.Client
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
	root       string
	removed    []string
	prepareErr error
}

func (workspace *fakeWorkspace) CreateForIssue(_ context.Context, issueIdentifier string) (model.Workspace, error) {
	path := filepath.Join(workspace.root, strings.ReplaceAll(strings.ReplaceAll(issueIdentifier, "/", "_"), "#", "_"))
	if err := os.MkdirAll(path, 0o755); err != nil {
		return model.Workspace{}, err
	}
	return model.Workspace{Path: path, WorkspaceKey: filepath.Base(path), CreatedNow: true}, nil
}

func (workspace *fakeWorkspace) PrepareForRun(context.Context, model.Workspace) error {
	return workspace.prepareErr
}
func (workspace *fakeWorkspace) RunAfterRunHook(context.Context, string) error { return nil }
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

type blockingCopilot struct {
	started chan struct{}
	release chan struct{}
	onEvent copilot.EventHandler
	one     sync.Once
}

func (client *blockingCopilot) StartSession(_ context.Context, request copilot.StartRequest) (copilot.Session, error) {
	client.onEvent = request.OnEvent
	if client.onEvent != nil {
		client.onEvent(copilot.Event{Event: "session_started", Timestamp: time.Now().UTC(), SessionID: "blocking-session", Turn: 0})
	}
	return &blockingSession{client: client}, nil
}

type blockingSession struct {
	client *blockingCopilot
}

func (session *blockingSession) ID() string        { return "blocking-session" }
func (session *blockingSession) Transport() string { return "acp_stdio" }
func (session *blockingSession) ProcessID() *int   { return nil }
func (session *blockingSession) RunPrompt(_ context.Context, _ string, turn int) error {
	session.client.one.Do(func() {
		close(session.client.started)
	})
	<-session.client.release
	if session.client.onEvent != nil {
		session.client.onEvent(copilot.Event{Event: "notification", Timestamp: time.Now().UTC(), SessionID: "blocking-session", Turn: turn, Message: "working"})
		session.client.onEvent(copilot.Event{Event: "prompt_completed", Timestamp: time.Now().UTC(), SessionID: "blocking-session", Turn: turn})
	}
	return nil
}
func (session *blockingSession) Close(context.Context) error { return nil }

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

func TestRunWorkerRemovesCreatedWorkspaceWhenPreFlightFails(t *testing.T) {
	t.Parallel()
	workflowPath := writeWorkflowFile(t)
	issue := model.Issue{ID: "1", Identifier: "octo/widgets#1", Title: "Task", State: "Open", Labels: []string{"autopilot:ready"}}
	fakeTracker := &fakeTracker{candidates: []model.Issue{issue}}
	fakeWorkspace := &fakeWorkspace{root: filepath.Join(t.TempDir(), "workspaces"), prepareErr: errors.New("before_run failed")}
	fakeCopilot := &fakeCopilot{}
	orchestrator, err := New(workflowPath, Options{Builder: fakeBuilder{tracker: fakeTracker, workspace: fakeWorkspace, copilot: fakeCopilot}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	orchestrator.tick(context.Background())
	orchestrator.wg.Wait()

	if len(fakeWorkspace.removed) != 1 || fakeWorkspace.removed[0] != issue.Identifier {
		t.Fatalf("removed workspaces = %#v, want %q", fakeWorkspace.removed, issue.Identifier)
	}
	if len(fakeCopilot.prompts) != 0 {
		t.Fatalf("prompts = %d, want 0", len(fakeCopilot.prompts))
	}
	_ = orchestrator.shutdown(context.Background())
}

func TestRunWorkerEmitsTracingPhases(t *testing.T) {
	t.Parallel()
	workflowPath := writeWorkflowFile(t)
	issue := model.Issue{ID: "1", Identifier: "octo/widgets#1", Title: "Task", State: "Open", Labels: []string{"autopilot:ready"}}
	tracker := &fakeTracker{candidates: []model.Issue{issue}, statesByID: map[string]model.Issue{"1": {ID: "1", Identifier: issue.Identifier, Title: issue.Title, State: "Closed"}}}
	workspace := &fakeWorkspace{root: filepath.Join(t.TempDir(), "workspaces")}
	client := &fakeCopilot{}
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider()
	tracerProvider.RegisterSpanProcessor(spanRecorder)
	t.Cleanup(func() {
		_ = tracerProvider.Shutdown(context.Background())
	})

	orchestrator, err := New(workflowPath, Options{Builder: fakeBuilder{tracker: tracker, workspace: workspace, copilot: client}, Tracer: tracerProvider.Tracer("test")})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	orchestrator.tick(context.Background())
	orchestrator.wg.Wait()

	spans := spanRecorder.Ended()
	if !spanNamesInOrder(spans, []string{"WorkspaceSetup", "PreFlightHooks", "CopilotExecution", "PostFlightHooks"}) {
		t.Fatalf("ended spans = %v, want execution phases in order", spanNames(spans))
	}
	var copilotSpan sdktrace.ReadOnlySpan
	for _, span := range spans {
		if span.Name() == "CopilotExecution" {
			copilotSpan = span
			break
		}
	}
	if copilotSpan == nil {
		t.Fatal("CopilotExecution span missing")
	}
	if got := spanAttributeValue(copilotSpan, "issue_id"); got != issue.ID {
		t.Fatalf("CopilotExecution issue_id = %q, want %q", got, issue.ID)
	}
	if got := spanAttributeValue(copilotSpan, "model"); got != "gpt-5.4" {
		t.Fatalf("CopilotExecution model = %q, want gpt-5.4", got)
	}
	_ = orchestrator.shutdown(context.Background())
}

func TestHTTPHandlersServeStateRefreshAndIssueDetail(t *testing.T) {
	t.Parallel()
	workflowPath := writeWorkflowFile(t)
	store, err := runstate.OpenSQLite(filepath.Join(t.TempDir(), ".autopilot", "runs.db"))
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	defer store.Close()
	startedAt := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
	runID, err := store.CreateRun(context.Background(), runstate.CreateRunParams{IssueID: "1", Repo: "octo/widgets", Status: runstate.StatusRunning, StartTime: startedAt})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	finishedAt := startedAt.Add(time.Minute)
	errorMessage := "audit trail"
	if err := store.UpdateRun(context.Background(), runstate.UpdateRunParams{RunID: runID, Status: runstate.StatusFailed, EndTime: &finishedAt, ErrorMessage: &errorMessage}); err != nil {
		t.Fatalf("UpdateRun() error = %v", err)
	}
	if err := store.InsertAuditEvent(context.Background(), runstate.AuditEvent{RunID: runID, Timestamp: startedAt.Add(30 * time.Second), ActionType: "workspace_exec", Payload: `{"command":"sh","output":"hello"}`}); err != nil {
		t.Fatalf("InsertAuditEvent() error = %v", err)
	}
	orchestrator, err := New(workflowPath, Options{Builder: fakeBuilder{tracker: &fakeTracker{}, workspace: &fakeWorkspace{root: t.TempDir()}, copilot: &fakeCopilot{}}, RunStore: store})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	now := time.Now().UTC()
	orchestrator.mu.Lock()
	orchestrator.state.running["1"] = &runningEntry{
		RunID:         runID,
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
	if !strings.Contains(dashboardBody, "/api/v1/runs") {
		t.Fatalf("dashboard missing run history hook: %s", dashboardBody)
	}
	if !strings.Contains(dashboardBody, "/runs/"+strconv.FormatInt(runID, 10)) {
		t.Fatalf("dashboard missing HTML run detail links: %s", dashboardBody)
	}
	if !strings.Contains(dashboardBody, "Auto-refreshing every") {
		t.Fatalf("dashboard missing auto-refresh indicator: %s", dashboardBody)
	}
	if !strings.Contains(dashboardBody, "History") {
		t.Fatalf("dashboard missing history section: %s", dashboardBody)
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

	runsRequest := httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil)
	runsRecorder := httptest.NewRecorder()
	orchestrator.handleRuns(runsRecorder, runsRequest)
	if runsRecorder.Code != http.StatusOK {
		t.Fatalf("runs status = %d body=%s", runsRecorder.Code, runsRecorder.Body.String())
	}
	var runs []runstate.RunRecord
	if err := json.Unmarshal(runsRecorder.Body.Bytes(), &runs); err != nil {
		t.Fatalf("json.Unmarshal(runs) error = %v", err)
	}
	if len(runs) != 1 || runs[0].ID != runID {
		t.Fatalf("unexpected runs payload: %#v", runs)
	}

	runDetailRequest := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+strconv.FormatInt(runID, 10), nil)
	runDetailRecorder := httptest.NewRecorder()
	orchestrator.handleRun(runDetailRecorder, runDetailRequest)
	if runDetailRecorder.Code != http.StatusOK {
		t.Fatalf("run detail status = %d body=%s", runDetailRecorder.Code, runDetailRecorder.Body.String())
	}
	var runDetail runstate.RunDetail
	if err := json.Unmarshal(runDetailRecorder.Body.Bytes(), &runDetail); err != nil {
		t.Fatalf("json.Unmarshal(run detail) error = %v", err)
	}
	if runDetail.ID != runID || len(runDetail.AuditEvents) != 1 {
		t.Fatalf("unexpected run detail payload: %#v", runDetail)
	}
	if runDetail.AuditEvents[0].ActionType != "workspace_exec" {
		t.Fatalf("unexpected audit event: %#v", runDetail.AuditEvents[0])
	}

	runPageRequest := httptest.NewRequest(http.MethodGet, "/runs/"+strconv.FormatInt(runID, 10), nil)
	runPageRecorder := httptest.NewRecorder()
	orchestrator.handleRunPage(runPageRecorder, runPageRequest)
	if runPageRecorder.Code != http.StatusOK {
		t.Fatalf("run page status = %d body=%s", runPageRecorder.Code, runPageRecorder.Body.String())
	}
	runPageBody := runPageRecorder.Body.String()
	if !strings.Contains(runPageBody, "Back to Dashboard") {
		t.Fatalf("run page missing dashboard navigation: %s", runPageBody)
	}
	if !strings.Contains(runPageBody, "Run #"+strconv.FormatInt(runID, 10)) {
		t.Fatalf("run page missing run title: %s", runPageBody)
	}
	if !strings.Contains(runPageBody, "workspace_exec") {
		t.Fatalf("run page missing audit timeline: %s", runPageBody)
	}
	if strings.Contains(runPageBody, runDetailReloadText) {
		t.Fatalf("run page unexpectedly auto-refreshes completed runs: %s", runPageBody)
	}

	missingRequest := httptest.NewRequest(http.MethodGet, "/api/v1/octo/widgets%23999", nil)
	missingRecorder := httptest.NewRecorder()
	orchestrator.handleIssue(missingRecorder, missingRequest)
	if missingRecorder.Code != http.StatusNotFound {
		t.Fatalf("missing issue status = %d", missingRecorder.Code)
	}
}

func TestDashboardRunningSessionWithoutRunIDRendersPlainIssueText(t *testing.T) {
	t.Parallel()
	workflowPath := writeWorkflowFile(t)
	orchestrator, err := New(workflowPath, Options{Builder: fakeBuilder{tracker: &fakeTracker{}, workspace: &fakeWorkspace{root: t.TempDir()}, copilot: &fakeCopilot{}}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer orchestrator.shutdown(context.Background())
	now := time.Now().UTC()
	orchestrator.mu.Lock()
	orchestrator.state.running["1"] = &runningEntry{
		Issue:         model.Issue{ID: "1", Identifier: "octo/widgets#1", Title: "Task", State: "Open"},
		WorkspacePath: "/tmp/workspaces/octo_widgets_1",
		StartedAt:     now,
		Session:       model.LiveSession{SessionID: "session-1", LastAgentEvent: "session_started"},
	}
	orchestrator.mu.Unlock()

	dashboardRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	dashboardRecorder := httptest.NewRecorder()
	orchestrator.handleDashboard(dashboardRecorder, dashboardRequest)
	if dashboardRecorder.Code != http.StatusOK {
		t.Fatalf("dashboard status = %d", dashboardRecorder.Code)
	}
	body := dashboardRecorder.Body.String()
	if !strings.Contains(body, `<code>octo/widgets#1</code>`) {
		t.Fatalf("dashboard missing issue text: %s", body)
	}
	if strings.Contains(body, `href="/runs/1"`) {
		t.Fatalf("dashboard unexpectedly rendered run link without run id: %s", body)
	}
}

func TestDispatchPublishesRunningEntryWithRunID(t *testing.T) {
	t.Parallel()
	workflowPath := writeWorkflowFileWithMaxTurns(t, 1)
	issue := model.Issue{ID: "1", Identifier: "octo/widgets#1", Title: "Task", State: "Open", Labels: []string{"autopilot:ready"}, BlockedBy: []model.BlockerRef{{Identifier: stringPtr("octo/widgets#2"), State: stringPtr("Closed")}}}
	tracker := &fakeTracker{candidates: []model.Issue{issue}, statesByID: map[string]model.Issue{"1": issue}}
	workspace := &fakeWorkspace{root: filepath.Join(t.TempDir(), "workspaces")}
	client := &blockingCopilot{started: make(chan struct{}), release: make(chan struct{})}
	orchestrator, err := New(workflowPath, Options{Builder: fakeBuilder{tracker: tracker, workspace: workspace, copilot: client}, RunStore: &runStoreRecorder{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer orchestrator.shutdown(context.Background())

	orchestrator.tick(context.Background())
	select {
	case <-client.started:
	case <-time.After(2 * time.Second):
		close(client.release)
		t.Fatal("worker did not start in time")
	}

	snapshot := orchestrator.Snapshot()
	if len(snapshot.Running) != 1 {
		close(client.release)
		t.Fatalf("running count = %d, want 1", len(snapshot.Running))
	}
	if snapshot.Running[0].RunID == 0 {
		close(client.release)
		t.Fatalf("running snapshot missing run id: %#v", snapshot.Running[0])
	}

	close(client.release)
	orchestrator.wg.Wait()
}

func TestRunPageAutoRefreshesActiveRuns(t *testing.T) {
	t.Parallel()
	workflowPath := writeWorkflowFile(t)
	store, err := runstate.OpenSQLite(filepath.Join(t.TempDir(), ".autopilot", "runs.db"))
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	defer store.Close()
	startedAt := time.Now().UTC().Truncate(time.Second)
	runID, err := store.CreateRun(context.Background(), runstate.CreateRunParams{IssueID: "1", Repo: "octo/widgets", Status: runstate.StatusRunning, StartTime: startedAt})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	orchestrator, err := New(workflowPath, Options{Builder: fakeBuilder{tracker: &fakeTracker{}, workspace: &fakeWorkspace{root: t.TempDir()}, copilot: &fakeCopilot{}}, RunStore: store})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer orchestrator.shutdown(context.Background())

	runPageRequest := httptest.NewRequest(http.MethodGet, "/runs/"+strconv.FormatInt(runID, 10), nil)
	runPageRecorder := httptest.NewRecorder()
	orchestrator.handleRunPage(runPageRecorder, runPageRequest)
	if runPageRecorder.Code != http.StatusOK {
		t.Fatalf("run page status = %d body=%s", runPageRecorder.Code, runPageRecorder.Body.String())
	}
	body := runPageRecorder.Body.String()
	if !strings.Contains(body, runDetailReloadText) {
		t.Fatalf("running run page missing auto-refresh badge: %s", body)
	}
	if !strings.Contains(body, "window.location.reload()") {
		t.Fatalf("running run page missing reload script: %s", body)
	}
}

func writeWorkflowFile(t *testing.T) string {
	t.Helper()
	return writeWorkflowFileWithMaxTurns(t, 1)
}

func writeWorkflowFileWithMaxTurns(t *testing.T, maxTurns int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	content := "---\n" +
		"tracker:\n" +
		"  kind: github\n" +
		"  repository: octo/widgets\n" +
		"  api_key: token\n" +
		"polling:\n" +
		"  interval_ms: 25\n" +
		"agent:\n" +
		"  max_turns: " + strconv.Itoa(maxTurns) + "\n" +
		"copilot:\n" +
		"  command: fake\n" +
		"  transport: acp_stdio\n" +
		"  model: gpt-5.4\n" +
		"  prompt_timeout_ms: 1000\n" +
		"  startup_timeout_ms: 1000\n" +
		"---\n" +
		"Implement {{ issue.identifier }}\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func stringPtr(value string) *string {
	return &value
}

func spanNamesInOrder(spans []sdktrace.ReadOnlySpan, want []string) bool {
	index := 0
	for _, span := range spans {
		if index >= len(want) {
			return true
		}
		if span.Name() == want[index] {
			index++
		}
	}
	return index == len(want)
}

func spanNames(spans []sdktrace.ReadOnlySpan) []string {
	names := make([]string, 0, len(spans))
	for _, span := range spans {
		names = append(names, span.Name())
	}
	return names
}

func spanAttributeValue(span sdktrace.ReadOnlySpan, key string) string {
	for _, attribute := range span.Attributes() {
		if string(attribute.Key) == key {
			return attribute.Value.AsString()
		}
	}
	return ""
}
