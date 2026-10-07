// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Greenfield stack guidance is seeded by the RUN, not by the launch: a chat's
// first run is started with WorkflowInput.GreenfieldProbe, and before its first
// LLM call it asks the daemon whether the working directory holds code
// (runtime.runGreenfieldProbe, handlers.GreenfieldProbeActivity, where the
// guidance and its rationale live). The request path only decides whether
// this is a first turn — which a new chat is by construction (Spec.GreenfieldProbe)
// and an existing chat has to count — and never waits on a machine that may be
// remote, suspended or cold.

// WantsGreenfieldProbe reports whether a fresh run of an EXISTING chat is still
// that chat's first turn, and so should probe for greenfield. Call it before
// the run's messages are saved.
//
// First turn only: the guidance is about how to START, so it is noise on a
// conversation that is already underway — and on later turns the model has
// the user's own words about the stack, which are better evidence than a
// directory listing. A chat with no machine has no directory to ask about.
func (l *Launcher) WantsGreenfieldProbe(ctx context.Context, chat *db.Chat) bool {
	if l == nil || chat == nil || chat.NoMachine {
		return false
	}
	count, err := l.repo.CountMessagesInChat(ctx, chat.ID)
	if err != nil {
		logging.Warn("Greenfield probe: could not count chat messages; skipping",
			"error", err, "chatID", chat.ID)
		return false
	}
	return count == 0
}
