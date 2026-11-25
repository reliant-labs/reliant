package wfyaml

import (
	"errors"
	"fmt"

	"github.com/reliant-labs/reliant/internal/version"
)

// UnknownFieldError is a key the workflow format does not define in this
// build. Every strict key check in the parser returns one, so a caller can
// tell "this file uses a field I do not know" from any other parse failure.
//
// The parser cannot say which release added a field it has never heard of:
// the binary that rejects one is by construction older than whatever defines
// it. What it can say is which build it is, so ParseWorkflow names its own
// version next to the field. A file written against a newer CLI and synced to
// an older server then reads "reliant v1.8.6 does not define \"concurrency\""
// instead of a bare parse failure.
type UnknownFieldError struct {
	// Field is the unknown key as written.
	Field string
	msg   string
}

func (e *UnknownFieldError) Error() string { return e.msg }

func unknownField(field, format string, args ...any) error {
	return &UnknownFieldError{Field: field, msg: fmt.Sprintf(format, args...)}
}

// removedFields are keys a release removed, by the name a file writes them
// with. A file that still has one is told so, and what to do, rather than
// asked whether it is a typo.
var removedFields = map[string]string{
	"cli_login": "requires.cli_login (vendor agent CLI sign-ins, shipped in v1.29.0) was removed: " +
		"delete it from requires",
	"llm_gateway": "a run node's llm_gateway (an LLM gateway step key for an agent CLI, shipped in v1.29.0) " +
		"was removed: delete it from the node's args",
}

// withVersionSkewHint names this build's version on an unknown-field error,
// or says which removed field it is, and returns any other error unchanged.
func withVersionSkewHint(err error) error {
	var unknown *UnknownFieldError
	if !errors.As(err, &unknown) {
		return err
	}
	if removed, ok := removedFields[unknown.Field]; ok {
		return fmt.Errorf("%w — %s", err, removed)
	}
	return fmt.Errorf("%w — reliant %s does not define %q: fix it if it is a typo or a removed field; "+
		"if the file was written for a newer Reliant, upgrade the server that runs it", err, version.Version, unknown.Field)
}
