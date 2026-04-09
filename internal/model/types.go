package model

import (
	"strings"
	"time"
)

type BlockerRef struct {
	ID         *string `json:"id,omitempty"`
	Identifier *string `json:"identifier,omitempty"`
	State      *string `json:"state,omitempty"`
}

type Issue struct {
	ID          string       `json:"id"`
	Identifier  string       `json:"identifier"`
	Title       string       `json:"title"`
	Description *string      `json:"description,omitempty"`
	Priority    *int         `json:"priority,omitempty"`
	State       string       `json:"state"`
	BranchName  *string      `json:"branch_name,omitempty"`
	URL         *string      `json:"url,omitempty"`
	Labels      []string     `json:"labels,omitempty"`
	BlockedBy   []BlockerRef `json:"blocked_by,omitempty"`
	CreatedAt   *time.Time   `json:"created_at,omitempty"`
	UpdatedAt   *time.Time   `json:"updated_at,omitempty"`
}

func (issue Issue) NormalizedState() string {
	return strings.ToLower(strings.TrimSpace(issue.State))
}

type Workspace struct {
	Path         string `json:"path"`
	WorkspaceKey string `json:"workspace_key"`
	CreatedNow   bool   `json:"created_now"`
}

type RunAttempt struct {
	IssueID         string    `json:"issue_id"`
	IssueIdentifier string    `json:"issue_identifier"`
	Attempt         *int      `json:"attempt,omitempty"`
	WorkspacePath   string    `json:"workspace_path"`
	StartedAt       time.Time `json:"started_at"`
	Status          string    `json:"status"`
	Error           string    `json:"error,omitempty"`
}

type LiveSession struct {
	SessionID                string     `json:"session_id"`
	Transport                string     `json:"transport"`
	CopilotCLIPID            *int       `json:"copilot_cli_pid,omitempty"`
	LastAgentEvent           string     `json:"last_agent_event,omitempty"`
	LastAgentTimestamp       *time.Time `json:"last_agent_timestamp,omitempty"`
	LastAgentMessage         string     `json:"last_agent_message,omitempty"`
	CopilotInputTokens       int        `json:"copilot_input_tokens"`
	CopilotOutputTokens      int        `json:"copilot_output_tokens"`
	CopilotTotalTokens       int        `json:"copilot_total_tokens"`
	LastReportedInputTokens  int        `json:"last_reported_input_tokens"`
	LastReportedOutputTokens int        `json:"last_reported_output_tokens"`
	LastReportedTotalTokens  int        `json:"last_reported_total_tokens"`
	TurnCount                int        `json:"turn_count"`
	LastRateLimits           any        `json:"last_rate_limits,omitempty"`
}

type RetryEntry struct {
	IssueID    string    `json:"issue_id"`
	Identifier string    `json:"identifier"`
	Attempt    int       `json:"attempt"`
	DueAt      time.Time `json:"due_at"`
	Error      string    `json:"error,omitempty"`
}

type CopilotTotals struct {
	InputTokens   int     `json:"input_tokens"`
	OutputTokens  int     `json:"output_tokens"`
	TotalTokens   int     `json:"total_tokens"`
	SecondsRunning float64 `json:"seconds_running"`
}