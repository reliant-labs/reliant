// Copyright (c) 2025 Reliant Labs
package responseswire

import (
	"fmt"

	"github.com/openai/openai-go/v3/responses"

	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
)

// ContextOverflowFailure returns the error a `failed` response carries when
// that error is a context-window refusal, and nil for any other response.
//
// The Codex backend does not refuse an oversized prompt with an HTTP 400: it
// opens the stream and ends it with `response.failed` whose error is
// {"code":"context_length_exceeded","message":"Your input exceeds the context
// window of this model. ..."} (codex-rs codex-api/src/sse/responses.rs maps
// exactly this to ApiError::ContextWindowExceeded). Read as a terminal
// response, it ended the turn as an unexplained failure; as an error marked
// drivererrors.ErrContextOverflow, call_llm can shrink the request and send it
// again. Only the overflow is turned into an error here: any other failed
// response keeps the handling it has.
func ContextOverflowFailure(resp *responses.Response) error {
	if resp == nil || resp.Status != responses.ResponseStatusFailed {
		return nil
	}
	code, msg := string(resp.Error.Code), resp.Error.Message
	failure := drivererrors.ProviderFailure{Codes: []string{code}, Message: msg}
	if !failure.IsContextOverflow() {
		return nil
	}
	return drivererrors.MarkContextOverflow(fmt.Errorf("responses %s: %s: %s", resp.Status, code, msg), failure)
}
