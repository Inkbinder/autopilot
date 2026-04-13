package runstate

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteStoreCreateUpdateAndAudit(t *testing.T) {
	t.Parallel()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), ".autopilot", "runs.db"))
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	defer store.Close()

	startedAt := time.Now().UTC().Truncate(time.Second)
	runID, err := store.CreateRun(context.Background(), CreateRunParams{IssueID: "1", Repo: "octo/widgets", Status: StatusQueued, StartTime: startedAt})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	if runID <= 0 {
		t.Fatalf("CreateRun() runID = %d, want > 0", runID)
	}

	finishedAt := startedAt.Add(2 * time.Minute)
	errorMessage := "copilot rate limited"
	if err := store.UpdateRun(context.Background(), UpdateRunParams{RunID: runID, Status: StatusFailed, EndTime: &finishedAt, ErrorMessage: &errorMessage}); err != nil {
		t.Fatalf("UpdateRun() error = %v", err)
	}
	if err := store.InsertAuditEvent(context.Background(), AuditEvent{RunID: runID, Timestamp: finishedAt, ActionType: "llm_prompt", Payload: `{"prompt":"do work"}`}); err != nil {
		t.Fatalf("InsertAuditEvent() error = %v", err)
	}

	var status string
	var endTime sql.NullString
	var storedError sql.NullString
	if err := store.db.QueryRow(`SELECT status, end_time, error_message FROM runs WHERE id = ?`, runID).Scan(&status, &endTime, &storedError); err != nil {
		t.Fatalf("QueryRow(runs) error = %v", err)
	}
	if status != string(StatusFailed) {
		t.Fatalf("status = %q, want %q", status, StatusFailed)
	}
	if !endTime.Valid {
		t.Fatal("expected end_time to be set")
	}
	if !storedError.Valid || storedError.String != errorMessage {
		t.Fatalf("error_message = %#v, want %q", storedError, errorMessage)
	}

	var actionType string
	var payload string
	if err := store.db.QueryRow(`SELECT action_type, payload FROM audit_events WHERE run_id = ?`, runID).Scan(&actionType, &payload); err != nil {
		t.Fatalf("QueryRow(audit_events) error = %v", err)
	}
	if actionType != "llm_prompt" {
		t.Fatalf("action_type = %q, want llm_prompt", actionType)
	}
	if payload != `{"prompt":"do work"}` {
		t.Fatalf("payload = %q", payload)
	}

	runs, err := store.ListRuns(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("ListRuns() length = %d, want 1", len(runs))
	}
	if runs[0].ID != runID || runs[0].Status != StatusFailed {
		t.Fatalf("ListRuns() first run = %#v", runs[0])
	}

	detail, ok, err := store.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if !ok {
		t.Fatal("GetRun() ok = false, want true")
	}
	if detail.ID != runID {
		t.Fatalf("GetRun() id = %d, want %d", detail.ID, runID)
	}
	if len(detail.AuditEvents) != 1 {
		t.Fatalf("GetRun() audit event count = %d, want 1", len(detail.AuditEvents))
	}
	if string(detail.AuditEvents[0].Payload) != `{"prompt":"do work"}` {
		t.Fatalf("GetRun() payload = %s", detail.AuditEvents[0].Payload)
	}
}
