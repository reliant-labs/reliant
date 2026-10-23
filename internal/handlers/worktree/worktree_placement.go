// Copyright (c) 2025 Reliant Labs
package worktree

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/errreason"
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

	if owner := daemonplacement.WorktreeOwner(source); owner != "" {
		return machinewake.Target{DaemonID: owner, NoMachine: noMachine}, nil
	}

	if s.deps.Router == nil {
		return machinewake.Target{}, svcerr.WithReason(
			svcerr.Unavailable(fmt.Sprintf("no daemon available to %s worktree: no daemon router", verb)), errreason.NoDaemon)
	}
	daemonID, err := s.deps.Router.ResolveDaemonID(ctx, userID)
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
