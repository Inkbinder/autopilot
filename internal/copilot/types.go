package copilot

import (
	"context"
	"time"

	"autopilot/internal/workflow"
)

const DefaultContinuationPrompt = "Continue in the existing session. Do not repeat the original prompt. Review the current workspace state and keep advancing the issue."

type UsageTotals struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type Event struct {
	Event          string       `json:"event"`
	Timestamp      time.Time    `json:"timestamp"`
	SessionID      string       `json:"session_id,omitempty"`
	CopilotCLIPID  *int         `json:"copilot_cli_pid,omitempty"`
	Message        string       `json:"message,omitempty"`
	Usage          *UsageTotals `json:"usage,omitempty"`
	RateLimits     any          `json:"rate_limits,omitempty"`
	Raw            map[string]any `json:"raw,omitempty"`
	Turn           int          `json:"turn,omitempty"`
}

type EventHandler func(Event)

type StartRequest struct {
	WorkspacePath string
	Copilot       workflow.CopilotConfig
	OnEvent       EventHandler
}

type Client interface {
	StartSession(ctx context.Context, request StartRequest) (Session, error)
}

type Session interface {
	ID() string
	Transport() string
	ProcessID() *int
	RunPrompt(ctx context.Context, prompt string, turn int) error
	Close(ctx context.Context) error
}