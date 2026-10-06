// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// integrationAvailabilityTimeout bounds asking whether the run's owner can use
// its integrations. For GitHub on a hosted deployment that is a control-plane
// round trip, and a slow one must cost the turn its integration tools, not
// the turn.
const integrationAvailabilityTimeout = 5 * time.Second

// withUsableIntegrations narrows a run's reach — what the model is handed AND
// what load_tool may add — to the integration tools its owner can
// authenticate. An integration that needs a connection (GitHub, Slack, Gmail,
// Twilio) is offered only when the run's owner has a usable one, or a
// delegated authority serves it; one that needs none (http__request) is never
// touched. tag:integration and "*" keep their meaning: every integration tool
// this owner can use.
//
// Availability is asked of the credential source the tools execute through,
// for the run they would execute in, so "offered" and "will authenticate" are
// one answer rather than two that can drift. It fails closed: no source, an
// unknown owner, or a source that cannot answer offers nothing gated.
//
// A node that cannot reach any gated tool never asks.
func withUsableIntegrations(ctx context.Context, factory *tools.ToolsFactory, chat *db.Chat, thread string, access tools.ToolAccess, names, mcpToolNames []string) (tools.ToolAccess, []string) {
	registry := registryToolNames()
	reached := reachedGatedIntegrations(access, names, registry)
	if len(reached) == 0 {
		return access, names
	}
	ctx, cancel := context.WithTimeout(ctx, integrationAvailabilityTimeout)
	defer cancel()
	usable, err := factory.UsableIntegrations(ctx, chat.ID, thread, reached)
	if err != nil {
		slog.Warn("[CallLLM] Could not establish which integrations the run's owner can use; withholding the rest",
			"chatID", chat.ID, "thread", thread, "integrations", reached, "usable", usable, "error", err)
	}
	return withoutUnusableIntegrations(access, names, mcpToolNames, registry, usable)
}

func registryToolNames() []string {
	defs := tools.GetToolRegistry()
	names := make([]string, len(defs))
	for i, def := range defs {
		names[i] = def.Name
	}
	return names
}

// reachedGatedIntegrations lists the connection-gated integrations a node can
// reach at all, sorted. "Load anything" reaches every one in the registry.
func reachedGatedIntegrations(access tools.ToolAccess, names, registry []string) []string {
	lists := [][]string{names, access.Preloaded, access.Loadable}
	if access.LoadableAll {
		lists = append(lists, registry)
	}
	seen := map[string]bool{}
	for _, list := range lists {
		for _, name := range list {
			if id, gated := tools.ConnectionGatedIntegration(name); gated {
				seen[id] = true
			}
		}
	}
	reached := make([]string, 0, len(seen))
	for id := range seen {
		reached = append(reached, id)
	}
	sort.Strings(reached)
	return reached
}

// withoutUnusableIntegrations drops every connection-gated tool whose
// integration is not in usable. "Load anything" stays exactly that when
// nothing is withheld; otherwise it becomes an explicit list — the registry
// and the connected MCP tools, minus what was withheld — because the registry
// is the universe load_tool searches, and an unrestricted scope is never
// filtered per name.
func withoutUnusableIntegrations(access tools.ToolAccess, names, mcpToolNames, registry []string, usable map[string]bool) (tools.ToolAccess, []string) {
	withheld := func(name string) bool {
		id, gated := tools.ConnectionGatedIntegration(name)
		return gated && !usable[id]
	}
	keep := func(list []string) []string {
		kept := make([]string, 0, len(list))
		for _, name := range list {
			if !withheld(name) {
				kept = append(kept, name)
			}
		}
		return kept
	}

	narrowed := tools.ToolAccess{Preloaded: keep(access.Preloaded), Loadable: keep(access.Loadable)}
	if access.LoadableAll {
		if loadable := keep(registry); len(loadable) < len(registry) {
			narrowed.Loadable = append(loadable, mcpToolNames...)
		} else {
			narrowed.LoadableAll, narrowed.Loadable = true, nil
		}
	}
	return narrowed, keep(names)
}
