// Copyright (c) 2025 Reliant Labs
package codex

import (
	"errors"
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/reliant-labs/reliant/internal/logging"
)

// A reasoning item's encrypted_content is bound to the organization that
// produced it. Replayed to a different account it is a hard 400 ("The
// encrypted content ... could not be verified. Reason: Encrypted content
// organization_id did not match"). The rest of the history is fine, so the
// request is retried once with every reasoning item removed.
//
// Only the request is rewritten. Stored history is never touched.

// isUnverifiableReasoningError reports whether err is the backend refusing a
// replayed reasoning item it cannot verify.
func isUnverifiableReasoningError(err error) bool {
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		return false
	}
	text := strings.ToLower(string(errorBody(apiErr)) + " " + apiErr.Message)
	return strings.Contains(text, "encrypted content") && strings.Contains(text, "could not be verified")
}

// withoutReasoningItems returns params with every reasoning item removed from
// its input, and how many were removed. The input slice is copied, never
// modified in place.
func withoutReasoningItems(params responses.ResponseNewParams) (responses.ResponseNewParams, int) {
	items := params.Input.OfInputItemList
	kept := make(responses.ResponseInputParam, 0, len(items))
	for _, item := range items {
		if item.OfReasoning != nil {
			continue
		}
		kept = append(kept, item)
	}
	removed := len(items) - len(kept)
	if removed == 0 {
		return params, 0
	}
	params.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: kept}
	return params, removed
}

// recoverUnverifiableReasoning decides whether the failed request should be
// retried without its reasoning items. When it returns true, params has been
// replaced by the stripped request and *done is set, so a second failure
// surfaces instead of looping.
func (c *CodexClient) recoverUnverifiableReasoning(err error, params *responses.ResponseNewParams, done *bool) bool {
	if *done || !isUnverifiableReasoningError(err) {
		return false
	}
	stripped, removed := withoutReasoningItems(*params)
	if removed == 0 {
		return false
	}
	*done = true
	*params = stripped
	logging.Warn("Codex rejected a replayed reasoning item; retrying once without reasoning items",
		"chat_id", c.sessionID, "removed_items", removed, "error", err)
	return true
}
