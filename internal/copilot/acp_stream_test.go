package copilot

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Inkbinder/autopilot/internal/workflow"
	"github.com/Inkbinder/autopilot/internal/workspace"
)

type fakeStreamExecutor struct {
	mu               sync.Mutex
	workingDir       string
	executeCalls     []streamExecuteCall
	expectedPrompt   string
	expectedCWD      string
	sessionID        string
	completionResult map[string]any
}

type streamExecuteCall struct {
	Command string
	Args    []string
	Dir     string
}

func (executor *fakeStreamExecutor) ExecuteStream(_ context.Context, command string, args []string, dir string) (workspace.ExecutionStream, error) {
	executor.mu.Lock()
	executor.executeCalls = append(executor.executeCalls, streamExecuteCall{Command: command, Args: append([]string(nil), args...), Dir: dir})
	executor.mu.Unlock()
	clientConn, serverConn := net.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()
	waitCh := make(chan error, 1)
	go func() {
		defer close(waitCh)
		defer stdoutWriter.Close()
		defer stderrWriter.Close()
		scanner := bufio.NewScanner(serverConn)
		for scanner.Scan() {
			var envelope map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
				waitCh <- err
				return
			}
			method, _ := envelope["method"].(string)
			requestID := int(envelope["id"].(float64))
			switch method {
			case "initialize":
				_, _ = io.WriteString(stdoutWriter, `{"id":1,"result":{"ok":true}}`+"\n")
			case "session/new":
				params := envelope["params"].(map[string]any)
				if params["cwd"] != executor.expectedCWD {
					waitCh <- io.ErrUnexpectedEOF
					return
				}
				_, _ = io.WriteString(stdoutWriter, `{"id":`+itoa(requestID)+`,"result":{"sessionId":"`+executor.sessionID+`"}}`+"\n")
			case "session/prompt":
				params := envelope["params"].(map[string]any)
				prompt := params["prompt"].([]any)[0].(map[string]any)["text"]
				if prompt != executor.expectedPrompt {
					waitCh <- io.ErrUnexpectedEOF
					return
				}
				payload, _ := json.Marshal(map[string]any{"id": requestID, "result": executor.completionResult})
				_, _ = stdoutWriter.Write(append(payload, '\n'))
				waitCh <- nil
				return
			}
		}
		waitCh <- scanner.Err()
	}()
	return &fakeExecutionStream{
		conn:       clientConn,
		stdout:     stdoutReader,
		stderr:     stderrReader,
		workingDir: executor.workingDir,
		waitFn: func() error {
			return <-waitCh
		},
		closeFn: func() error {
			_ = clientConn.Close()
			_ = serverConn.Close()
			return nil
		},
	}, nil
}

type fakeExecutionStream struct {
	conn       net.Conn
	stdout     io.Reader
	stderr     io.Reader
	workingDir string
	waitFn     func() error
	closeFn    func() error
}

func (stream *fakeExecutionStream) Conn() net.Conn     { return stream.conn }
func (stream *fakeExecutionStream) Stdout() io.Reader  { return stream.stdout }
func (stream *fakeExecutionStream) Stderr() io.Reader  { return stream.stderr }
func (stream *fakeExecutionStream) WorkingDir() string { return stream.workingDir }
func (stream *fakeExecutionStream) ProcessID() *int    { return nil }
func (stream *fakeExecutionStream) Wait() error        { return stream.waitFn() }
func (stream *fakeExecutionStream) Close() error       { return stream.closeFn() }

func TestACPStdioClientUsesExecutionStreamWorkingDir(t *testing.T) {
	t.Parallel()
	workspacePath := t.TempDir()
	executor := &fakeStreamExecutor{
		workingDir:     filepath.Join("/workspace", "issue-1"),
		expectedCWD:    filepath.Join("/workspace", "issue-1"),
		expectedPrompt: "do work",
		sessionID:      "stream-session",
		completionResult: map[string]any{
			"stopReason":    "end_turn",
			"input_tokens":  10,
			"output_tokens": 5,
			"total_tokens":  15,
		},
	}
	client, err := NewClientWithOptions(workflow.Config{Copilot: workflow.CopilotConfig{Transport: "acp_stdio"}}, ClientOptions{StreamExecutor: executor})
	if err != nil {
		t.Fatalf("NewClientWithOptions() error = %v", err)
	}
	var events []Event
	var mu sync.Mutex
	session, err := client.StartSession(context.Background(), StartRequest{
		WorkspacePath: workspacePath,
		Copilot: workflow.CopilotConfig{
			Command:        "copilot",
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
	if err := session.RunPrompt(context.Background(), "do work", 1); err != nil {
		t.Fatalf("RunPrompt() error = %v", err)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.executeCalls) != 1 {
		t.Fatalf("execute call count = %d, want 1", len(executor.executeCalls))
	}
	call := executor.executeCalls[0]
	if call.Dir != workspacePath {
		t.Fatalf("execute dir = %q, want %q", call.Dir, workspacePath)
	}
	if call.Command != "bash" || len(call.Args) != 2 || call.Args[0] != "-lc" {
		t.Fatalf("execute command = %q %#v, want bash -lc <command>", call.Command, call.Args)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) == 0 || events[len(events)-1].Event != "prompt_completed" {
		t.Fatalf("events = %#v, want prompt_completed", events)
	}
	if session.ID() != "stream-session" {
		t.Fatalf("session ID = %q, want stream-session", session.ID())
	}
	if session.Transport() != "acp_stdio" {
		t.Fatalf("session transport = %q, want acp_stdio", session.Transport())
	}
}

func itoa(value int) string {
	return fmt.Sprintf("%d", value)
}
