package runstate

import (
	"context"
	"encoding/json"
	"time"
)

type Status string

const (
	StatusQueued  Status = "queued"
	StatusRunning Status = "running"
	StatusSuccess Status = "success"
	StatusFailed  Status = "failed"
)

type Writer interface {
	CreateRun(ctx context.Context, params CreateRunParams) (int64, error)
	UpdateRun(ctx context.Context, params UpdateRunParams) error
	InsertAuditEvent(ctx context.Context, event AuditEvent) error
}

type HistoryReader interface {
	ListRuns(ctx context.Context, limit int) ([]RunRecord, error)
	GetRun(ctx context.Context, runID int64) (RunDetail, bool, error)
}

type Store interface {
	Writer
	HistoryReader
	Close() error
}

type CreateRunParams struct {
	IssueID   string
	Repo      string
	Status    Status
	StartTime time.Time
}

type UpdateRunParams struct {
	RunID        int64
	Status       Status
	EndTime      *time.Time
	ErrorMessage *string
}

type AuditEvent struct {
	RunID      int64
	Timestamp  time.Time
	ActionType string
	Payload    string
}

type RunRecord struct {
	ID           int64      `json:"id"`
	IssueID      string     `json:"issue_id"`
	Repo         string     `json:"repo"`
	Status       Status     `json:"status"`
	StartTime    time.Time  `json:"start_time"`
	EndTime      *time.Time `json:"end_time,omitempty"`
	ErrorMessage *string    `json:"error_message,omitempty"`
}

type AuditEventRecord struct {
	ID         int64           `json:"id"`
	RunID      int64           `json:"run_id"`
	Timestamp  time.Time       `json:"timestamp"`
	ActionType string          `json:"action_type"`
	Payload    json.RawMessage `json:"payload"`
}

type RunDetail struct {
	RunRecord
	AuditEvents []AuditEventRecord `json:"audit_events"`
}

type Metadata struct {
	RunID   int64
	IssueID string
	Repo    string
}

type metadataKey struct{}

type NopStore struct{}

func (NopStore) CreateRun(context.Context, CreateRunParams) (int64, error) {
	return 0, nil
}

func (NopStore) UpdateRun(context.Context, UpdateRunParams) error {
	return nil
}

func (NopStore) InsertAuditEvent(context.Context, AuditEvent) error {
	return nil
}

func (NopStore) ListRuns(context.Context, int) ([]RunRecord, error) {
	return []RunRecord{}, nil
}

func (NopStore) GetRun(context.Context, int64) (RunDetail, bool, error) {
	return RunDetail{}, false, nil
}

func (NopStore) Close() error {
	return nil
}

func WithMetadata(ctx context.Context, metadata Metadata) context.Context {
	return context.WithValue(ctx, metadataKey{}, metadata)
}

func MetadataFromContext(ctx context.Context) (Metadata, bool) {
	metadata, ok := ctx.Value(metadataKey{}).(Metadata)
	if !ok {
		return Metadata{}, false
	}
	return metadata, true
}

func RecordAuditEvent(ctx context.Context, writer Writer, actionType string, payload any) error {
	if writer == nil {
		return nil
	}
	metadata, ok := MetadataFromContext(ctx)
	if !ok || metadata.RunID <= 0 {
		return nil
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return writer.InsertAuditEvent(ctx, AuditEvent{
		RunID:      metadata.RunID,
		Timestamp:  time.Now().UTC(),
		ActionType: actionType,
		Payload:    string(encodedPayload),
	})
}
