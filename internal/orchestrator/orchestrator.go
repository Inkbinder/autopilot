package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"autopilot/internal/copilot"
	"autopilot/internal/model"
	"autopilot/internal/workflow"
)

const defaultFallbackPrompt = "You are working on an issue from GitHub."

type Orchestrator struct {
	workflowPath string
	logger       *slog.Logger
	builder      DependencyBuilder
	portOverride *int

	mu        sync.Mutex
	definition workflow.Definition
	config     workflow.Config
	tracker    IssueTracker
	workspace  WorkspaceManager
	copilot    copilot.Client
	state      runtimeState

	refreshCh   chan struct{}
	serverErrCh chan error
	server      *httpServer

	runCtx context.Context
	wg     sync.WaitGroup
}

type runtimeState struct {
	pollInterval        time.Duration
	maxConcurrentAgents int
	running             map[string]*runningEntry
	claimed             map[string]struct{}
	retryAttempts       map[string]*retryState
	completed           map[string]struct{}
	copilotTotals       model.CopilotTotals
	copilotRateLimits   any
}

type runningEntry struct {
	Issue          model.Issue
	WorkspacePath  string
	StartedAt      time.Time
	RetryAttempt   int
	Session        model.LiveSession
	RecentEvents   []IssueEvent
	Cancel         context.CancelFunc
	StopReason     string
	CleanupWorkspace bool
	LastError      string
	RestartCount   int
}

type retryState struct {
	entry model.RetryEntry
	timer *time.Timer
}

type IssueEvent struct {
	At      time.Time `json:"at"`
	Event   string    `json:"event"`
	Message string    `json:"message,omitempty"`
}

type Snapshot struct {
	GeneratedAt   time.Time               `json:"generated_at"`
	Counts        SnapshotCounts         `json:"counts"`
	Running       []RunningSnapshot      `json:"running"`
	Retrying      []RetrySnapshot        `json:"retrying"`
	CopilotTotals model.CopilotTotals    `json:"copilot_totals"`
	RateLimits    any                    `json:"rate_limits"`
}

type SnapshotCounts struct {
	Running  int `json:"running"`
	Retrying int `json:"retrying"`
}

type RunningSnapshot struct {
	IssueID       string              `json:"issue_id"`
	Identifier    string              `json:"issue_identifier"`
	State         string              `json:"state"`
	SessionID     string              `json:"session_id,omitempty"`
	TurnCount     int                 `json:"turn_count"`
	LastEvent     string              `json:"last_event,omitempty"`
	LastMessage   string              `json:"last_message,omitempty"`
	StartedAt     time.Time           `json:"started_at"`
	LastEventAt   *time.Time          `json:"last_event_at,omitempty"`
	Tokens        copilot.UsageTotals `json:"tokens"`
	WorkspacePath string              `json:"workspace_path,omitempty"`
	RestartCount  int                 `json:"restart_count"`
	LastError     string              `json:"last_error,omitempty"`
}

type RetrySnapshot struct {
	IssueID    string    `json:"issue_id"`
	Identifier string    `json:"issue_identifier"`
	Attempt    int       `json:"attempt"`
	DueAt      time.Time `json:"due_at"`
	Error      string    `json:"error,omitempty"`
}

type IssueDetail struct {
	IssueIdentifier string                 `json:"issue_identifier"`
	IssueID         string                 `json:"issue_id"`
	Status          string                 `json:"status"`
	Workspace       map[string]string      `json:"workspace,omitempty"`
	Attempts        map[string]int         `json:"attempts,omitempty"`
	Running         *RunningSnapshot       `json:"running,omitempty"`
	Retry           *RetrySnapshot         `json:"retry,omitempty"`
	Logs            map[string]any         `json:"logs,omitempty"`
	RecentEvents    []IssueEvent           `json:"recent_events,omitempty"`
	LastError       *string                `json:"last_error,omitempty"`
	Tracked         map[string]any         `json:"tracked,omitempty"`
}

