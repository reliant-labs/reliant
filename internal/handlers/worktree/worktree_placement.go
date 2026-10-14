// Copyright (c) 2025 Reliant Labs
package worktree

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/machinewake"
)

// placeNewWorktree picks the daemon a new worktree is created on: the machine
// of the chat it is created for (chatID), else the machine holding the
// workspace it is created from (source), else, with no chat context at all,
// the user's default machine. The result is marked noMachine when the chat has
// no machine by design, so nothing done for it wakes one (see machinewake).
//
// A worktree exists on one machine, and the tools of every chat bound to it
// route there. Placing it by default resolution alone moved a chat running on
// machine B onto machine A the moment it branched into a new workspace.
//
// verb names the operation in the no-daemon error ("create", "import").
func (s *Service) placeNewWorktree(ctx context.Context, userID, projectID string, chatID *string, source *db.Worktree, verb string) (machinewake.Target, error) {
	noMachine := false
	if chatID != nil && *chatID != "" {
		chat, err := s.deps.Repo.GetChat(ctx, *chatID)
		if err != nil || chat == nil || chat.UserID != userID {
			return machinewake.Target{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
		}
		if chat.ProjectID != projectID {
			return machinewake.Target{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat belongs to a different project"))
		}
		daemonID, err := daemonplacement.ChatDaemonID(ctx, s.deps.Repo, chat)
		if err != nil {
			logging.Error("Failed to resolve the chat's machine for worktree "+verb, "error", err, "chatID", chat.ID)
			return machinewake.Target{}, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to resolve the chat's machine"))
		}
		if daemonID != "" {
			return machinewake.Target{DaemonID: daemonID, NoMachine: chat.NoMachine}, nil
		}
		noMachine = chat.NoMachine
	}

	if owner := daemonplacement.WorktreeOwner(source); owner != "" {
		return machinewake.Target{DaemonID: owner, NoMachine: noMachine}, nil
	}

	if s.deps.Router == nil {
		return machinewake.Target{}, connect.NewError(connect.CodeUnavailable, fmt.Errorf("no daemon available to %s worktree: no daemon router", verb))
	}
	daemonID, err := s.deps.Router.ResolveDaemonID(ctx, userID)
	if err != nil {
		err = s.wake().AfterFailure(ctx, userID, machinewake.Target{NoMachine: noMachine}, err)
		logging.Error("Failed to resolve daemon for worktree "+verb, "error", err, "userID", userID)
		return machinewake.Target{}, connect.NewError(connect.CodeUnavailable, fmt.Errorf("no daemon available to %s worktree: %w", verb, err))
	}
	return machinewake.Target{DaemonID: daemonID, NoMachine: noMachine}, nil
}
