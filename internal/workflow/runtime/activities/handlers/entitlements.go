// Copyright (c) 2026 Reliant Labs
package handlers

import (
	"context"
	"log/slog"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/featuregate"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// entitlementTimeout bounds asking whether the run's owner is entitled to the
// gated features a declaration reaches. The gate's membership check is a
// control-plane round trip on a hosted deployment, and a slow one must cost
// the turn its gated tools, not the turn.
const entitlementTimeout = 5 * time.Second

// entitledFeatures asks the gate which of the features behind the gated tools
// a declaration reaches the run's owner is entitled to in the run's org. The
// answer is the capability resolver's EntitledFeatures input: a gated tool
// whose feature is not in it is excluded as if it did not exist.
//
// It fails closed, as usableIntegrations does: no gate, a chat with no owner
// or no org, a refusal, a timeout or an error leaves the feature out of the
// answer, and the resolver excludes every gated tool not in it. A declaration
// that reaches no gated tool never asks.
func entitledFeatures(ctx context.Context, gate featuregate.Gate, chat *db.Chat, access tools.ToolAccess) map[string]bool {
	reached := tools.ReachedGatedFeatures(access)
	if len(reached) == 0 || gate == nil || chat == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, entitlementTimeout)
	defer cancel()
	orgID := chatOrgID(chat)
	var entitled map[string]bool
	for _, f := range reached {
		allowed, err := gate.Allowed(ctx, f, chat.UserID, orgID)
		if err != nil {
			slog.WarnContext(ctx, "[CallLLM] Could not establish whether the run's owner is entitled to a feature; withholding its tools",
				"chatID", chat.ID, "feature", string(f), "error", err)
			continue
		}
		if !allowed {
			continue
		}
		if entitled == nil {
			entitled = map[string]bool{}
		}
		entitled[string(f)] = true
	}
	return entitled
}
