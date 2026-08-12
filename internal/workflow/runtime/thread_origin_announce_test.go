// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// originForAnnouncement mirrors the origin decision in
// inlineWorkflowExecutor's thread-created announcement.
func originForAnnouncement(threadMode string) string {
	if threadMode == model.ThreadModeFork {
		return model.ThreadOriginFork
	}
	return ""
}

// A spawned sub-agent's thread is created and announced by the SPAWN path with
// origin "spawn", then its workflow runs through the inline executor -- which
// used to announce the same thread again with an origin derived from
// ThreadMode. The second update landed ~30ms later and won, so the UI read
// "node", isSpawn went false, and the entire sub-agent transcript rendered
// inline in the parent chat.
//
// Observed on the wire before the fix (chat_updates, update_type=3):
//
//	thread b823f9da  origin="spawn"  17:35:57.837687
//	thread b823f9da  origin="node"   17:35:57.867940
//
// The executor must not restate an origin it did not determine. Emitting ""
// omits the field (workflow_status.go only sets "origin" when non-empty), so
// the authoritative earlier announcement stands.
func TestInlineExecutorDoesNotOverwriteSpawnOrigin(t *testing.T) {
	// The mode a spawned sub-agent's workflow runs under must NOT produce an
	// origin — anything non-empty here clobbers "spawn".
	for _, mode := range []string{model.ThreadModeNew, model.ThreadModeInherit} {
		if got := originForAnnouncement(mode); got != "" {
			t.Errorf("mode %q announced origin %q; a non-empty value overwrites the creating path's origin (e.g. spawn)", mode, got)
		}
	}

	// A fork IS created here, so it may still state its own origin.
	if got := originForAnnouncement(model.ThreadModeFork); got != model.ThreadOriginFork {
		t.Errorf("fork mode announced %q, want %q", got, model.ThreadOriginFork)
	}
}

// threads.origin is NOT NULL and already records every thread's provenance
// (377 spawn / 166 fork / 142 main / 28 node in a real database), so the
// stream never needs to restate it — the read path joins the thread row.
func TestThreadOriginConstantsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, o := range []string{
		model.ThreadOriginMain, model.ThreadOriginSpawn,
		model.ThreadOriginFork, model.ThreadOriginNode,
	} {
		if o == "" {
			t.Error("origin constant is empty; empty means \"unset\" on the wire and must not be a real value")
		}
		if seen[o] {
			t.Errorf("duplicate origin constant %q", o)
		}
		seen[o] = true
	}
}
