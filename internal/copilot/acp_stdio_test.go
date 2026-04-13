package copilot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Inkbinder/autopilot/internal/workflow"
	"github.com/Inkbinder/autopilot/internal/workspace"
)

func TestACPStdioClientSessionAndPrompt(t *testing.T) {
	t.Parallel()
	workspacePath := t.TempDir()
	script := writeExecutableScript(t, `#!/usr/bin/env bash
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*)
      echo '{"id":1,"result":{"ok":true}}'
      ;;
		*'"method":"session/new"'*)
      echo '{"id":2,"result":{"sessionId":"session-1"}}'
      ;;
		*'"method":"session/prompt"'*)
			echo '{"method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"working"}}}}'
			echo '{"id":3,"result":{"stopReason":"end_turn","input_tokens":10,"output_tokens":5,"total_tokens":15}}'
      ;;
  esac
done
`)
	provider := newLocalStreamProvider(t, workspacePath)
	client, err := NewClientWithOptions(workflow.Config{Copilot: workflow.CopilotConfig{Transport: "acp_stdio"}}, ClientOptions{StreamExecutor: provider})
	if err != nil {
		t.Fatalf("NewClientWithOptions() error = %v", err)
	}
	var (
		mu     sync.Mutex
		events []Event
	)
	session, err := client.StartSession(context.Background(), StartRequest{
		WorkspacePath: workspacePath,
		Copilot: workflow.CopilotConfig{
			Command:        script,
			Transport:      "acp_stdio",
			StartupTimeout: time.Second,
		},
		OnEvent: func(event Event) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer session.Close(context.Background())
	if session.ID() != "session-1" {
		t.Fatalf("session ID = %q, want session-1", session.ID())
	}
	if err := session.RunPrompt(context.Background(), "do work", 1); err != nil {
		t.Fatalf("RunPrompt() error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) == 0 {
		t.Fatal("expected session events")
	}
	var sawSessionStarted bool
	var sawUsage bool
	for _, event := range events {
		if event.Event == "session_started" {
			sawSessionStarted = true
		}
		if event.Usage != nil && event.Usage.TotalTokens == 15 {
			sawUsage = true
		}
	}
	if !sawSessionStarted {
		t.Fatal("expected session_started event")
	}
	if !sawUsage {
		t.Fatal("expected token usage event")
	}
}

func TestACPStdioClientInputRequiredFails(t *testing.T) {
	t.Parallel()
	workspacePath := t.TempDir()
	script := writeExecutableScript(t, `#!/usr/bin/env bash
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*)
      echo '{"id":1,"result":{"ok":true}}'
      ;;
		*'"method":"session/new"'*)
      echo '{"id":2,"result":{"sessionId":"session-2"}}'
      ;;
		*'"method":"session/prompt"'*)
      echo '{"method":"userInputRequired","params":{"message":"need input"}}'
      ;;
  esac
done
`)
	provider := newLocalStreamProvider(t, workspacePath)
	client, err := NewClientWithOptions(workflow.Config{Copilot: workflow.CopilotConfig{Transport: "acp_stdio"}}, ClientOptions{StreamExecutor: provider})
	if err != nil {
		t.Fatalf("NewClientWithOptions() error = %v", err)
	}
	session, err := client.StartSession(context.Background(), StartRequest{
		WorkspacePath: workspacePath,
		Copilot: workflow.CopilotConfig{
			Command:        script,
			Transport:      "acp_stdio",
			StartupTimeout: time.Second,
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer session.Close(context.Background())
	err = session.RunPrompt(context.Background(), "need help", 1)
	if err == nil {
		t.Fatal("RunPrompt() error = nil, want error")
	}
	var typedErr *Error
	if !errors.As(err, &typedErr) {
		t.Fatalf("expected typed copilot error, got %T", err)
	}
	if typedErr.Code != ErrPromptInputRequired && typedErr.Code != ErrPromptFailed {
		t.Fatalf("unexpected error code %s", typedErr.Code)
	}
}

func TestACPStdioClientAutoApprovesPermissionRequest(t *testing.T) {
	t.Parallel()
	workspacePath := t.TempDir()
	script := writeExecutableScript(t, `#!/usr/bin/env bash
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*)
      echo '{"id":1,"result":{"ok":true}}'
      ;;
    *'"method":"session/new"'*)
      echo '{"id":2,"result":{"sessionId":"session-3"}}'
      ;;
    *'"method":"session/prompt"'*)
      echo '{"id":99,"method":"session/request_permission","params":{"sessionId":"session-3","toolCall":{"toolCallId":"call-1"},"options":[{"optionId":"allow-once","name":"Allow once","kind":"allow_once"},{"optionId":"reject-once","name":"Reject","kind":"reject_once"}]}}'
      ;;
	   *'"id":99'*'"optionId":"allow-once"'*'"selected"'*)
      echo '{"id":3,"result":{"stopReason":"end_turn"}}'
      ;;
  esac
done
`)
	provider := newLocalStreamProvider(t, workspacePath)
	client, err := NewClientWithOptions(workflow.Config{Copilot: workflow.CopilotConfig{Transport: "acp_stdio"}}, ClientOptions{StreamExecutor: provider})
	if err != nil {
		t.Fatalf("NewClientWithOptions() error = %v", err)
	}
	session, err := client.StartSession(context.Background(), StartRequest{
		WorkspacePath: workspacePath,
		Copilot: workflow.CopilotConfig{
			Command:        script,
			Transport:      "acp_stdio",
			StartupTimeout: time.Second,
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer session.Close(context.Background())
	if err := session.RunPrompt(context.Background(), "do work", 1); err != nil {
		t.Fatalf("RunPrompt() error = %v", err)
	}
}

func writeExecutableScript(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-copilot.sh")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func newLocalStreamProvider(t *testing.T, workspacePath string) *workspace.LocalProvider {
	t.Helper()
	provider, err := workspace.NewLocalProvider(workflow.WorkspaceConfig{Root: workspacePath})
	if err != nil {
		t.Fatalf("NewLocalProvider() error = %v", err)
	}
	return provider
}
