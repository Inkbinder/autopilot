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
}
