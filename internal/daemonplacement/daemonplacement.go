// Copyright (c) 2025 Reliant Labs

// Package daemonplacement answers which of a user's machines (daemons) a
// worktree's checkout, and a chat's tools, live on. A request about an
// existing worktree or about a chat's files has to reach that machine: the
// checkout exists nowhere else.
//
// It also decides where a chat that names no machine goes (PinTarget,
// ProjectDefaultDaemonID) and records that decision (Pinner), so the answer
// is made once instead of by default resolution on every tool call: see
// research/CHAT_MACHINE_PLACEMENT.md.
//
//forge:exclude-contract: placement rules over chat, worktree and project-install rows; every read goes through an interface its caller passes (WorktreeReader, Installs, Machines, PinStore) and the only state is the Pinner's two handles
package daemonplacement

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// WorktreeOwner is the daemon holding a worktree's checkout on disk, or "" when
// the row names none — the main checkout, which every machine with the project
// has, and rows from before ownership was recorded. "" leaves default
// resolution.
//
// Every command about an existing worktree goes to its owner. The checkout
// exists nowhere else, so sending one to the default daemon reports a missing
// directory at best, and at worst acts on a same-named path on another machine.
func WorktreeOwner(worktree *db.Worktree) string {
	if worktree == nil || worktree.DaemonID == nil {
		return ""
	}
	return *worktree.DaemonID
}

// WorktreeReader is what ChatDaemonID needs to find the owner of the
// worktree a chat runs in.
type WorktreeReader interface {
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
}

// ChatDaemonID returns the daemon a chat's tools run on, or "" when the chat
// names none and default resolution picks one.
//
// The rule is ExecuteTools' (handlers.toolDaemonSelector): a chat in a branch
// workspace that a machine owns runs on that owner, whatever its pin says.
// The checkout exists on no other disk, so a pin naming another machine is
// stale (from before StartChat refused it, or a workspace whose owner was
// recorded after the chat was pinned) and yields, as a session pin does for
// tools. Otherwise the chat's pin (session_daemon_id), else "".
//
// A worktree row that no longer exists means "no owner", as it does for
// routing. Any other lookup failure is returned: guessing here puts a checkout
// on the wrong machine.
func ChatDaemonID(ctx context.Context, worktrees WorktreeReader, chat *db.Chat) (string, error) {
	if chat.WorktreeID != nil && *chat.WorktreeID != "" {
		worktree, err := worktrees.GetWorktree(ctx, *chat.WorktreeID)
		switch {
		case errors.Is(err, core.ErrWorktreeNotFound):
		case err != nil:
			return "", fmt.Errorf("load chat worktree %s: %w", *chat.WorktreeID, err)
		default:
			if owner := WorktreeOwner(worktree); owner != "" {
				return owner, nil
			}
		}
	}
	if chat.ActiveDaemonID != nil && *chat.ActiveDaemonID != "" {
		return *chat.ActiveDaemonID, nil
	}
	return "", nil
}
