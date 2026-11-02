// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/projectcheckout"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// A CHAT'S MAIN CHECKOUT IS RESOLVED ON THE MACHINE THAT RUNS ITS CALLS.
//
// projects.path is where the project was created, on the machine that
// created it; each machine's own checkout is its project_daemons row. So the
// directory a call runs in is resolved where the machine is known — at
// dispatch — from (machine, worktree):
//
//   - a branch worktree runs at worktree.Path, on its owner (toolDaemonSelector
//     routes the call there);
//   - the main checkout is the machine's own row (projectcheckout.On). A path
//     the run computed for another machine — the chat moved, or a node's
//     working directory under the checkout — is rebased onto it.
//
// A machine with no checkout of the project does not stop the conversation:
// the call (or batch) is refused with the reason and the way out — the
// machines that have it, or cloning it here — as its result, and the next
// call re-resolves, so cloning or switching machine unblocks the chat.

// checkoutPurpose finishes projectcheckout's "… to <purpose>".
const checkoutPurpose = "keep working in this chat"

// callCheckout is where a call (or a batch of them) runs.
type callCheckout struct {
	// daemonID is the concrete machine; "" when none resolves (no router, no
	// machine connected): dispatch then reports the machine's state itself.
	daemonID string
	// main is the project's checkout on daemonID — zero when it has none.
	main projectcheckout.Target
	// missing is the actionable answer when the call runs in the main
	// checkout and daemonID has none (projectcheckout.NotOnError).
	missing error
}

// machinesOf is router's placement, when it has one (NATSDaemonRouter).
func machinesOf(router toolexec.DaemonRouter) daemonplacement.Machines {
	if m, ok := router.(daemonplacement.Machines); ok {
		return m
	}
	return nil
}

// resolveCallCheckout resolves selector to its machine and the project's
// checkout there. inMain says the call runs in the main checkout (not a
// branch worktree), so a machine without one is a refusal, not a detail.
func resolveCallCheckout(ctx context.Context, r projectcheckout.Reader, router toolexec.DaemonRouter, project *db.Project, selector *toolexec.DaemonSelector, inMain bool) callCheckout {
	out := callCheckout{daemonID: configMachine(ctx, router, project.UserID, selector)}
	target, err := projectcheckout.ForMachine(ctx, r, machinesOf(router), project.UserID, project, out.daemonID, checkoutPurpose)
	switch {
	case err == nil:
		out.main = target
		if out.daemonID == "" && selector == nil {
			// No machine was chosen up front (no router, or none resolved):
			// the call goes to the machine the directory was resolved
			// from. Left to resolve again at dispatch, it could reach a
			// machine that connected in between, carrying this one's path.
			out.daemonID = target.DaemonID
		}
	case !inMain:
		// A branch worktree runs without the main checkout; its repairs and
		// repo list just go without a source.
	case out.daemonID == "" && !projectcheckout.IsMissing(err):
		// No machine resolved and the records could not be read: leave
		// the call to dispatch, which reports the machine's own state.
		logging.DebugContext(ctx, "[checkout] could not resolve the project's checkout", "error", err, "projectID", project.ID)
	default:
		out.missing = err
	}
	return out
}

// pin names the resolved machine outright, so the call runs where its
// directory was resolved: a selector by name or label, or none, could
// otherwise reach a machine whose disk holds a different path.
func (c callCheckout) pin(selector *toolexec.DaemonSelector) *toolexec.DaemonSelector {
	if c.daemonID == "" || (selector != nil && selector.ID != "") {
		return selector
	}
	return &toolexec.DaemonSelector{ID: c.daemonID}
}

// path is the directory a call runs in: the run's path rebased onto this
// machine's checkout, else the branch worktree's, else the checkout itself.
// A thread-bound worktree is the caller's to apply before this.
func (c callCheckout) path(runPath, branchWorktreePath string) string {
	switch {
	case runPath != "" && c.main.Path != "":
		return c.main.Rebase(runPath)
	case runPath != "":
		return runPath
	case branchWorktreePath != "":
		return branchWorktreePath
	default:
		return c.main.Path
	}
}

// missingRefusal is the text a call refused for c.missing returns: the
// actionable reason, that nothing ran, and that retrying cannot help — so the
// model stops and asks the user, who is the only one who can switch the
// chat's machine or clone the project. (Each call does re-check, so once the
// user has acted the next turn just works.)
func missingRefusal(err error) string {
	return svcerr.ToConnect(err).Message() + ". No machine tools ran. Retrying will not help: stop calling tools that need the machine, " +
		"tell the user exactly this, and ask them to switch this chat's machine or clone the project; it works on the next turn once they have."
}
