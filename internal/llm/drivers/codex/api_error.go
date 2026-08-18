// Copyright (c) 2025 Reliant Labs
//
// forge:exclude-contract
//
// This package implements the llm.Driver interface declared in
// internal/llm/types.go. Its behavioral contract already exists upstream, and
// the exported methods here are that interface's implementation plus
// provider-specific wire handling. A local contract.go would restate an
// interface this package does not own.
package codex

import (
	"errors"
	"fmt"
	"strings"
)

// ErrMissingResponsesWriteScope means the access token is valid but lacks
// api.responses.write (common after OpenAI scope changes); user must re-run OAuth.
var ErrMissingResponsesWriteScope = errors.New("codex token missing api.responses.write; reconnect with Login with Codex in Settings")

// AugmentAPIError wraps Codex HTTP/API errors with a clearer message when the
// backend reports missing_scope for the responses API.
func AugmentAPIError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "api.responses.write") &&
		(strings.Contains(msg, "missing_scope") || strings.Contains(msg, "insufficient permissions")) {
		return fmt.Errorf("%w: %v", ErrMissingResponsesWriteScope, err)
	}
	return err
}