func New(workflowPath string, options Options) (*Orchestrator, error) {
	if strings.TrimSpace(workflowPath) == "" {
		return nil, fmt.Errorf("workflow path is required")
	}
	builder := options.Builder
	if builder == nil {
		builder = DefaultDependencyBuilder{}
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	definition, config, err := workflow.LoadAndResolve(workflowPath, nil)
	if err != nil {
		return nil, err
	}
	trackerClient, workspaceManager, copilotClient, err := builder.Build(config)
	if err != nil {
		return nil, err
	}
	return &Orchestrator{
		workflowPath: workflowPath,
		logger:       logger,
		builder:      builder,
		portOverride: options.PortOverride,
		definition:   definition,
		config:       config,
		tracker:      trackerClient,
		workspace:    workspaceManager,
		copilot:      copilotClient,
		state: runtimeState{
			pollInterval:        config.Polling.Interval,
			maxConcurrentAgents: config.Agent.MaxConcurrentAgents,
			running:             map[string]*runningEntry{},
			claimed:             map[string]struct{}{},
			retryAttempts:       map[string]*retryState{},
			completed:           map[string]struct{}{},
		},
		refreshCh:   make(chan struct{}, 1),
		serverErrCh: make(chan error, 1),
	}, nil
}

func (orchestrator *Orchestrator) Run(ctx context.Context) error {
	orchestrator.runCtx = ctx
	if err := workflow.WatchFile(ctx, orchestrator.workflowPath, func() {
		orchestrator.reloadWorkflow(true)
		orchestrator.TriggerRefresh()
	}, func(err error) {
		orchestrator.logger.Error("workflow watch error", slog.Any("error", err))
	}); err != nil {
		return err
	}
	if err := orchestrator.startHTTPServer(); err != nil {
		return err
	}
	orchestrator.startupCleanup(ctx)

	for {
		orchestrator.tick(ctx)
		interval := orchestrator.currentPollInterval()
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return orchestrator.shutdown(context.Background())
		case err := <-orchestrator.serverErrCh:
			timer.Stop()
			if err != nil {
				_ = orchestrator.shutdown(context.Background())
				return err
			}
		case <-orchestrator.refreshCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
	}
}

func (orchestrator *Orchestrator) TriggerRefresh() bool {
	select {
	case orchestrator.refreshCh <- struct{}{}:
		return false
	default:
		return true
	}
}

func (orchestrator *Orchestrator) tick(ctx context.Context) {
	orchestrator.reloadWorkflow(false)
	orchestrator.reconcileRunningIssues(ctx)

	trackerClient, _, _, config, _, _ := orchestrator.snapshotDependencies()
	if err := config.ValidateDispatch(); err != nil {
		orchestrator.logger.Error("dispatch validation failed", slog.Any("error", err))
		return
	}
	issues, err := trackerClient.FetchCandidateIssues(ctx)
	if err != nil {
		orchestrator.logger.Error("candidate fetch failed", slog.Any("error", err))
		return
	}
	sortForDispatch(issues)
	for _, issue := range issues {
		if !orchestrator.dispatchIfEligible(ctx, issue, nil) {
			if orchestrator.noAvailableSlots() {
				break
			}
		}
	}
}

func (orchestrator *Orchestrator) dispatchIfEligible(ctx context.Context, issue model.Issue, attempt *int) bool {
	definition, config, trackerClient, workspaceManager, copilotClient, allowed := orchestrator.claimForDispatch(issue, attempt)
	if !allowed {
		return false
	}
	workerCtx, cancel := context.WithCancel(ctx)
	orchestrator.mu.Lock()
	entry := orchestrator.state.running[issue.ID]
	entry.Cancel = cancel
	orchestrator.mu.Unlock()

	orchestrator.wg.Add(1)
	go func() {
		defer orchestrator.wg.Done()
		orchestrator.runWorker(workerCtx, definition, config, trackerClient, workspaceManager, copilotClient, issue, attempt)
	}()
	return true
}

func (orchestrator *Orchestrator) claimForDispatch(issue model.Issue, attempt *int) (workflow.Definition, workflow.Config, IssueTracker, WorkspaceManager, copilot.Client, bool) {
	orchestrator.mu.Lock()
	defer orchestrator.mu.Unlock()
	if !orchestrator.isEligibleLocked(issue) {
		return workflow.Definition{}, workflow.Config{}, nil, nil, nil, false
	}
	retryAttempt := 0
	if attempt != nil {
		retryAttempt = *attempt
	}
	orchestrator.state.claimed[issue.ID] = struct{}{}
	if retry, ok := orchestrator.state.retryAttempts[issue.ID]; ok {
		retry.timer.Stop()
		delete(orchestrator.state.retryAttempts, issue.ID)
	}
	orchestrator.state.running[issue.ID] = &runningEntry{
		Issue:         issue,
		StartedAt:     time.Now().UTC(),
		RetryAttempt:  retryAttempt,
		RestartCount:  retryAttempt,
		Session: model.LiveSession{
			Transport: orchestrator.config.Copilot.Transport,
		},
	}
	return orchestrator.definition, orchestrator.config, orchestrator.tracker, orchestrator.workspace, orchestrator.copilot, true
}

func (orchestrator *Orchestrator) isEligibleLocked(issue model.Issue) bool {
	if issue.ID == "" || issue.Identifier == "" || issue.Title == "" || issue.State == "" {
		return false
	}
	if _, ok := orchestrator.state.running[issue.ID]; ok {
		return false
	}
	if _, ok := orchestrator.state.claimed[issue.ID]; ok {
		return false
	}
	if !containsNormalized(orchestrator.config.Tracker.ActiveStates, issue.State) {
		return false
	}
	if containsNormalized(orchestrator.config.Tracker.TerminalStates, issue.State) {
		return false
	}
	if !orchestrator.hasAvailableSlotsLocked(issue.NormalizedState()) {
		return false
	}
	if issueHasActiveBlockers(issue, orchestrator.config.Tracker.TerminalStates) {
		return false
	}
	return true
}

func (orchestrator *Orchestrator) hasAvailableSlotsLocked(state string) bool {
	if len(orchestrator.state.running) >= orchestrator.config.Agent.MaxConcurrentAgents {
		return false
	}
	limit, ok := orchestrator.config.Agent.MaxConcurrentAgentsByState[strings.ToLower(state)]
	if !ok {
		return true
	}
	runningInState := 0
	for _, entry := range orchestrator.state.running {
		if entry.Issue.NormalizedState() == strings.ToLower(state) {
			runningInState++
		}
	}
	return runningInState < limit
}

func (orchestrator *Orchestrator) noAvailableSlots() bool {
	orchestrator.mu.Lock()
	defer orchestrator.mu.Unlock()
	return len(orchestrator.state.running) >= orchestrator.config.Agent.MaxConcurrentAgents
}

func (orchestrator *Orchestrator) runWorker(ctx context.Context, definition workflow.Definition, config workflow.Config, trackerClient IssueTracker, workspaceManager WorkspaceManager, copilotClient copilot.Client, issue model.Issue, attempt *int) {
	workspace, err := workspaceManager.CreateForIssue(ctx, issue.Identifier)
	if err != nil {
		orchestrator.handleWorkerOutcome(issue.ID, workerOutcome{Issue: issue, Err: err})
		return
	}
	orchestrator.updateWorkspacePath(issue.ID, workspace.Path)
	if err := workspaceManager.PrepareForRun(ctx, workspace); err != nil {
		if workspace.CreatedNow {
			_ = os.RemoveAll(workspace.Path)
		}
		_ = workspaceManager.RunAfterRunHook(context.Background(), workspace.Path)
		orchestrator.handleWorkerOutcome(issue.ID, workerOutcome{Issue: issue, Err: err})
		return
	}
	defer func() {
		if err := workspaceManager.RunAfterRunHook(context.Background(), workspace.Path); err != nil {
			orchestrator.logger.Warn("after_run hook failed", slog.String("issue_id", issue.ID), slog.String("issue_identifier", issue.Identifier), slog.Any("error", err))
		}
	}()

	promptTemplate := definition.PromptTemplate
	if strings.TrimSpace(promptTemplate) == "" {
		promptTemplate = defaultFallbackPrompt
	}
	prompt, err := workflow.RenderPrompt(promptTemplate, issue, attempt)
	if err != nil {
		orchestrator.handleWorkerOutcome(issue.ID, workerOutcome{Issue: issue, Err: err})
		return
	}
	session, err := copilotClient.StartSession(ctx, copilot.StartRequest{
		WorkspacePath: workspace.Path,
		Copilot:       config.Copilot,
		OnEvent: func(event copilot.Event) {
			orchestrator.handleAgentEvent(issue.ID, event)
		},
	})
	if err != nil {
		orchestrator.handleWorkerOutcome(issue.ID, workerOutcome{Issue: issue, Err: err})
		return
	}
	orchestrator.attachSession(issue.ID, session)
	defer session.Close(context.Background())

	currentIssue := issue
	for turn := 1; turn <= config.Agent.MaxTurns; turn++ {
		turnPrompt := prompt
		if turn > 1 {
			turnPrompt = copilot.DefaultContinuationPrompt
		}
		promptCtx, cancel := context.WithTimeout(ctx, config.Copilot.PromptTimeout)
		err := session.RunPrompt(promptCtx, turnPrompt, turn)
		cancel()
		if err != nil {
			orchestrator.handleWorkerOutcome(issue.ID, workerOutcome{Issue: currentIssue, Err: err})
			return
		}
		refreshed, err := trackerClient.FetchIssueStatesByIDs(ctx, []string{issue.ID})
		if err != nil {
			orchestrator.handleWorkerOutcome(issue.ID, workerOutcome{Issue: currentIssue, Err: err})
			return
		}
		if len(refreshed) > 0 {
			currentIssue = refreshed[0]
			orchestrator.updateIssue(issue.ID, currentIssue)
		}
		if !containsNormalized(config.Tracker.ActiveStates, currentIssue.State) {
			break
		}
	}
	orchestrator.handleWorkerOutcome(issue.ID, workerOutcome{Issue: currentIssue, Normal: true})
}

type workerOutcome struct {
	Issue   model.Issue
	Normal  bool
	Err     error
}

func (orchestrator *Orchestrator) handleWorkerOutcome(issueID string, outcome workerOutcome) {
	orchestrator.mu.Lock()
	entry, ok := orchestrator.state.running[issueID]
	if !ok {
		orchestrator.mu.Unlock()
		return
	}
	delete(orchestrator.state.running, issueID)
	runtimeSeconds := time.Since(entry.StartedAt).Seconds()
	orchestrator.state.copilotTotals.SecondsRunning += runtimeSeconds
	stopReason := entry.StopReason
	cleanupWorkspace := entry.CleanupWorkspace
	identifier := entry.Issue.Identifier
	nextAttempt := 1
	if entry.RetryAttempt > 0 {
		nextAttempt = entry.RetryAttempt + 1
	}
	if outcome.Err != nil {
		entry.LastError = outcome.Err.Error()
	}
	orchestrator.mu.Unlock()

	switch stopReason {
	case "terminal":
		if cleanupWorkspace {
			_ = orchestrator.workspace.RemoveForIssue(context.Background(), identifier)
		}
		orchestrator.releaseClaim(issueID)
		return
	case "inactive", "shutdown":
		orchestrator.releaseClaim(issueID)
		return
	case "stalled":
		orchestrator.scheduleRetry(issueID, identifier, nextAttempt, false, "stalled session")
		return
	}

	if outcome.Normal {
		orchestrator.mu.Lock()
		orchestrator.state.completed[issueID] = struct{}{}
		orchestrator.mu.Unlock()
		orchestrator.scheduleRetry(issueID, identifier, 1, true, "")
		return
	}
	orchestrator.scheduleRetry(issueID, identifier, nextAttempt, false, errorString(outcome.Err))
}

func (orchestrator *Orchestrator) releaseClaim(issueID string) {
	orchestrator.mu.Lock()
	defer orchestrator.mu.Unlock()
	delete(orchestrator.state.claimed, issueID)
	if retry, ok := orchestrator.state.retryAttempts[issueID]; ok {
		retry.timer.Stop()
		delete(orchestrator.state.retryAttempts, issueID)
	}
}

func (orchestrator *Orchestrator) scheduleRetry(issueID string, identifier string, attempt int, continuation bool, errorMessage string) {
	if attempt <= 0 {
		attempt = 1
	}
	delay := time.Second
	if !continuation {
		multiplier := math.Pow(2, float64(attempt-1))
		delay = time.Duration(10000*multiplier) * time.Millisecond
		maxDelay := orchestrator.currentRetryBackoff()
		if delay > maxDelay {
			delay = maxDelay
		}
	}
	dueAt := time.Now().Add(delay)
	timer := time.AfterFunc(delay, func() {
		orchestrator.handleRetry(issueID)
	})

	orchestrator.mu.Lock()
	defer orchestrator.mu.Unlock()
	if retry, ok := orchestrator.state.retryAttempts[issueID]; ok {
		retry.timer.Stop()
	}
	orchestrator.state.claimed[issueID] = struct{}{}
	orchestrator.state.retryAttempts[issueID] = &retryState{
		entry: model.RetryEntry{IssueID: issueID, Identifier: identifier, Attempt: attempt, DueAt: dueAt, Error: errorMessage},
		timer: timer,
	}
}

func (orchestrator *Orchestrator) handleRetry(issueID string) {
	orchestrator.mu.Lock()
	retry, ok := orchestrator.state.retryAttempts[issueID]
	if !ok {
		orchestrator.mu.Unlock()
		return
	}
	delete(orchestrator.state.retryAttempts, issueID)
	identifier := retry.entry.Identifier
	attempt := retry.entry.Attempt
	orchestrator.mu.Unlock()

	trackerClient, _, _, _, _, _ := orchestrator.snapshotDependencies()
	issues, err := trackerClient.FetchCandidateIssues(context.Background())
	if err != nil {
		orchestrator.scheduleRetry(issueID, identifier, attempt+1, false, "retry poll failed")
		return
	}
	for _, issue := range issues {
		if issue.ID != issueID {
			continue
		}
		value := attempt
		if orchestrator.dispatchIfEligible(context.Background(), issue, &value) {
			return
		}
		orchestrator.scheduleRetry(issueID, issue.Identifier, attempt+1, false, "no available orchestrator slots")
		return
	}
	orchestrator.releaseClaim(issueID)
}

func (orchestrator *Orchestrator) reconcileRunningIssues(ctx context.Context) {
	trackerClient, _, _, config, _, running := orchestrator.snapshotDependencies()
	if len(running) == 0 {
		return
	}
	if config.Copilot.StallTimeout > 0 {
		now := time.Now().UTC()
		for _, entry := range running {
			last := entry.StartedAt
			if entry.Session.LastAgentTimestamp != nil {
				last = *entry.Session.LastAgentTimestamp
			}
			if now.Sub(last) > config.Copilot.StallTimeout {
				orchestrator.stopRunningIssue(entry.Issue.ID, "stalled", false)
			}
		}
	}
	ids := make([]string, 0, len(running))
	for _, entry := range running {
		ids = append(ids, entry.Issue.ID)
	}
	refreshed, err := trackerClient.FetchIssueStatesByIDs(ctx, ids)
	if err != nil {
		orchestrator.logger.Warn("running issue refresh failed", slog.Any("error", err))
		return
	}
	refreshedByID := map[string]model.Issue{}
	for _, issue := range refreshed {
		refreshedByID[issue.ID] = issue
	}
	for _, entry := range running {
		issue, ok := refreshedByID[entry.Issue.ID]
		if !ok {
			continue
		}
		if containsNormalized(config.Tracker.TerminalStates, issue.State) {
			orchestrator.stopRunningIssue(issue.ID, "terminal", true)
			continue
		}
		if containsNormalized(config.Tracker.ActiveStates, issue.State) {
			orchestrator.updateIssue(issue.ID, issue)
			continue
		}
		orchestrator.stopRunningIssue(issue.ID, "inactive", false)
	}
}

func (orchestrator *Orchestrator) stopRunningIssue(issueID string, reason string, cleanupWorkspace bool) {
	orchestrator.mu.Lock()
	entry, ok := orchestrator.state.running[issueID]
	if ok {
		entry.StopReason = reason
		entry.CleanupWorkspace = cleanupWorkspace
		cancel := entry.Cancel
		orchestrator.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return
	}
	orchestrator.mu.Unlock()
}

func (orchestrator *Orchestrator) startupCleanup(ctx context.Context) {
	trackerClient, workspaceManager, _, config, _, _ := orchestrator.snapshotDependencies()
	issues, err := trackerClient.FetchIssuesByStates(ctx, config.Tracker.TerminalStates)
	if err != nil {
		orchestrator.logger.Warn("startup cleanup failed", slog.Any("error", err))
		return
	}
	for _, issue := range issues {
		if err := workspaceManager.RemoveForIssue(ctx, issue.Identifier); err != nil {
			orchestrator.logger.Warn("startup workspace cleanup failed", slog.String("issue_id", issue.ID), slog.String("issue_identifier", issue.Identifier), slog.Any("error", err))
		}
	}
}

func (orchestrator *Orchestrator) reloadWorkflow(logInvalid bool) {
	definition, config, err := workflow.LoadAndResolve(orchestrator.workflowPath, nil)
	if err != nil {
		if logInvalid {
			orchestrator.logger.Error("workflow reload failed", slog.Any("error", err))
		}
		return
	}
	trackerClient, workspaceManager, copilotClient, err := orchestrator.builder.Build(config)
	if err != nil {
		if logInvalid {
			orchestrator.logger.Error("workflow reload dependency rebuild failed", slog.Any("error", err))
		}
		return
	}
	orchestrator.mu.Lock()
	orchestrator.definition = definition
	orchestrator.config = config
	orchestrator.tracker = trackerClient
	orchestrator.workspace = workspaceManager
	orchestrator.copilot = copilotClient
	orchestrator.state.pollInterval = config.Polling.Interval
	orchestrator.state.maxConcurrentAgents = config.Agent.MaxConcurrentAgents
	orchestrator.mu.Unlock()
	if orchestrator.server != nil {
		port := orchestrator.effectivePort(&config)
		if port != nil && *port != orchestrator.server.port {
			orchestrator.logger.Warn("http listener port changed in workflow; restart required", slog.Int("current_port", orchestrator.server.port), slog.Int("requested_port", *port))
		}
	}
}

func (orchestrator *Orchestrator) currentPollInterval() time.Duration {
	orchestrator.mu.Lock()
	defer orchestrator.mu.Unlock()
	return orchestrator.state.pollInterval
}

func (orchestrator *Orchestrator) currentRetryBackoff() time.Duration {
	orchestrator.mu.Lock()
	defer orchestrator.mu.Unlock()
	return orchestrator.config.Agent.MaxRetryBackoff
}

func (orchestrator *Orchestrator) snapshotDependencies() (IssueTracker, WorkspaceManager, copilot.Client, workflow.Config, workflow.Definition, []*runningEntry) {
	orchestrator.mu.Lock()
	defer orchestrator.mu.Unlock()
	running := make([]*runningEntry, 0, len(orchestrator.state.running))
	for _, entry := range orchestrator.state.running {
		clone := *entry
		running = append(running, &clone)
	}
	return orchestrator.tracker, orchestrator.workspace, orchestrator.copilot, orchestrator.config, orchestrator.definition, running
}

func (orchestrator *Orchestrator) attachSession(issueID string, session copilot.Session) {
	orchestrator.mu.Lock()
	defer orchestrator.mu.Unlock()
	entry, ok := orchestrator.state.running[issueID]
	if !ok {
		return
	}
	entry.Session.SessionID = session.ID()
	entry.Session.Transport = session.Transport()
	entry.Session.CopilotCLIPID = session.ProcessID()
}

func (orchestrator *Orchestrator) updateWorkspacePath(issueID string, workspacePath string) {
	orchestrator.mu.Lock()
	defer orchestrator.mu.Unlock()
	if entry, ok := orchestrator.state.running[issueID]; ok {
		entry.WorkspacePath = workspacePath
	}
}

func (orchestrator *Orchestrator) updateIssue(issueID string, issue model.Issue) {
	orchestrator.mu.Lock()
	defer orchestrator.mu.Unlock()
	if entry, ok := orchestrator.state.running[issueID]; ok {
		entry.Issue = issue
	}
}

func (orchestrator *Orchestrator) handleAgentEvent(issueID string, event copilot.Event) {
	orchestrator.mu.Lock()
	defer orchestrator.mu.Unlock()
	entry, ok := orchestrator.state.running[issueID]
	if !ok {
		return
	}
	entry.Session.SessionID = firstNonEmpty(event.SessionID, entry.Session.SessionID)
	entry.Session.Transport = firstNonEmpty(entry.Session.Transport, orchestrator.config.Copilot.Transport)
	if event.CopilotCLIPID != nil {
		entry.Session.CopilotCLIPID = event.CopilotCLIPID
	}
	entry.Session.LastAgentEvent = event.Event
	entry.Session.LastAgentMessage = event.Message
	entry.Session.TurnCount = max(entry.Session.TurnCount, event.Turn)
	timestamp := event.Timestamp
	entry.Session.LastAgentTimestamp = &timestamp
	if event.Usage != nil {
		deltaInput := max(event.Usage.InputTokens-entry.Session.LastReportedInputTokens, 0)
		deltaOutput := max(event.Usage.OutputTokens-entry.Session.LastReportedOutputTokens, 0)
		deltaTotal := max(event.Usage.TotalTokens-entry.Session.LastReportedTotalTokens, 0)
		orchestrator.state.copilotTotals.InputTokens += deltaInput
		orchestrator.state.copilotTotals.OutputTokens += deltaOutput
		orchestrator.state.copilotTotals.TotalTokens += deltaTotal
		entry.Session.CopilotInputTokens = event.Usage.InputTokens
		entry.Session.CopilotOutputTokens = event.Usage.OutputTokens
		entry.Session.CopilotTotalTokens = event.Usage.TotalTokens
		entry.Session.LastReportedInputTokens = event.Usage.InputTokens
		entry.Session.LastReportedOutputTokens = event.Usage.OutputTokens
		entry.Session.LastReportedTotalTokens = event.Usage.TotalTokens
	}
	if event.RateLimits != nil {
		entry.Session.LastRateLimits = event.RateLimits
		orchestrator.state.copilotRateLimits = event.RateLimits
	}
	entry.RecentEvents = append(entry.RecentEvents, IssueEvent{At: event.Timestamp, Event: event.Event, Message: event.Message})
	if len(entry.RecentEvents) > 25 {
		entry.RecentEvents = entry.RecentEvents[len(entry.RecentEvents)-25:]
	}
}

func (orchestrator *Orchestrator) Snapshot() Snapshot {
	orchestrator.mu.Lock()
	defer orchestrator.mu.Unlock()
	now := time.Now().UTC()
	running := make([]RunningSnapshot, 0, len(orchestrator.state.running))
	for _, entry := range orchestrator.state.running {
		running = append(running, runningSnapshotForEntry(entry))
	}
	retrying := make([]RetrySnapshot, 0, len(orchestrator.state.retryAttempts))
	for _, retry := range orchestrator.state.retryAttempts {
		retrying = append(retrying, RetrySnapshot{IssueID: retry.entry.IssueID, Identifier: retry.entry.Identifier, Attempt: retry.entry.Attempt, DueAt: retry.entry.DueAt, Error: retry.entry.Error})
	}
	totals := orchestrator.state.copilotTotals
	for _, entry := range orchestrator.state.running {
		totals.SecondsRunning += now.Sub(entry.StartedAt).Seconds()
	}
	sort.SliceStable(running, func(left int, right int) bool { return running[left].Identifier < running[right].Identifier })
	sort.SliceStable(retrying, func(left int, right int) bool { return retrying[left].Identifier < retrying[right].Identifier })
	return Snapshot{
		GeneratedAt: now,
		Counts: SnapshotCounts{Running: len(running), Retrying: len(retrying)},
		Running: running,
		Retrying: retrying,
		CopilotTotals: totals,
		RateLimits: orchestrator.state.copilotRateLimits,
	}
}

func (orchestrator *Orchestrator) IssueDetail(issueIdentifier string) (IssueDetail, bool) {
	orchestrator.mu.Lock()
	defer orchestrator.mu.Unlock()
	for _, entry := range orchestrator.state.running {
		if entry.Issue.Identifier == issueIdentifier {
			detail := IssueDetail{
				IssueIdentifier: issueIdentifier,
				IssueID: entry.Issue.ID,
				Status: "running",
				Workspace: map[string]string{"path": entry.WorkspacePath},
				Attempts: map[string]int{"restart_count": entry.RestartCount, "current_retry_attempt": entry.RetryAttempt},
				Running: ptrRunningSnapshot(runningSnapshotForEntry(entry)),
				RecentEvents: append([]IssueEvent(nil), entry.RecentEvents...),
				Tracked: map[string]any{},
			}
			if entry.LastError != "" {
				lastError := entry.LastError
				detail.LastError = &lastError
			}
			return detail, true
		}
	}
	for _, retry := range orchestrator.state.retryAttempts {
		if retry.entry.Identifier == issueIdentifier {
			detail := IssueDetail{
				IssueIdentifier: issueIdentifier,
				IssueID: retry.entry.IssueID,
				Status: "retrying",
				Retry: &RetrySnapshot{IssueID: retry.entry.IssueID, Identifier: retry.entry.Identifier, Attempt: retry.entry.Attempt, DueAt: retry.entry.DueAt, Error: retry.entry.Error},
				Tracked: map[string]any{},
			}
			if retry.entry.Error != "" {
				lastError := retry.entry.Error
				detail.LastError = &lastError
			}
			return detail, true
		}
	}
	return IssueDetail{}, false
}

func (orchestrator *Orchestrator) shutdown(ctx context.Context) error {
	orchestrator.mu.Lock()
	for _, retry := range orchestrator.state.retryAttempts {
		retry.timer.Stop()
	}
	for _, entry := range orchestrator.state.running {
		entry.StopReason = "shutdown"
		if entry.Cancel != nil {
			entry.Cancel()
		}
	}
	orchestrator.mu.Unlock()
	if orchestrator.server != nil {
		_ = orchestrator.server.shutdown(ctx)
	}
	finished := make(chan struct{})
	go func() {
		orchestrator.wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func runningSnapshotForEntry(entry *runningEntry) RunningSnapshot {
	return RunningSnapshot{
		IssueID:       entry.Issue.ID,
		Identifier:    entry.Issue.Identifier,
		State:         entry.Issue.State,
		SessionID:     entry.Session.SessionID,
		TurnCount:     entry.Session.TurnCount,
		LastEvent:     entry.Session.LastAgentEvent,
		LastMessage:   entry.Session.LastAgentMessage,
		StartedAt:     entry.StartedAt,
		LastEventAt:   entry.Session.LastAgentTimestamp,
		Tokens:        copilot.UsageTotals{InputTokens: entry.Session.CopilotInputTokens, OutputTokens: entry.Session.CopilotOutputTokens, TotalTokens: entry.Session.CopilotTotalTokens},
		WorkspacePath: entry.WorkspacePath,
		RestartCount:  entry.RestartCount,
		LastError:     entry.LastError,
	}
}

func ptrRunningSnapshot(snapshot RunningSnapshot) *RunningSnapshot {
	return &snapshot
}

func sortForDispatch(issues []model.Issue) {
	sort.SliceStable(issues, func(left int, right int) bool {
		leftPriority := priorityValue(issues[left].Priority)
		rightPriority := priorityValue(issues[right].Priority)
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		if issues[left].CreatedAt != nil && issues[right].CreatedAt != nil && !issues[left].CreatedAt.Equal(*issues[right].CreatedAt) {
			return issues[left].CreatedAt.Before(*issues[right].CreatedAt)
		}
		if issues[left].CreatedAt != nil && issues[right].CreatedAt == nil {
			return true
		}
		if issues[left].CreatedAt == nil && issues[right].CreatedAt != nil {
			return false
		}
		return issues[left].Identifier < issues[right].Identifier
	})
}

func priorityValue(priority *int) int {
	if priority == nil {
		return math.MaxInt32
	}
	return *priority
}

func containsNormalized(values []string, candidate string) bool {
	normalizedCandidate := strings.ToLower(strings.TrimSpace(candidate))
	for _, value := range values {
		if strings.ToLower(strings.TrimSpace(value)) == normalizedCandidate {
			return true
		}
	}
	return false
}

func issueHasActiveBlockers(issue model.Issue, terminalStates []string) bool {
	for _, blocker := range issue.BlockedBy {
		if blocker.State == nil {
			return true
		}
		if !containsNormalized(terminalStates, *blocker.State) {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func max(value int, other int) int {
	if value > other {
		return value
	}
	return other
}