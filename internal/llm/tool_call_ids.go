// Copyright (c) 2025 Reliant Labs
package llm

import (
	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/logging"
)

// ToolCallIDs gives the tool calls of ONE model response their ids.
//
// A driver that relays a third-party server's ids (a local OpenAI-compatible
// server, an OpenRouter upstream) cannot assume they are usable: a server may
// omit the id, or repeat one within a response. Reliant keys a call's record,
// its result and a spawn's report by the id, so an empty id dropped the call
// and a repeated one made two calls one. Assign keeps a provider id that is
// non-empty and not yet used in this response -- the server pairs results with
// calls by it -- and otherwise mints call_<uuid>, which a stateless
// OpenAI-compatible server accepts because it only needs the ids in one
// request to agree with each other.
//
// One value per response; the zero value is ready to use.
type ToolCallIDs struct {
	used map[string]struct{}
}

// Assign returns the id the next call of the response should carry, given
// the id the provider sent for it.
func (ids *ToolCallIDs) Assign(providerID string) string {
	if ids.used == nil {
		ids.used = make(map[string]struct{})
	}
	id := providerID
	if _, taken := ids.used[id]; id == "" || taken {
		id = "call_" + uuid.New().String()
		if providerID != "" {
			logging.Warn("[LLM] Provider repeated a tool call id within one response; giving the call its own",
				"provider_id", providerID, "assigned_id", id)
		}
	}
	ids.used[id] = struct{}{}
	return id
}
