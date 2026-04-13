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

type Store interface {
	Writer
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
