package runstate

import (
	"context"
	"database/sql"
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
