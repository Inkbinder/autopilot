package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Inkbinder/autopilot/internal/model"
)

func TestLoadDefinitionParsesFrontMatterAndPrompt(t *testing.T) {
	t.Parallel()
	workflowPath := writeTempFile(t, `---
tracker:
  kind: github
  repository: octo/widgets
  api_key: token
---
Hello {{ issue.identifier }}`)

	definition, err := LoadDefinition(workflowPath)
	if err != nil {
		t.Fatalf("LoadDefinition() error = %v", err)
	}
	if got := nestedMap(definition.Config, "tracker")["kind"]; got != "github" {
		t.Fatalf("tracker.kind = %v, want github", got)
	}
	if definition.PromptTemplate != "Hello {{ issue.identifier }}" {
		t.Fatalf("prompt template = %q", definition.PromptTemplate)
	}
}

func TestLoadDefinitionRejectsNonMapFrontMatter(t *testing.T) {
	t.Parallel()
	workflowPath := writeTempFile(t, `---
- wrong
---
body`)

	_, err := LoadDefinition(workflowPath)
	if err == nil {
		t.Fatal("LoadDefinition() error = nil, want error")
	}
	var typedErr *Error
	if !errors.As(err, &typedErr) {
		t.Fatalf("expected typed workflow error, got %T", err)
	}
	if typedErr.Code != ErrFrontMatterNotMap {
		t.Fatalf("error code = %s, want %s", typedErr.Code, ErrFrontMatterNotMap)
	}
}

func TestResolveConfigAppliesDefaultsAndEnv(t *testing.T) {
	t.Parallel()
	definition := Definition{Config: map[string]any{
		"tracker": map[string]any{
			"kind":       "github",
			"repository": "octo/widgets",
			"api_key":    "$TOKEN",
		},
		"workspace": map[string]any{
			"root": "$WORKROOT/subdir",
		},
	}}
	lookupEnv := func(key string) string {
		switch key {
		case "TOKEN":
			return "secret-token"
		case "WORKROOT":
			return "/tmp/autopilot"
		default:
			return ""
		}
	}
	config, err := ResolveConfig("/tmp/WORKFLOW.md", definition, lookupEnv)
	if err != nil {
		t.Fatalf("ResolveConfig() error = %v", err)
	}
	if config.Tracker.APIKey != "secret-token" {
		t.Fatalf("tracker api key = %q, want secret-token", config.Tracker.APIKey)
	}
	if config.Tracker.Endpoint != "https://api.github.com/graphql" {
		t.Fatalf("endpoint = %q", config.Tracker.Endpoint)
	}
	if config.Workspace.Root != filepath.Clean("/tmp/autopilot/subdir") {
		t.Fatalf("workspace root = %q", config.Workspace.Root)
	}
	if config.Agent.MaxConcurrentAgents != 10 {
		t.Fatalf("max concurrent agents = %d", config.Agent.MaxConcurrentAgents)
	}
	if len(config.Tracker.DispatchLabels) == 0 || len(config.Tracker.ExcludedLabels) == 0 {
		t.Fatal("expected default label sets to be populated")
	}
	if config.Polling.Interval != 30*time.Second {
		t.Fatalf("polling interval = %s", config.Polling.Interval)
	}
}

func TestRenderPromptUsesLiquidAndFailsOnUnknownVariables(t *testing.T) {
	t.Parallel()
	attempt := 2
	prompt, err := RenderPrompt(`Issue {{ issue.identifier }}{% if attempt %} retry {{ attempt }}{% endif %}`,
		model.Issue{ID: "1", Identifier: "octo/widgets#1", Title: "Bug", State: "Open"}, &attempt)
	if err != nil {
		t.Fatalf("RenderPrompt() error = %v", err)
	}
	if prompt != "Issue octo/widgets#1 retry 2" {
		t.Fatalf("prompt = %q", prompt)
	}

	_, err = RenderPrompt(`{{ issue.missing }}`, model.Issue{ID: "1", Identifier: "octo/widgets#1", Title: "Bug", State: "Open"}, nil)
	if err == nil {
		t.Fatal("RenderPrompt() error = nil, want error")
	}
	var typedErr *Error
	if !errors.As(err, &typedErr) {
		t.Fatalf("expected typed workflow error, got %T", err)
	}
	if typedErr.Code != ErrTemplateRender && typedErr.Code != ErrTemplateParse {
		t.Fatalf("unexpected error code %s", typedErr.Code)
	}
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}
