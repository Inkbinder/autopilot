package copilot

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Inkbinder/autopilot/internal/runstate"
	"github.com/Inkbinder/autopilot/internal/workflow"
)

const maxACPLineBytes = 10 * 1024 * 1024

type ACPStdioClient struct {
	gracePeriod time.Duration
	auditor     runstate.Writer
	logger      *slog.Logger
}

func NewClient(config workflow.Config) (Client, error) {
	return NewClientWithOptions(config, ClientOptions{})
}

func NewClientWithOptions(config workflow.Config, options ClientOptions) (Client, error) {
	if config.Copilot.Transport != "acp_stdio" {
		return nil, wrap(ErrUnsupportedTransport, fmt.Errorf("transport %s is not implemented in this build", config.Copilot.Transport))
	}
	return &ACPStdioClient{gracePeriod: 2 * time.Second, auditor: options.AuditWriter, logger: options.Logger}, nil
}

func (client *ACPStdioClient) StartSession(ctx context.Context, request StartRequest) (Session, error) {
	stat, err := os.Stat(request.WorkspacePath)
	if err != nil {
		return nil, wrap(ErrInvalidWorkspaceCWD, err)
	}
	if !stat.IsDir() {
		return nil, wrap(ErrInvalidWorkspaceCWD, fmt.Errorf("workspace is not a directory: %s", request.WorkspacePath))
	}
	process, err := startACPProcess(ctx, request.WorkspacePath, request.Copilot, request.OnEvent, client.gracePeriod, client.auditor, client.logger)
	if err != nil {
		return nil, err
	}
	startupCtx, cancel := context.WithTimeout(ctx, request.Copilot.StartupTimeout)
	defer cancel()
	if err := process.initialize(startupCtx); err != nil {
		_ = process.close(context.Background())
		return nil, err
	}
	sessionID, err := process.newSession(startupCtx, request.WorkspacePath, request.Copilot.Model)
	if err != nil {
		_ = process.close(context.Background())
		return nil, err
	}
	process.sessionID = sessionID
	process.emit(Event{
		Event:         "session_started",
		Timestamp:     time.Now().UTC(),
		SessionID:     sessionID,
		CopilotCLIPID: process.pid,
	})
	return process, nil
}

type acpProcess struct {
	command     *exec.Cmd
	stdin       io.WriteCloser
	pid         *int
	onEvent     EventHandler
	gracePeriod time.Duration
	sessionID   string
	auditor     runstate.Writer
	logger      *slog.Logger
	auditMeta   runstate.Metadata

	mu        sync.Mutex
	nextID    int
	pending   map[int]chan acpEnvelope
	waitDone  chan struct{}
	waitErr   error
	interrupt chan error
	closed    bool
	closeOnce sync.Once
}

