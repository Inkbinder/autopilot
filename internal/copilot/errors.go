package copilot

import "fmt"

const (
	ErrCopilotCLINotFound    = "copilot_cli_not_found"
	ErrInvalidWorkspaceCWD   = "invalid_workspace_cwd"
	ErrStartupTimeout        = "startup_timeout"
	ErrPromptTimeout         = "prompt_timeout"
	ErrTransportExit         = "transport_exit"
	ErrTransportError        = "transport_error"
	ErrACPHandshakeFailed    = "acp_handshake_failed"
	ErrPromptFailed          = "prompt_failed"
	ErrPromptCancelled       = "prompt_cancelled"
	ErrPromptInputRequired   = "prompt_input_required"
	ErrUnsupportedTransport  = "unsupported_copilot_transport"
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