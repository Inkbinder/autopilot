package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var supportedTransports = map[string]struct{}{
	"acp_stdio":     {},
	"acp_tcp":       {},
	"headless_http": {},
}

var (
	defaultDispatchLabels = []string{"autopilot:ready", "autopilot:merging", "autopilot:in-progress", "autopilot:rework"}
	defaultExcludedLabels = []string{"autopilot:human-review", "autopilot:blocked", "autopilot:question"}
)

type Config struct {
	WorkflowPath string
	Tracker      TrackerConfig
	Polling      PollingConfig
	Workspace    WorkspaceConfig
	Hooks        HooksConfig
	Agent        AgentConfig
	Copilot      CopilotConfig
	Telemetry    TelemetryConfig
	Server       ServerConfig
}

type TrackerConfig struct {
	Kind           string
	Endpoint       string
	APIKey         string
	Repository     string
	ActiveStates   []string
	TerminalStates []string
	DispatchLabels []string
	ExcludedLabels []string
}

type PollingConfig struct {
	Interval time.Duration
}

type WorkspaceConfig struct {
	Provider string
	Root     string
	Image    string
}

type HooksConfig struct {
	AfterCreate  string
	BeforeRun    string
	AfterRun     string
	BeforeRemove string
	Timeout      time.Duration
}

type AgentConfig struct {
	MaxConcurrentAgents        int
	MaxTurns                   int
	MaxRetryBackoff            time.Duration
	MaxConcurrentAgentsByState map[string]int
}

type CopilotConfig struct {
	Command        string
	Transport      string
	CLIArgs        []string
	Model          string
	Port           int
	GitHubMCPTools []string
	PromptTimeout  time.Duration
	StartupTimeout time.Duration
	StallTimeout   time.Duration
}

type TelemetryConfig struct {
	OTLPEndpoint string
}

type ServerConfig struct {
	Port *int
}

func LoadAndResolve(path string, lookupEnv func(string) string) (Definition, Config, error) {
	definition, err := LoadDefinition(path)
	if err != nil {
		return Definition{}, Config{}, err
	}
	config, err := ResolveConfig(path, definition, lookupEnv)
	if err != nil {
		return Definition{}, Config{}, err
	}
	return definition, config, nil
}