type acpEnvelope struct {
	ID     *int            `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *acpError       `json:"error,omitempty"`
}

type acpError struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type acpCallResult struct {
	Result     map[string]any
	RawRequest json.RawMessage
	RawResult  json.RawMessage
	RawError   json.RawMessage
}

func startACPProcess(ctx context.Context, workspacePath string, config workflow.CopilotConfig, onEvent EventHandler, gracePeriod time.Duration, auditor runstate.Writer, logger *slog.Logger) (*acpProcess, error) {
	commandString := buildACPCommand(config)
	command := exec.Command("bash", "-lc", commandString)
	command.Dir = workspacePath
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, wrap(ErrTransportError, err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, wrap(ErrTransportError, err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, wrap(ErrTransportError, err)
	}
	err = command.Start()
	if auditErr := runstate.RecordAuditEvent(ctx, auditor, "copilot_cli_start", map[string]any{
		"command":        commandString,
		"workspace_path": workspacePath,
		"success":        err == nil,
		"error":          errorString(err),
	}); auditErr != nil {
		logAuditFailure(logger, ctx, auditErr, "copilot audit write failed")
	}
	if err != nil {
		return nil, wrap(ErrCopilotCLINotFound, err)
	}
	auditMeta, _ := runstate.MetadataFromContext(ctx)
	process := &acpProcess{
		command:     command,
		stdin:       stdin,
		onEvent:     onEvent,
		gracePeriod: gracePeriod,
		auditor:     auditor,
		logger:      logger,
		auditMeta:   auditMeta,
		pending:     map[int]chan acpEnvelope{},
		waitDone:    make(chan struct{}),
		interrupt:   make(chan error, 4),
	}
	if command.Process != nil {
		pid := command.Process.Pid
		process.pid = &pid
	}
	go process.readStdout(stdout)
	go process.readStderr(stderr)
	go func() {
		err := command.Wait()
		process.mu.Lock()
		process.waitErr = err
		process.mu.Unlock()
		close(process.waitDone)
	}()
	return process, nil
}

func buildACPCommand(config workflow.CopilotConfig) string {
	parts := []string{strings.TrimSpace(config.Command), "--acp", "--stdio"}
	for _, arg := range config.CLIArgs {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func (process *acpProcess) ID() string {
	return process.sessionID
}

func (process *acpProcess) Transport() string {
	return "acp_stdio"
}

func (process *acpProcess) ProcessID() *int {
	return process.pid
}

func (process *acpProcess) RunPrompt(ctx context.Context, prompt string, turn int) error {
	promptCtx := ctx
	result, err := process.call(promptCtx, "session/prompt", map[string]any{
		"sessionId": process.sessionID,
		"prompt": []map[string]any{{
			"type": "text",
			"text": prompt,
		}},
	})
	process.recordAudit("llm_prompt", map[string]any{
		"turn":           turn,
		"prompt":         prompt,
		"request":        rawJSON(result.RawRequest),
		"response":       rawJSON(result.RawResult),
		"response_error": rawJSON(result.RawError),
		"error":          errorString(err),
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			process.emit(Event{Event: "prompt_timeout", Timestamp: time.Now().UTC(), SessionID: process.sessionID, CopilotCLIPID: process.pid, Turn: turn})
			return wrap(ErrPromptTimeout, err)
		}
		if errors.Is(err, context.Canceled) {
			process.emit(Event{Event: "prompt_cancelled", Timestamp: time.Now().UTC(), SessionID: process.sessionID, CopilotCLIPID: process.pid, Turn: turn})
			return wrap(ErrPromptCancelled, err)
		}
		var copilotErr *Error
		if errors.As(err, &copilotErr) {
			return err
		}
		process.emit(Event{Event: "prompt_failed", Timestamp: time.Now().UTC(), SessionID: process.sessionID, CopilotCLIPID: process.pid, Message: err.Error(), Turn: turn})
		return wrap(ErrPromptFailed, err)
	}
	message := summarizePayload(result.Result)
	usage := extractUsageFromNamedPayload("result", result.Result)
	rateLimits := extractRateLimits(result.Result)
	process.emit(Event{Event: "prompt_completed", Timestamp: time.Now().UTC(), SessionID: process.sessionID, CopilotCLIPID: process.pid, Message: message, Usage: usage, RateLimits: rateLimits, Turn: turn})
	return nil
}

func (process *acpProcess) Close(ctx context.Context) error {
	return process.close(ctx)
}

func (process *acpProcess) initialize(ctx context.Context) error {
	_, err := process.call(ctx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{},
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return wrap(ErrStartupTimeout, err)
		}
		return wrap(ErrACPHandshakeFailed, err)
	}
	return nil
}

func (process *acpProcess) newSession(ctx context.Context, workspacePath string, model string) (string, error) {
	params := map[string]any{
		"cwd":        workspacePath,
		"mcpServers": []map[string]any{},
	}
	if strings.TrimSpace(model) != "" {
		params["model"] = model
	}
	result, err := process.call(ctx, "session/new", params)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", wrap(ErrStartupTimeout, err)
		}
		return "", wrap(ErrACPHandshakeFailed, err)
	}
	sessionID := findString(result.Result, "sessionId", "session_id", "id")
	if sessionID == "" {
		if nested := nestedMap(result.Result, "session"); nested != nil {
			sessionID = findString(nested, "sessionId", "session_id", "id")
		}
	}
	if sessionID == "" {
		return "", wrap(ErrACPHandshakeFailed, fmt.Errorf("session id missing in newSession response"))
	}
	return sessionID, nil
}

func (process *acpProcess) call(ctx context.Context, method string, params map[string]any) (acpCallResult, error) {
	responseChannel := make(chan acpEnvelope, 1)
	requestID := process.reserveID(responseChannel)
	defer process.releaseID(requestID)

	result := acpCallResult{}
	payload := map[string]any{"id": requestID, "method": method, "params": params}
	line, err := json.Marshal(payload)
	if err != nil {
		return result, wrap(ErrTransportError, err)
	}
	result.RawRequest = append(result.RawRequest[:0], line...)
	if _, err := process.stdin.Write(append(line, '\n')); err != nil {
		return result, wrap(ErrTransportError, err)
	}

	select {
	case response := <-responseChannel:
		if response.Error != nil {
			rawError, marshalErr := json.Marshal(response.Error)
			if marshalErr == nil {
				result.RawError = rawError
			}
			return result, fmt.Errorf("acp error code=%d message=%s", response.Error.Code, response.Error.Message)
		}
		if len(response.Result) == 0 {
			result.Result = map[string]any{}
			return result, nil
		}
		result.RawResult = append(result.RawResult[:0], response.Result...)
		result.Result = map[string]any{}
		if err := json.Unmarshal(response.Result, &result.Result); err != nil {
			return result, wrap(ErrTransportError, err)
		}
		return result, nil
	case err := <-process.interrupt:
		return result, err
	case <-process.waitDone:
		err := process.exitErr()
		if err == nil {
			return result, wrap(ErrTransportExit, fmt.Errorf("copilot process exited"))
		}
		return result, wrap(ErrTransportExit, err)
	case <-ctx.Done():
		return result, ctx.Err()
	}
}

func (process *acpProcess) reserveID(responseChannel chan acpEnvelope) int {
	process.mu.Lock()
	defer process.mu.Unlock()
	process.nextID++
	requestID := process.nextID
	process.pending[requestID] = responseChannel
	return requestID
}

func (process *acpProcess) releaseID(requestID int) {
	process.mu.Lock()
	defer process.mu.Unlock()
	delete(process.pending, requestID)
}

func (process *acpProcess) readStdout(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), maxACPLineBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		envelope := acpEnvelope{}
		if err := json.Unmarshal(line, &envelope); err != nil {
			process.emit(Event{Event: "malformed", Timestamp: time.Now().UTC(), SessionID: process.sessionID, CopilotCLIPID: process.pid, Message: truncate(string(line), 512)})
			continue
		}
		if envelope.ID != nil && envelope.Method == "" {
			process.dispatchResponse(*envelope.ID, envelope)
			continue
		}
		process.handleInboundEnvelope(envelope, line)
	}
	if err := scanner.Err(); err != nil {
		select {
		case process.interrupt <- wrap(ErrTransportError, err):
		default:
		}
	}
}

func (process *acpProcess) readStderr(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 0, 64*1024), maxACPLineBytes)
	for scanner.Scan() {
		message := scanner.Text()
		if strings.TrimSpace(message) == "" {
			continue
		}
		process.recordAudit("copilot_stderr", map[string]any{"message": message})
		process.emit(Event{Event: "other_message", Timestamp: time.Now().UTC(), SessionID: process.sessionID, CopilotCLIPID: process.pid, Message: truncate(message, 512)})
	}
}

func (process *acpProcess) handleInboundEnvelope(envelope acpEnvelope, rawLine []byte) {
	raw := map[string]any{}
	_ = json.Unmarshal(rawLine, &raw)
	method := strings.ToLower(strings.TrimSpace(envelope.Method))
	params := rawMap(envelope.Params)
	message := summarizePayload(params)
	usage := extractUsage(method, params)
	rateLimits := extractRateLimits(params)
	actionType := "copilot_event"
	switch {
	case isInputRequired(method, params):
		actionType = "copilot_input_required"
	case isPermissionRequest(method, params):
		actionType = "copilot_permission_request"
	case strings.Contains(method, "update"):
		actionType = "llm_response"
	case strings.Contains(method, "tool"):
		actionType = "mcp_tool_called"
	}
	process.recordAudit(actionType, map[string]any{"method": envelope.Method, "payload": rawJSON(rawLine)})

	switch {
	case isInputRequired(method, params):
		process.emit(Event{Event: "prompt_input_required", Timestamp: time.Now().UTC(), SessionID: process.sessionID, CopilotCLIPID: process.pid, Message: message, Usage: usage, RateLimits: rateLimits, Raw: raw})
		select {
		case process.interrupt <- wrap(ErrPromptInputRequired, fmt.Errorf("user input required")):
		default:
		}
	case isPermissionRequest(method, params):
		if envelope.ID == nil {
			process.emit(Event{Event: "prompt_failed", Timestamp: time.Now().UTC(), SessionID: process.sessionID, CopilotCLIPID: process.pid, Message: "permission request could not be auto-approved", Raw: raw})
			select {
			case process.interrupt <- wrap(ErrPromptFailed, fmt.Errorf("permission request could not be auto-approved")):
			default:
			}
			return
		}
		if err := process.respondApproval(*envelope.ID, params); err != nil {
			select {
			case process.interrupt <- wrap(ErrPromptFailed, err):
			default:
			}
			return
		}
		process.emit(Event{Event: "permission_auto_approved", Timestamp: time.Now().UTC(), SessionID: process.sessionID, CopilotCLIPID: process.pid, Message: message, Usage: usage, RateLimits: rateLimits, Raw: raw})
	case strings.Contains(method, "tool"):
		process.emit(Event{Event: "mcp_tool_called", Timestamp: time.Now().UTC(), SessionID: process.sessionID, CopilotCLIPID: process.pid, Message: message, Usage: usage, RateLimits: rateLimits, Raw: raw})
	default:
		process.emit(Event{Event: "notification", Timestamp: time.Now().UTC(), SessionID: process.sessionID, CopilotCLIPID: process.pid, Message: message, Usage: usage, RateLimits: rateLimits, Raw: raw})
	}
}

func (process *acpProcess) respondApproval(requestID int, params map[string]any) error {
	outcome, err := approvalOutcome(params)
	if err != nil {
		return err
	}
	response := map[string]any{
		"id": requestID,
		"result": map[string]any{
			"outcome": outcome,
		},
	}
	line, err := json.Marshal(response)
	if err != nil {
		return err
	}
	_, err = process.stdin.Write(append(line, '\n'))
	return err
}

func (process *acpProcess) dispatchResponse(requestID int, envelope acpEnvelope) {
	process.mu.Lock()
	responseChannel, ok := process.pending[requestID]
	process.mu.Unlock()
	if !ok {
		return
	}
	responseChannel <- envelope
}

func (process *acpProcess) close(_ context.Context) error {
	var closeErr error
	process.closeOnce.Do(func() {
		process.mu.Lock()
		process.closed = true
		process.mu.Unlock()
		if process.command == nil || process.command.Process == nil {
			return
		}
		_ = process.command.Process.Signal(syscall.SIGTERM)
		select {
		case <-process.waitDone:
			if err := process.exitErr(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				closeErr = err
			}
		case <-time.After(process.gracePeriod):
			_ = process.command.Process.Kill()
			<-process.waitDone
			if err := process.exitErr(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				closeErr = err
			}
		}
	})
	return closeErr
}

func (process *acpProcess) emit(event Event) {
	if process.onEvent == nil {
		return
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if event.SessionID == "" {
		event.SessionID = process.sessionID
	}
	if event.CopilotCLIPID == nil {
		event.CopilotCLIPID = process.pid
	}
	process.onEvent(event)
}

func (process *acpProcess) exitErr() error {
	process.mu.Lock()
	defer process.mu.Unlock()
	return process.waitErr
}

func (process *acpProcess) recordAudit(actionType string, payload any) {
	if process.auditor == nil {
		return
	}
	if err := runstate.RecordAuditEvent(process.auditContext(), process.auditor, actionType, payload); err != nil {
		logAuditFailure(process.logger, process.auditContext(), err, "copilot audit write failed")
	}
}

func (process *acpProcess) auditContext() context.Context {
	ctx := context.Background()
	if process.auditMeta.RunID <= 0 {
		return ctx
	}
	return runstate.WithMetadata(ctx, process.auditMeta)
}

func logAuditFailure(logger *slog.Logger, ctx context.Context, err error, message string) {
	if logger == nil {
		return
	}
	metadata, _ := runstate.MetadataFromContext(ctx)
	logger.With(slog.String("repo", strings.TrimSpace(metadata.Repo)), slog.String("issue_id", strings.TrimSpace(metadata.IssueID))).Warn(message, slog.Any("error", err))
}

func rawJSON(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return json.RawMessage(append([]byte(nil), value...))
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func rawMap(payload json.RawMessage) map[string]any {
	if len(payload) == 0 {
		return nil
	}
	decoded := map[string]any{}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil
	}
	return decoded
}

func nestedMap(values map[string]any, key string) map[string]any {
	if values == nil {
		return nil
	}
	raw, ok := values[key]
	if !ok || raw == nil {
		return nil
	}
	typed, ok := raw.(map[string]any)
	if ok {
		return typed
	}
	return nil
}

func findString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if values == nil {
			return ""
		}
		raw, ok := values[key]
		if !ok || raw == nil {
			continue
		}
		switch typed := raw.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				return strings.TrimSpace(typed)
			}
		case fmt.Stringer:
			value := strings.TrimSpace(typed.String())
			if value != "" {
				return value
			}
		default:
			value := strings.TrimSpace(fmt.Sprint(raw))
			if value != "" && value != "<nil>" {
				return value
			}
		}
	}
	return ""
}

func approvalOutcome(params map[string]any) (map[string]any, error) {
	optionID := selectPermissionOption(params)
	if optionID == "" {
		return nil, fmt.Errorf("permission request missing selectable option")
	}
	return map[string]any{
		"outcome":  "selected",
		"optionId": optionID,
	}, nil
}

func selectPermissionOption(params map[string]any) string {
	preferred := map[string]int{
		"allow_once":   0,
		"allow_always": 1,
	}
	bestRank := len(preferred) + 1
	bestOptionID := ""
	fallbackOptionID := ""
	rawOptions, ok := params["options"]
	if !ok || rawOptions == nil {
		return ""
	}
	options, ok := rawOptions.([]any)
	if !ok {
		return ""
	}
	for _, rawOption := range options {
		option, ok := rawOption.(map[string]any)
		if !ok {
			continue
		}
		optionID := findString(option, "optionId", "option_id", "id")
		if optionID == "" {
			continue
		}
		if fallbackOptionID == "" {
			fallbackOptionID = optionID
		}
		rank, ok := preferred[strings.ToLower(findString(option, "kind", "type"))]
		if ok && rank < bestRank {
			bestRank = rank
			bestOptionID = optionID
		}
	}
	if bestOptionID != "" {
		return bestOptionID
	}
	return fallbackOptionID
}

func summarizePayload(values map[string]any) string {
	if values == nil {
		return ""
	}
	for _, key := range []string{"message", "text", "content", "status", "summary"} {
		if value := findString(values, key); value != "" {
			return truncate(value, 512)
		}
	}
	if nested := nestedMap(values, "payload"); nested != nil {
		if value := summarizePayload(nested); value != "" {
			return value
		}
	}
	if nested := nestedMap(values, "data"); nested != nil {
		if value := summarizePayload(nested); value != "" {
			return value
		}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return ""
	}
	return truncate(string(encoded), 512)
}

func isInputRequired(method string, params map[string]any) bool {
	if strings.Contains(method, "input_required") || strings.Contains(method, "inputrequired") || strings.Contains(method, "userinput") {
		return true
	}
	return findString(params, "status", "type") == "input_required"
}

func isPermissionRequest(method string, params map[string]any) bool {
	if strings.Contains(method, "permission") {
		return true
	}
	return strings.Contains(strings.ToLower(findString(params, "type", "event")), "permission")
}

func extractUsage(method string, params map[string]any) *UsageTotals {
	if params == nil {
		return nil
	}
	if usage := extractUsageFromNamedPayload("total_token_usage", params); usage != nil {
		return usage
	}
	if strings.Contains(method, "tokenusage") {
		if usage := extractUsageFromNamedPayload("payload", params); usage != nil {
			return usage
		}
		if usage := extractUsageFromNamedPayload("data", params); usage != nil {
			return usage
		}
		if usage := directUsage(params); usage != nil {
			return usage
		}
	}
	return nil
}

func extractUsageFromNamedPayload(key string, params map[string]any) *UsageTotals {
	if key == "result" {
		return directUsage(params)
	}
	nested := nestedMap(params, key)
	if nested == nil {
		return nil
	}
	return directUsage(nested)
}

func directUsage(params map[string]any) *UsageTotals {
	if params == nil {
		return nil
	}
	input, inputOK := intField(params, "input_tokens", "inputTokens", "prompt_tokens", "promptTokens")
	output, outputOK := intField(params, "output_tokens", "outputTokens", "completion_tokens", "completionTokens")
	total, totalOK := intField(params, "total_tokens", "totalTokens")
	if !inputOK && !outputOK && !totalOK {
		return nil
	}
	if !totalOK {
		total = input + output
	}
	return &UsageTotals{InputTokens: input, OutputTokens: output, TotalTokens: total}
}

func intField(values map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		raw, ok := values[key]
		if !ok || raw == nil {
			continue
		}
		switch typed := raw.(type) {
		case float64:
			return int(typed), true
		case float32:
			return int(typed), true
		case int:
			return typed, true
		case int64:
			return int(typed), true
		case string:
			var parsed int
			_, err := fmt.Sscanf(strings.TrimSpace(typed), "%d", &parsed)
			if err == nil {
				return parsed, true
			}
		}
	}
	return 0, false
}

func extractRateLimits(values map[string]any) any {
	if values == nil {
		return nil
	}
	for _, key := range []string{"rate_limits", "rateLimits"} {
		if raw, ok := values[key]; ok {
			return raw
		}
	}
	for _, nestedKey := range []string{"payload", "data"} {
		if nested := nestedMap(values, nestedKey); nested != nil {
			if rateLimits := extractRateLimits(nested); rateLimits != nil {
				return rateLimits
			}
		}
	}
	return nil
}

func truncate(value string, maxLength int) string {
	if len(value) <= maxLength {
		return value
	}
	if maxLength <= 3 {
		return value[:maxLength]
	}
	return value[:maxLength-3] + "..."
}
