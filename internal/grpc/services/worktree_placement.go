// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// chatWorktreeReader is what chatDaemonID needs to find the owner of the
// worktree a chat runs in.
type chatWorktreeReader interface {
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
}

// chatDaemonID returns the daemon a chat's tools run on, or "" when the chat
// names none and default resolution picks one.
//
// The precedence is ExecuteTools' (handlers.toolDaemonSelector): the chat's
// pinned daemon, which its workflow carries as session_daemon_id, beats the
// owner of its worktree's checkout. A branch chat is pinned to that owner
// anyway; the two differ only for a chat started in an owned worktree without
// a pin, and that chat runs on the owner.
//
// A worktree row that no longer exists means "no owner", as it does for
// routing. Any other lookup failure is returned: guessing here puts a checkout
// on the wrong machine.
func chatDaemonID(ctx context.Context, worktrees chatWorktreeReader, chat *db.Chat) (string, error) {
	if chat.ActiveDaemonID != nil && *chat.ActiveDaemonID != "" {
		return *chat.ActiveDaemonID, nil
	}
	if chat.WorktreeID == nil || *chat.WorktreeID == "" {
		return "", nil
	}
	worktree, err := worktrees.GetWorktree(ctx, *chat.WorktreeID)
	if errors.Is(err, core.ErrWorktreeNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load chat worktree %s: %w", *chat.WorktreeID, err)
	}
	return worktreeOwner(worktree), nil
}

// placeNewWorktree picks the daemon a new worktree is created on: the machine
// of the chat it is created for (chatID), else the machine holding the
// workspace it is created from (source), else, with no chat context at all,
// the user's default machine. The result is marked noMachine when the chat has
// no machine by design, so nothing done for it wakes one (see machineWake).
//
// A worktree exists on one machine, and the tools of every chat bound to it
// route there. Placing it by default resolution alone moved a chat running on
// machine B onto machine A the moment it branched into a new workspace.
//
// verb names the operation in the no-daemon error ("create", "import").
func (s *WorktreeService) placeNewWorktree(ctx context.Context, userID, projectID string, chatID *string, source *db.Worktree, verb string) (wakeTarget, error) {
	noMachine := false
	if chatID != nil && *chatID != "" {
		chat, err := s.database.GetChat(ctx, *chatID)
		if err != nil || chat == nil || chat.UserID != userID {
			return wakeTarget{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
		}
		if chat.ProjectID != projectID {
			return wakeTarget{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat belongs to a different project"))
		}
		daemonID, err := chatDaemonID(ctx, s.database, chat)
		if err != nil {
			logging.Error("Failed to resolve the chat's machine for worktree "+verb, "error", err, "chatID", chat.ID)
			return wakeTarget{}, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to resolve the chat's machine"))
		}
		if daemonID != "" {
			return wakeTarget{daemonID: daemonID, noMachine: chat.NoMachine}, nil
		}
		noMachine = chat.NoMachine
	}

	if owner := worktreeOwner(source); owner != "" {
		return wakeTarget{daemonID: owner, noMachine: noMachine}, nil
	}

	daemonID, err := s.daemonRouter.ResolveDaemonID(ctx, userID)
	if err != nil {
		err = s.wake.afterFailure(ctx, userID, wakeTarget{noMachine: noMachine}, err)
		logging.Error("Failed to resolve daemon for worktree "+verb, "error", err, "userID", userID)
		return wakeTarget{}, connect.NewError(connect.CodeUnavailable, fmt.Errorf("no daemon available to %s worktree: %w", verb, err))
	}
	return wakeTarget{daemonID: daemonID, noMachine: noMachine}, nil
}
