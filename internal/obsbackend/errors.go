package obsbackend

import (
	"errors"
	"fmt"
)

// The exit statuses forge's agent verbs share (internal/cli/exitcodes.go): 1
// means "we looked, and it is wrong"; 2 means "we could not look". A backend
// error carries one so the CLI reports the same code the document stamps.
const (
	ExitWrong        = 1
	ExitUndetermined = 2
)

// Error is a backend failure with the process status it produces and the
// commands that remedy it. It satisfies the ExitCode() int interface forge's
// error-to-status resolution reads, so a backend never imports the CLI.
type Error struct {
	Code int
	Msg  string
	// Suggested are complete, runnable commands that remedy the failure.
	Suggested []string
	Err       error
}

func (e *Error) Error() string {
	if e.Err != nil && e.Msg != "" {
		return e.Msg + ": " + e.Err.Error()
	}
	if e.Msg == "" && e.Err != nil {
		return e.Err.Error()
	}
	return e.Msg
}

func (e *Error) Unwrap() error { return e.Err }

// ExitCode is the process status this error produces.
func (e *Error) ExitCode() int { return e.Code }

// Wrong is a failure of the request: the statement was refused, the thing
// asked for does not exist.
func Wrong(format string, a ...any) *Error {
	return &Error{Code: ExitWrong, Msg: fmt.Sprintf(format, a...)}
}

// Undetermined is a failure to look: the stack is down, the backend is
// unreachable.
func Undetermined(format string, a ...any) *Error {
	return &Error{Code: ExitUndetermined, Msg: fmt.Sprintf(format, a...)}
}

// With attaches the commands that remedy the error.
func (e *Error) With(cmds ...string) *Error {
	e.Suggested = append(e.Suggested, cmds...)
	return e
}

// Cause records the underlying error.
func (e *Error) Cause(err error) *Error {
	e.Err = err
	return e
}

// SuggestedOf returns the remedy commands of the first *Error in err's chain.
func SuggestedOf(err error) []string {
	var be *Error
	if errors.As(err, &be) {
		return be.Suggested
	}
	return nil
}
