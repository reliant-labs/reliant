// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"log/slog"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// integrationAvailabilityTimeout bounds asking whether the run's owner can use
// its integrations. For GitHub on a hosted deployment that is a control-plane
// round trip, and a slow one must cost the turn its integration tools, not
// the turn.
const integrationAvailabilityTimeout = 5 * time.Second

// usableIntegrations asks which of the connection-gated integrations a
// declaration reaches the run's owner can authenticate now. The answer is the
// capability resolver's UsableIntegrations input: an integration that needs a
// connection (GitHub, Slack, Gmail, Twilio) is offered — handed to the model
// or reachable through load_tool — only when its owner has a usable one, or a
// delegated authority serves it; one that needs none (http__request) is never
// gated. tag:integration and "*" keep their meaning: every integration tool
// this owner can use.
//
// Availability is asked of the credential source the tools execute through,
// for the run they would execute in, so "offered" and "will authenticate" are
// one answer rather than two that can drift. It fails closed — no source, an
// unknown owner, or a source that cannot answer leaves the integration out of
// the answer, and the resolver withholds every integration not in it.
//
// A declaration that cannot reach any gated tool never asks.
func usableIntegrations(ctx context.Context, factory *tools.ToolsFactory, chat *db.Chat, thread string, access tools.ToolAccess) map[string]bool {
	reached := tools.ReachedGatedIntegrations(access)
	if len(reached) == 0 || factory == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, integrationAvailabilityTimeout)
	defer cancel()
	usable, err := factory.UsableIntegrations(ctx, chat.ID, thread, reached)
	if err != nil {
		slog.Warn("[CallLLM] Could not establish which integrations the run's owner can use; withholding the rest",
			"chatID", chat.ID, "thread", thread, "integrations", reached, "usable", usable, "error", err)
	}
	return usable
}
