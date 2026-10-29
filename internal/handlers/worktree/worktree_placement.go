// Copyright (c) 2025 Reliant Labs
package worktree

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/daemonchoice"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/machinewake"
)

// placeNewWorktree picks the daemon a new worktree is created on: the machine
// of the chat it is created for (chatID), else the machine holding the
// workspace it is created from (source), else the project's default machine
// (daemonplacement.ProjectDefaultDaemonID: default resolution narrowed to the
// machines that have the project). The result is marked noMachine when the chat has
// no machine by design, so nothing done for it wakes one (see machinewake).
//
// A worktree exists on one machine, and the tools of every chat bound to it
// route there. Placing it by default resolution alone moved a chat running on
// machine B onto machine A the moment it branched into a new workspace. The
// same goes for a chat that was never pinned (it started on the default
// machine): its workspace belongs where its tools run, so it is placed by the
// rule that pins it, not by whichever machine connected last
// (research/CHAT_MACHINE_PLACEMENT.md).
//
// requested is the machine the caller named (the new-chat composer's "Runs
// on"), "" for none. It must be one of the caller's machines the project is
// installed on, and it may not contradict the chat's machine or the source
// workspace's: the new checkout belongs with them.
//
// verb names the operation in the no-daemon error ("create", "import").
func (s *Service) placeNewWorktree(ctx context.Context, userID, projectID string, chatID *string, source *db.Worktree, requested *string, verb string) (machinewake.Target, error) {
	if requested != nil && *requested != "" {
		return s.placeOnRequestedMachine(ctx, userID, projectID, chatID, source, *requested)
	}
	noMachine := false
	if chatID != nil && *chatID != "" {
		chat, err := s.deps.Repo.GetChat(ctx, *chatID)
		if err != nil {
			return machinewake.Target{}, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
		}
		// Someone else's chat is not found, so the answer never reveals it.
		if chat == nil || chat.UserID != userID {
			return machinewake.Target{}, svcerr.NotFound("chat")
		}
		if chat.ProjectID != projectID {
			return machinewake.Target{}, svcerr.InvalidArgument("chat belongs to a different project")
		}
		daemonID, err := daemonplacement.ChatDaemonID(ctx, s.deps.Repo, chat)
		if err != nil {
			return machinewake.Target{}, svcerr.WrapOr(err, svcerr.Internal("failed to resolve the chat's machine"))
		}
		if daemonID != "" {
			return machinewake.Target{DaemonID: daemonID, NoMachine: chat.NoMachine}, nil
		}
		noMachine = chat.NoMachine
	}

	if owner := s.owner(ctx, userID, source); owner != "" {
		return machinewake.Target{DaemonID: owner, NoMachine: noMachine}, nil
	}

	if s.deps.Router == nil {
		return machinewake.Target{}, svcerr.WithReason(
			svcerr.Unavailable(fmt.Sprintf("no daemon available to %s worktree: no daemon router", verb)), errreason.NoDaemon)
	}
	daemonID, err := s.defaultMachine(ctx, userID, projectID)
	if err != nil {
		err = s.wake().AfterFailure(ctx, userID, machinewake.Target{NoMachine: noMachine}, err)
		// "no daemon available" is a marker the web keys on, so it leads the
		// message. A machine state picks the code daemonerr gives it everywhere
		// and appends the router's sentence for it ("no daemon connected …
		// still starting"), the marker the web's machine wait keys on; any
		// other resolution failure is Unavailable, its text withheld.
		doing := fmt.Sprintf("no daemon available to %s worktree", verb)
		if serr, ok := daemonerr.MachineState(doing, err); ok {
			return machinewake.Target{}, serr
		}
		return machinewake.Target{}, svcerr.WithReason(svcerr.WithCause(svcerr.Unavailable(doing), err), errreason.NoDaemon)
	}
	return machinewake.Target{DaemonID: daemonID, NoMachine: noMachine}, nil
}

// placeOnRequestedMachine checks a machine the caller named for a new
// workspace and returns it as the placement.
func (s *Service) placeOnRequestedMachine(ctx context.Context, userID, projectID string, chatID *string, source *db.Worktree, requested string) (machinewake.Target, error) {
	if err := daemonchoice.Validate(ctx, s.deps.Repo, userID, projectID, requested); err != nil {
		return machinewake.Target{}, err
	}
	if _, conflict := daemonplacement.WorkspaceMachineConflict(source, requested); conflict {
		return machinewake.Target{}, svcerr.InvalidArgument("daemon_id conflicts with the source workspace's machine: a copy of a workspace is made on the machine that holds it")
	}
	if chatID != nil && *chatID != "" {
		chat, err := s.deps.Repo.GetChat(ctx, *chatID)
		if err != nil {
			return machinewake.Target{}, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
		}
		if chat == nil || chat.UserID != userID {
			return machinewake.Target{}, svcerr.NotFound("chat")
		}
		chatDaemon, err := daemonplacement.ChatDaemonID(ctx, s.deps.Repo, chat)
		if err != nil {
			return machinewake.Target{}, svcerr.WrapOr(err, svcerr.Internal("failed to resolve the chat's machine"))
		}
		if chatDaemon != "" && chatDaemon != requested {
			return machinewake.Target{}, svcerr.InvalidArgument("daemon_id conflicts with the chat's machine: a chat's workspace is made where the chat runs")
		}
	}
	return machinewake.Target{DaemonID: requested}, nil
}

// defaultMachine is the project-aware default (daemonplacement.
// ProjectDefaultDaemonID) when the router can place a machine, and plain
// default resolution when it cannot (a test double, or a router with no
// registry). Without a router there is no machine to resolve.
func (s *Service) defaultMachine(ctx context.Context, userID, projectID string) (string, error) {
	if s.deps.Router == nil {
		return "", errors.New("no daemon router")
	}
	machines, ok := s.deps.Router.(daemonplacement.Machines)
	if !ok {
		return s.deps.Router.ResolveDaemonID(ctx, userID)
	}
	return daemonplacement.ProjectDefaultDaemonID(ctx, s.deps.Repo, machines, userID, projectID)
}

// owner is the machine holding worktree's checkout ("" for default
// resolution): the one its row records, or, for a row from before owners were
// recorded, the one connected machine found to hold it, recorded on the row
// (daemonplacement.Resolver). Every command about an existing workspace goes
// through here, so an unattributed workspace is settled the first time it is
// opened instead of being sent to the user's default machine.
func (s *Service) owner(ctx context.Context, userID string, worktree *db.Worktree) string {
	return s.owners.Owner(ctx, userID, worktree)
}
