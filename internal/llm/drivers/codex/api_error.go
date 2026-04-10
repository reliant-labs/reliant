// Copyright (c) 2025 Reliant Labs
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
