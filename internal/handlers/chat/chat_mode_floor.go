// Copyright (c) 2025 Reliant Labs

package chat

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"

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
// toolbar sets and every run of the chat takes. When neither can say, the
// send is refused retryably: waking an auto run unpoliced is not an option,
// and forcing manual could loosen a plan run. update may be nil; the result
// is nil only when it was and nothing changed.
func (s *Service) withLiveModeFloor(ctx context.Context, userID string, chat *db.Chat, workflowName string, update map[string]interface{}) (map[string]interface{}, error) {
	if _, sent := update["mode"]; sent {
		return update, nil
	}
	if !launch.ChatModeFloorEnforced(ctx, chat) {
		return update, nil
	}
	current, known := s.runInputsForSend(ctx, chat.MainThreadID())["mode"].(string)
	var floored string
	if known {
		probe := map[string]interface{}{"mode": current}
		launch.FloorChatMode(ctx, chat, probe)
		floored, _ = probe["mode"].(string)
	} else {
		mode, ok, err := s.launcher().ChatMode(ctx, userID, chat, workflowName)
		if err != nil || !ok {
			return nil, svcerr.Unavailable("This chat's mode could not be read, and its organization's policy requires approvals; try sending again shortly.")
		}
		// The chat's mode, already floored by the build: auto became
		// manual, plan stayed plan.
		floored, _ = mode.(string)
	}
	if floored != "manual" || floored == current {
		return update, nil
	}
	if update == nil {
		update = map[string]interface{}{}
	}
	update["mode"] = floored
	return update, nil
}
