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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/openai/openai-go/v3"
)

// ErrMissingResponsesWriteScope means the access token is valid but lacks
// api.responses.write (common after OpenAI scope changes); user must re-run OAuth.
var ErrMissingResponsesWriteScope = errors.New("codex token missing api.responses.write; reconnect with Login with Codex in Settings")

// AugmentAPIError wraps Codex HTTP/API errors with a clearer message when the
// backend reports missing_scope for the responses API, and recovers the reason
// text from Codex responses the OpenAI SDK cannot decode.
func AugmentAPIError(err error) error {
	if err == nil {
		return nil
	}
	err = withCodexDetail(err)
	msg := err.Error()
	if strings.Contains(msg, "api.responses.write") &&
		(strings.Contains(msg, "missing_scope") || strings.Contains(msg, "insufficient permissions")) {
		return fmt.Errorf("%w: %v", ErrMissingResponsesWriteScope, err)
	}
	return err
}

// withCodexDetail appends the Codex backend's `detail` text to an API error the
// SDK rendered without a reason.
//
// The SDK unwraps only a top-level `error` object when building its error
// (requestconfig does gjson.GetBytes(contents, "error")). The Codex backend
// does not use that shape — it reports refusals as a bare top-level `detail`
// string:
//
//	{"detail":"The 'gpt-5.3-codex-spark' model is not supported when using
//	 Codex with a ChatGPT account."}
//
// so the decode yields nothing and Error() renders as `POST "…": 400 Bad
// Request` with an empty message. That empty 400 is actively misleading: it
// reads as a malformed-request problem, and a titling failure carrying it was
// diagnosed as "the backend rejects non-streaming requests" when the real cause
// was an unservable model. The fix shipped, the bug stayed.
//
// The SDK leaves the unread body on the response for debugging, so the reason
// is recoverable here rather than lost.
func withCodexDetail(err error) error {
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) || apiErr.Response == nil || apiErr.Response.Body == nil {
		return err
	}
	// A body the SDK already decoded needs no help.
	if apiErr.Message != "" {
		return err
	}

	raw, readErr := io.ReadAll(io.LimitReader(apiErr.Response.Body, 8192))
	_ = apiErr.Response.Body.Close()
	// Restore the body so anything downstream (DumpResponse) still sees it.
	apiErr.Response.Body = io.NopCloser(bytes.NewReader(raw))
	if readErr != nil || len(raw) == 0 {
		return err
	}

	var payload struct {
		Detail string `json:"detail"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.Detail == "" {
		return err
	}

	return fmt.Errorf("%w: %s", err, payload.Detail)
}
