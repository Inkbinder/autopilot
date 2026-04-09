package workflow

import "fmt"

const (
	ErrMissingWorkflowFile       = "missing_workflow_file"
	ErrWorkflowParse            = "workflow_parse_error"
	ErrFrontMatterNotMap        = "workflow_front_matter_not_a_map"
	ErrTemplateParse            = "template_parse_error"
	ErrTemplateRender           = "template_render_error"
	ErrUnsupportedTrackerKind   = "unsupported_tracker_kind"
	ErrMissingTrackerAPIKey     = "missing_tracker_api_key"
	ErrMissingTrackerRepository = "missing_tracker_repository"
	ErrMissingCopilotCommand    = "missing_copilot_command"
	ErrUnsupportedTransport     = "unsupported_copilot_transport"
)

type Error struct {
	Code string
	Err  error
}

func (err *Error) Error() string {
	if err == nil {
		return ""
	}
	if err.Err == nil {
		return err.Code
	}
	return fmt.Sprintf("%s: %v", err.Code, err.Err)
}

func (err *Error) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

func wrap(code string, err error) error {
	if err == nil {
		return &Error{Code: code}
	}
	return &Error{Code: code, Err: err}
}