func ResolveConfig(path string, definition Definition, lookupEnv func(string) string) (Config, error) {
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}

	trackerMap := nestedMap(definition.Config, "tracker")
	pollingMap := nestedMap(definition.Config, "polling")
	workspaceMap := nestedMap(definition.Config, "workspace")
	hooksMap := nestedMap(definition.Config, "hooks")
	agentMap := nestedMap(definition.Config, "agent")
	copilotMap := nestedMap(definition.Config, "copilot")
	telemetryMap := nestedMap(definition.Config, "telemetry")
	serverMap := nestedMap(definition.Config, "server")

	config := Config{
		WorkflowPath: path,
		Tracker: TrackerConfig{
			Kind:           strings.TrimSpace(stringValue(trackerMap, "kind")),
			Endpoint:       defaultString(stringValue(trackerMap, "endpoint"), "https://api.github.com/graphql"),
			APIKey:         resolveSecret(stringValue(trackerMap, "api_key"), lookupEnv),
			Repository:     strings.TrimSpace(stringValue(trackerMap, "repository")),
			ActiveStates:   normalizeStringSlice(stringSliceValue(trackerMap, "active_states", []string{"Open"})),
			TerminalStates: normalizeStringSlice(stringSliceValue(trackerMap, "terminal_states", []string{"Closed"})),
			DispatchLabels: normalizeLower(stringSliceValue(trackerMap, "dispatch_labels", defaultDispatchLabels)),
			ExcludedLabels: normalizeLower(stringSliceValue(trackerMap, "excluded_labels", defaultExcludedLabels)),
		},
		Polling: PollingConfig{Interval: durationFromMillis(pollingMap["interval_ms"], 30000*time.Millisecond)},
		Workspace: WorkspaceConfig{
			Provider: normalizeWorkspaceProvider(stringValue(workspaceMap, "provider")),
			Root:     resolvePathValue(stringValue(workspaceMap, "root"), lookupEnv),
			Image:    trimOptionalString(stringValue(workspaceMap, "image")),
		},
		Hooks: HooksConfig{
			AfterCreate:  trimOptionalString(stringValue(hooksMap, "after_create")),
			BeforeRun:    trimOptionalString(stringValue(hooksMap, "before_run")),
			AfterRun:     trimOptionalString(stringValue(hooksMap, "after_run")),
			BeforeRemove: trimOptionalString(stringValue(hooksMap, "before_remove")),
			Timeout:      positiveDurationFromMillis(hooksMap["timeout_ms"], 60000*time.Millisecond),
		},
		Agent: AgentConfig{
			MaxConcurrentAgents:        positiveInt(agentMap["max_concurrent_agents"], 10),
			MaxTurns:                   positiveInt(agentMap["max_turns"], 20),
			MaxRetryBackoff:            durationFromMillis(agentMap["max_retry_backoff_ms"], 300000*time.Millisecond),
			MaxConcurrentAgentsByState: normalizedPositiveIntMap(agentMap["max_concurrent_agents_by_state"]),
		},
		Copilot: CopilotConfig{
			Command:        defaultString(trimOptionalString(stringValue(copilotMap, "command")), "copilot"),
			Transport:      defaultString(trimOptionalString(stringValue(copilotMap, "transport")), "acp_stdio"),
			CLIArgs:        stringSliceValue(copilotMap, "cli_args", nil),
			Model:          trimOptionalString(stringValue(copilotMap, "model")),
			Port:           intValue(copilotMap["port"], 0),
			GitHubMCPTools: stringSliceValue(copilotMap, "github_mcp_tools", nil),
			PromptTimeout:  durationFromMillis(copilotMap["prompt_timeout_ms"], time.Hour),
			StartupTimeout: durationFromMillis(copilotMap["startup_timeout_ms"], 5*time.Second),
			StallTimeout:   durationFromMillis(copilotMap["stall_timeout_ms"], 5*time.Minute),
		},
		Telemetry: TelemetryConfig{OTLPEndpoint: trimOptionalString(stringValue(telemetryMap, "otel_endpoint"))},
		Server:    ServerConfig{Port: optionalInt(serverMap["port"])},
	}

	if config.Tracker.Kind == "github" && config.Tracker.APIKey == "" {
		config.Tracker.APIKey = strings.TrimSpace(lookupEnv("GITHUB_TOKEN"))
	}
	if config.Workspace.Root == "" {
		config.Workspace.Root = filepath.Join(os.TempDir(), "autopilot_workspaces")
	}
	if config.Workspace.Provider == "" {
		config.Workspace.Provider = "local"
	}
	if config.Polling.Interval <= 0 {
		config.Polling.Interval = 30000 * time.Millisecond
	}
	if config.Copilot.StallTimeout < 0 {
		config.Copilot.StallTimeout = 0
	}
	if len(config.Tracker.ActiveStates) == 0 {
		config.Tracker.ActiveStates = []string{"Open"}
	}
	if len(config.Tracker.TerminalStates) == 0 {
		config.Tracker.TerminalStates = []string{"Closed"}
	}

	if err := config.ValidateDispatch(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (config Config) ValidateDispatch() error {
	if config.Tracker.Kind == "" {
		return wrap(ErrUnsupportedTrackerKind, fmt.Errorf("tracker.kind is required"))
	}
	if config.Tracker.Kind != "github" {
		return wrap(ErrUnsupportedTrackerKind, fmt.Errorf("tracker.kind=%s", config.Tracker.Kind))
	}
	if strings.TrimSpace(config.Tracker.APIKey) == "" {
		return wrap(ErrMissingTrackerAPIKey, fmt.Errorf("tracker.api_key is required"))
	}
	if strings.TrimSpace(config.Tracker.Repository) == "" {
		return wrap(ErrMissingTrackerRepository, fmt.Errorf("tracker.repository is required"))
	}
	if strings.TrimSpace(config.Copilot.Command) == "" {
		return wrap(ErrMissingCopilotCommand, fmt.Errorf("copilot.command is required"))
	}
	if _, ok := supportedTransports[config.Copilot.Transport]; !ok {
		return wrap(ErrUnsupportedTransport, fmt.Errorf("copilot.transport=%s", config.Copilot.Transport))
	}
	return nil
}

func nestedMap(values map[string]any, key string) map[string]any {
	raw, ok := values[key]
	if !ok || raw == nil {
		return map[string]any{}
	}
	typed, ok := raw.(map[string]any)
	if ok {
		return typed
	}
	if generic, ok := raw.(map[any]any); ok {
		converted := map[string]any{}
		for key, value := range generic {
			converted[fmt.Sprint(key)] = value
		}
		return converted
	}
	return map[string]any{}
}

func stringValue(values map[string]any, key string) string {
	raw, ok := values[key]
	if !ok || raw == nil {
		return ""
	}
	if typed, ok := raw.(string); ok {
		return trimOptionalString(typed)
	}
	return trimOptionalString(fmt.Sprint(raw))
}

func stringSliceValue(values map[string]any, key string, fallback []string) []string {
	raw, ok := values[key]
	if !ok || raw == nil {
		return cloneStrings(fallback)
	}
	switch typed := raw.(type) {
	case []string:
		return normalizeStringSlice(typed)
	case []any:
		result := make([]string, 0, len(typed))
		for _, value := range typed {
			result = append(result, strings.TrimSpace(fmt.Sprint(value)))
		}
		return normalizeStringSlice(result)
	default:
		value := strings.TrimSpace(fmt.Sprint(raw))
		if value == "" {
			return cloneStrings(fallback)
		}
		return []string{value}
	}
}

func normalizeWorkspaceProvider(value string) string {
	provider := strings.ToLower(strings.TrimSpace(value))
	if provider == "" {
		return "local"
	}
	return provider
}

func normalizeStringSlice(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func normalizeLower(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(strings.ToLower(value))
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func resolveSecret(value string, lookupEnv func(string) string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "$") {
		resolved := strings.TrimSpace(lookupEnv(strings.TrimPrefix(value, "$")))
		return resolved
	}
	return value
}

func resolvePathValue(value string, lookupEnv func(string) string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = os.Expand(value, lookupEnv)
	if strings.HasPrefix(value, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			suffix := strings.TrimPrefix(strings.TrimPrefix(value, "~"), string(os.PathSeparator))
			value = filepath.Join(home, suffix)
		}
	}
	if strings.ContainsRune(value, os.PathSeparator) {
		return filepath.Clean(os.ExpandEnv(value))
	}
	return value
}

func durationFromMillis(raw any, fallback time.Duration) time.Duration {
	value := intValue(raw, int(fallback/time.Millisecond))
	return time.Duration(value) * time.Millisecond
}

func positiveDurationFromMillis(raw any, fallback time.Duration) time.Duration {
	value := durationFromMillis(raw, fallback)
	if value <= 0 {
		return fallback
	}
	return value
}

func positiveInt(raw any, fallback int) int {
	value := intValue(raw, fallback)
	if value <= 0 {
		return fallback
	}
	return value
}

func intValue(raw any, fallback int) int {
	if raw == nil {
		return fallback
	}
	switch typed := raw.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float32:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return fallback
		}
		return parsed
	default:
		parsed, err := strconv.Atoi(strings.TrimSpace(fmt.Sprint(raw)))
		if err != nil {
			return fallback
		}
		return parsed
	}
}

func optionalInt(raw any) *int {
	if raw == nil {
		return nil
	}
	value := intValue(raw, 0)
	return &value
}

func normalizedPositiveIntMap(raw any) map[string]int {
	typed, ok := raw.(map[string]any)
	if !ok {
		if generic, ok := raw.(map[any]any); ok {
			typed = map[string]any{}
			for key, value := range generic {
				typed[fmt.Sprint(key)] = value
			}
		} else {
			return map[string]int{}
		}
	}
	result := map[string]int{}
	for key, value := range typed {
		parsed := intValue(value, 0)
		if parsed > 0 {
			result[strings.ToLower(strings.TrimSpace(key))] = parsed
		}
	}
	return result
}

func trimOptionalString(value string) string {
	return strings.TrimSpace(value)
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	clone := make([]string, len(values))
	copy(clone, values)
	return clone
}
