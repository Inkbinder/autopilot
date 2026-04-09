package tracker

import "fmt"

const (
	ErrUnsupportedTrackerKind = "unsupported_tracker_kind"
	ErrMissingTrackerAPIKey   = "missing_tracker_api_key"
	ErrMissingRepository      = "missing_tracker_repository"
	ErrGitHubAPIRequest       = "github_api_request"
	ErrGitHubAPIStatus        = "github_api_status"
	ErrGitHubGraphQLErrors    = "github_graphql_errors"
	ErrGitHubUnknownPayload   = "github_unknown_payload"
	ErrGitHubMissingEndCursor = "github_missing_end_cursor"
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