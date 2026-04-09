package workflow

import (
	"time"

	"github.com/osteele/liquid"

	"autopilot/internal/model"
)

type Template struct {
	template *liquid.Template
}

func ParseTemplate(source string) (*Template, error) {
	engine := liquid.NewEngine()
	engine.StrictVariables()
	template, err := engine.ParseString(source)
	if err != nil {
		return nil, wrap(ErrTemplateParse, err)
	}
	return &Template{template: template}, nil
}

func RenderPrompt(source string, issue model.Issue, attempt *int) (string, error) {
	template, err := ParseTemplate(source)
	if err != nil {
		return "", err
	}
	return template.Render(buildPromptData(issue, attempt))
}

func (template *Template) Render(values map[string]any) (string, error) {
	rendered, err := template.template.RenderString(values)
	if err != nil {
		return "", wrap(ErrTemplateRender, err)
	}
	return rendered, nil
}

func buildPromptData(issue model.Issue, attempt *int) map[string]any {
	var priority any
	if issue.Priority != nil {
		priority = *issue.Priority
	}
	var branchName any
	if issue.BranchName != nil {
		branchName = *issue.BranchName
	}
	var url any
	if issue.URL != nil {
		url = *issue.URL
	}
	var description any
	if issue.Description != nil {
		description = *issue.Description
	}
	blockedBy := make([]any, 0, len(issue.BlockedBy))
	for _, blocker := range issue.BlockedBy {
		entry := map[string]any{}
		if blocker.ID != nil {
			entry["id"] = *blocker.ID
		} else {
			entry["id"] = nil
		}
		if blocker.Identifier != nil {
			entry["identifier"] = *blocker.Identifier
		} else {
			entry["identifier"] = nil
		}
		if blocker.State != nil {
			entry["state"] = *blocker.State
		} else {
			entry["state"] = nil
		}
		blockedBy = append(blockedBy, entry)
	}
	labels := make([]any, 0, len(issue.Labels))
	for _, label := range issue.Labels {
		labels = append(labels, label)
	}
	issueMap := map[string]any{
		"id":          issue.ID,
		"identifier":  issue.Identifier,
		"title":       issue.Title,
		"description": description,
		"priority":    priority,
		"state":       issue.State,
		"branch_name": branchName,
		"url":         url,
		"labels":      labels,
		"blocked_by":  blockedBy,
		"created_at":  formatOptionalTime(issue.CreatedAt),
		"updated_at":  formatOptionalTime(issue.UpdatedAt),
	}
	data := map[string]any{"issue": issueMap}
	if attempt != nil {
		data["attempt"] = *attempt
	} else {
		data["attempt"] = nil
	}
	return data
}

func formatOptionalTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339)
}