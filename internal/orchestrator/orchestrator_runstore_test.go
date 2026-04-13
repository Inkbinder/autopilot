package orchestrator

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Inkbinder/autopilot/internal/model"
	"github.com/Inkbinder/autopilot/internal/runstate"
)

type runStoreRecorder struct {
	mu      sync.Mutex
	creates []runstate.CreateRunParams
	updates []runstate.UpdateRunParams
}

func (recorder *runStoreRecorder) CreateRun(_ context.Context, params runstate.CreateRunParams) (int64, error) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.creates = append(recorder.creates, params)
	return int64(len(recorder.creates)), nil
}

func (recorder *runStoreRecorder) UpdateRun(_ context.Context, params runstate.UpdateRunParams) error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.updates = append(recorder.updates, params)
	return nil
}

func (recorder *runStoreRecorder) InsertAuditEvent(context.Context, runstate.AuditEvent) error {
	return nil
}

func (recorder *runStoreRecorder) snapshot() ([]runstate.CreateRunParams, []runstate.UpdateRunParams) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	creates := append([]runstate.CreateRunParams(nil), recorder.creates...)
	updates := append([]runstate.UpdateRunParams(nil), recorder.updates...)
	return creates, updates
}

func TestOrchestratorRecordsSuccessfulRunLifecycle(t *testing.T) {
	t.Parallel()
	workflowPath := writeWorkflowFile(t)
	issue := model.Issue{ID: "1", Identifier: "octo/widgets#1", Title: "Task", State: "Open", Labels: []string{"autopilot:ready"}, BlockedBy: []model.BlockerRef{{Identifier: stringPtr("octo/widgets#2"), State: stringPtr("Closed")}}}
	tracker := &fakeTracker{candidates: []model.Issue{issue}, statesByID: map[string]model.Issue{"1": {ID: "1", Identifier: issue.Identifier, Title: issue.Title, State: "Closed"}}}
	workspace := &fakeWorkspace{root: filepath.Join(t.TempDir(), "workspaces")}
	copilotClient := &fakeCopilot{}
	store := &runStoreRecorder{}
	orchestrator, err := New(workflowPath, Options{Builder: fakeBuilder{tracker: tracker, workspace: workspace, copilot: copilotClient}, RunStore: store})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	orchestrator.tick(context.Background())
	orchestrator.wg.Wait()
	defer orchestrator.shutdown(context.Background())

	creates, updates := store.snapshot()
	if len(creates) != 1 {
		t.Fatalf("create count = %d, want 1", len(creates))
	}
	if creates[0].IssueID != issue.ID || creates[0].Repo != "octo/widgets" || creates[0].Status != runstate.StatusQueued {
		t.Fatalf("unexpected create params: %#v", creates[0])
	}
	if len(updates) < 2 {
		t.Fatalf("update count = %d, want at least 2", len(updates))
	}
	if updates[0].Status != runstate.StatusRunning {
		t.Fatalf("first status = %q, want running", updates[0].Status)
	}
	last := updates[len(updates)-1]
	if last.Status != runstate.StatusSuccess {
		t.Fatalf("last status = %q, want success", last.Status)
	}
	if last.EndTime == nil {
		t.Fatal("expected success update to set EndTime")
	}
}

func TestOrchestratorRecordsFailedRunLifecycle(t *testing.T) {
	t.Parallel()
	workflowPath := writeWorkflowFileWithMaxTurns(t, 3)
	issue := model.Issue{ID: "1", Identifier: "octo/widgets#1", Title: "Task", State: "Open", Labels: []string{"autopilot:merging"}}
	tracker := &fakeTracker{candidates: []model.Issue{issue}, statesByID: map[string]model.Issue{"1": issue}}
	workspace := &fakeWorkspace{root: filepath.Join(t.TempDir(), "workspaces")}
	store := &runStoreRecorder{}
	orchestrator, err := New(workflowPath, Options{Builder: fakeBuilder{tracker: tracker, workspace: workspace, copilot: &fakeCopilot{silent: true}}, RunStore: store})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	orchestrator.tick(context.Background())
	orchestrator.wg.Wait()
	defer orchestrator.shutdown(context.Background())

	_, updates := store.snapshot()
	if len(updates) < 2 {
		t.Fatalf("update count = %d, want at least 2", len(updates))
	}
	last := updates[len(updates)-1]
	if last.Status != runstate.StatusFailed {
		t.Fatalf("last status = %q, want failed", last.Status)
	}
	if last.ErrorMessage == nil || *last.ErrorMessage != "stalled session" {
		t.Fatalf("last error = %#v, want stalled session", last.ErrorMessage)
	}
	if last.EndTime == nil {
		t.Fatal("expected failed update to set EndTime")
	}
}
