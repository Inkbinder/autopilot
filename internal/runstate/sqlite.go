package runstate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type SQLiteStore struct {
	db *sql.DB
}

func OpenSQLite(path string) (*SQLiteStore, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", absPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &SQLiteStore{db: db}
	if err := store.initialize(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (store *SQLiteStore) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	return store.db.Close()
}

func (store *SQLiteStore) CreateRun(ctx context.Context, params CreateRunParams) (int64, error) {
	result, err := store.db.ExecContext(
		ctx,
		`INSERT INTO runs (issue_id, repo, status, start_time) VALUES (?, ?, ?, ?)`,
		strings.TrimSpace(params.IssueID),
		strings.TrimSpace(params.Repo),
		string(params.Status),
		params.StartTime.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (store *SQLiteStore) UpdateRun(ctx context.Context, params UpdateRunParams) error {
	if params.RunID <= 0 {
		return nil
	}
	var endTime any
	if params.EndTime != nil {
		endTime = params.EndTime.UTC().Format(time.RFC3339Nano)
	}
	var errorMessage any
	if params.ErrorMessage != nil {
		errorMessage = strings.TrimSpace(*params.ErrorMessage)
	}
	_, err := store.db.ExecContext(
		ctx,
		`UPDATE runs SET status = ?, end_time = ?, error_message = ? WHERE id = ?`,
		string(params.Status),
		endTime,
		errorMessage,
		params.RunID,
	)
	return err
}

func (store *SQLiteStore) InsertAuditEvent(ctx context.Context, event AuditEvent) error {
	if event.RunID <= 0 {
		return nil
	}
	_, err := store.db.ExecContext(
		ctx,
		`INSERT INTO audit_events (run_id, timestamp, action_type, payload) VALUES (?, ?, ?, ?)`,
		event.RunID,
		event.Timestamp.UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(event.ActionType),
		event.Payload,
	)
	return err
}

func (store *SQLiteStore) ListRuns(ctx context.Context, limit int) ([]RunRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := store.db.QueryContext(
		ctx,
		`SELECT id, issue_id, repo, status, start_time, end_time, error_message
		 FROM runs
		 ORDER BY start_time DESC, id DESC
		 LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	runs := make([]RunRecord, 0, limit)
	for rows.Next() {
		run, err := scanRunRecord(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return runs, nil
}

func (store *SQLiteStore) GetRun(ctx context.Context, runID int64) (RunDetail, bool, error) {
	if runID <= 0 {
		return RunDetail{}, false, nil
	}
	row := store.db.QueryRowContext(
		ctx,
		`SELECT id, issue_id, repo, status, start_time, end_time, error_message
		 FROM runs
		 WHERE id = ?`,
		runID,
	)
	run, err := scanRunRecord(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return RunDetail{}, false, nil
		}
		return RunDetail{}, false, err
	}

	eventRows, err := store.db.QueryContext(
		ctx,
		`SELECT id, run_id, timestamp, action_type, payload
		 FROM audit_events
		 WHERE run_id = ?
		 ORDER BY timestamp ASC, id ASC`,
		runID,
	)
	if err != nil {
		return RunDetail{}, false, err
	}
	defer eventRows.Close()

	events := make([]AuditEventRecord, 0)
	for eventRows.Next() {
		var (
			id         int64
			storedRun  int64
			timestamp  string
			actionType string
			payload    string
		)
		if err := eventRows.Scan(&id, &storedRun, &timestamp, &actionType, &payload); err != nil {
			return RunDetail{}, false, err
		}
		parsedTimestamp, err := parseStoredTime(timestamp)
		if err != nil {
			return RunDetail{}, false, err
		}
		events = append(events, AuditEventRecord{
			ID:         id,
			RunID:      storedRun,
			Timestamp:  parsedTimestamp,
			ActionType: strings.TrimSpace(actionType),
			Payload:    payloadJSON(payload),
		})
	}
	if err := eventRows.Err(); err != nil {
		return RunDetail{}, false, err
	}
	return RunDetail{RunRecord: run, AuditEvents: events}, true, nil
}

func (store *SQLiteStore) initialize(ctx context.Context) error {
	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA journal_mode = WAL`,
		`PRAGMA busy_timeout = 5000`,
		`CREATE TABLE IF NOT EXISTS runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id TEXT NOT NULL,
			repo TEXT NOT NULL,
			status TEXT NOT NULL CHECK(status IN ('queued', 'running', 'success', 'failed')),
			start_time TEXT NOT NULL,
			end_time TEXT,
			error_message TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS runs_issue_repo_started_idx ON runs (issue_id, repo, start_time DESC)`,
		`CREATE TABLE IF NOT EXISTS audit_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id INTEGER NOT NULL,
			timestamp TEXT NOT NULL,
			action_type TEXT NOT NULL,
			payload TEXT NOT NULL,
			FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS audit_events_run_timestamp_idx ON audit_events (run_id, timestamp)`,
	}
	for _, statement := range statements {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize sqlite state store: %w", err)
		}
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRunRecord(scanner rowScanner) (RunRecord, error) {
	var (
		run         RunRecord
		status      string
		startTime   string
		endTime     sql.NullString
		errorString sql.NullString
	)
	if err := scanner.Scan(&run.ID, &run.IssueID, &run.Repo, &status, &startTime, &endTime, &errorString); err != nil {
		return RunRecord{}, err
	}
	parsedStartTime, err := parseStoredTime(startTime)
	if err != nil {
		return RunRecord{}, err
	}
	run.Status = Status(strings.TrimSpace(status))
	run.StartTime = parsedStartTime
	if endTime.Valid {
		parsedEndTime, err := parseStoredTime(endTime.String)
		if err != nil {
			return RunRecord{}, err
		}
		run.EndTime = &parsedEndTime
	}
	if errorString.Valid {
		trimmed := strings.TrimSpace(errorString.String)
		run.ErrorMessage = &trimmed
	}
	return run, nil
}

func parseStoredTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
}

func payloadJSON(value string) json.RawMessage {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return json.RawMessage(`null`)
	}
	raw := []byte(trimmed)
	if json.Valid(raw) {
		return json.RawMessage(append([]byte(nil), raw...))
	}
	encoded, err := json.Marshal(trimmed)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return json.RawMessage(encoded)
}
