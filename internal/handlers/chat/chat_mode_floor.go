// Copyright (c) 2025 Reliant Labs

package chat

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/launch"
)

// withLiveModeFloor holds a live run to its org's chat mode floor (control-
// plane docs/design/org-policies.md §4.4, NO_AUTO) at the next turn, whether or
// not the send carries params: a chat that began in `auto` before an admin
// set the floor gets `mode: manual` in the update that reaches its run with
// this message, so its next turn asks before every tool call. A mode the send
// itself carries was floored where it was built
// (Launcher.BuildStateUpdateForActiveWorkflow). `plan` is never touched.
//
// The run's own current mode decides; when the run cannot say (its query
// failed), the chat's mode does (Launcher.ChatMode, floored), the one the
// toolbar sets and every run of the chat takes. update may be nil; the result
// is nil only when it was and nothing changed.
func (s *Service) withLiveModeFloor(ctx context.Context, userID string, chat *db.Chat, workflowName string, update map[string]interface{}) map[string]interface{} {
	if _, sent := update["mode"]; sent {
		return update
	}
	if !launch.ChatModeFloorEnforced(ctx, chat) {
		return update
	}
	current, known := s.runInputsForSend(ctx, chat.MainThreadID())["mode"].(string)
	floored := current
	if known {
		probe := map[string]interface{}{"mode": current}
		launch.FloorChatMode(ctx, chat, probe)
		floored, _ = probe["mode"].(string)
	} else if mode, ok, err := s.launcher().ChatMode(ctx, userID, chat, workflowName); err == nil && ok {
		// The chat's mode, already floored by the build: auto became
		// manual, plan stayed plan.
		floored, _ = mode.(string)
	}
	if floored != "manual" || floored == current {
		return update
	}
	if update == nil {
		update = map[string]interface{}{}
	}
	update["mode"] = floored
	return update
}
