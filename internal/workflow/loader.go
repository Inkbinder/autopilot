package workflow

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Definition struct {
	Config         map[string]any
	PromptTemplate string
}

func LoadDefinition(path string) (Definition, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Definition{}, wrap(ErrMissingWorkflowFile, err)
		}
		return Definition{}, wrap(ErrMissingWorkflowFile, err)
	}

	normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
	definition, err := parseDefinition(normalized)
	if err != nil {
		return Definition{}, err
	}
	return definition, nil
}

func parseDefinition(content string) (Definition, error) {
	if !strings.HasPrefix(content, "---\n") && strings.TrimSpace(content) != "---" {
		return Definition{Config: map[string]any{}, PromptTemplate: strings.TrimSpace(content)}, nil
	}

	lines := strings.Split(content, "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return Definition{Config: map[string]any{}, PromptTemplate: strings.TrimSpace(content)}, nil
	}

	closing := -1
	for index := 1; index < len(lines); index++ {
		if lines[index] == "---" {
			closing = index
			break
		}
	}
	if closing == -1 {
		return Definition{}, wrap(ErrWorkflowParse, fmt.Errorf("missing closing front matter delimiter"))
	}

	rawConfig := strings.Join(lines[1:closing], "\n")
	body := strings.Join(lines[closing+1:], "\n")

	if strings.TrimSpace(rawConfig) == "" {
		return Definition{Config: map[string]any{}, PromptTemplate: strings.TrimSpace(body)}, nil
	}

	var parsedAny any
	if err := yaml.Unmarshal([]byte(rawConfig), &parsedAny); err != nil {
		return Definition{}, wrap(ErrWorkflowParse, err)
	}
	if parsedAny == nil {
		return Definition{Config: map[string]any{}, PromptTemplate: strings.TrimSpace(body)}, nil
	}
	parsed, ok := parsedAny.(map[string]any)
	if !ok {
		return Definition{}, wrap(ErrFrontMatterNotMap, fmt.Errorf("front matter root must be an object"))
	}
	return Definition{Config: parsed, PromptTemplate: strings.TrimSpace(body)}, nil
}