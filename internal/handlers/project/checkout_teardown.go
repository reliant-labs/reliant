package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/checkoutteardown"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// A remove or reclone deletes the project's checkout on one machine, and
// every branch workspace of the project there is a git worktree of it. So
// each is a recorded operation (reliant#2429, design 1; checkoutteardown):
//
//  1. beginTeardown refuses while workspaces depend on the checkout, and
//     records the op under the checkout lock workspace creation shares. From
//     then on, no workspace of the checkout is created until the op settles.
//  2. The command carries the op's id. A daemon that knows ids runs it at
//     most once per id, journaled, and acknowledges its state; an older one
//     answers the old way, which is its synchronous outcome.
//  3. settleTeardown records what the machine said. An op the machine is
//     still running, or whose answer was lost, stays pending: a retry of the
//     same call sends the same id (the machine answers how it ended, never
//     deletes twice), and workspace creation asks the machine itself.

// teardownTimeoutMs is how long the server waits for a remove or reclone's
// answer. The op runs on after that on a daemon that knows ids.
const teardownTimeoutMs int32 = 30_000

// teardownMachine sends commands to the one machine holding the checkout
// (checkoutteardown.Machine).
type teardownMachine struct {
	s                *Service
	userID, daemonID string
}

func (m teardownMachine) Send(ctx context.Context, commandType string, payload, resp any, timeoutMs int32) error {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	if m.s.deps.Router == nil {
		return fmt.Errorf("daemon command %s: %w", commandType, errNoDaemonRouter)
	}
	respBytes, err := m.s.deps.Router.SendDaemonCommandToDaemon(ctx, m.userID, m.daemonID, commandType, payloadBytes, timeoutMs)
	if err != nil {
		return fmt.Errorf("daemon command %s: %w", commandType, err)
	}
	if resp != nil {
		if err := json.Unmarshal(respBytes, resp); err != nil {
			return fmt.Errorf("unmarshal response for %s: %w", commandType, err)
		}
	}
	return nil
}

// beginTeardown records a kind teardown of pd on daemonID and returns the op
// to send, or done=true when a pending op it would have retried turns out to
// have finished (nothing is to be sent). A pending op of the same kind is a
// retry; one of another kind blocks this teardown. Either way the machine is
// asked how the pending op ended BEFORE anything is sent: an older daemon
// ignores op ids, so resending to it would start a second delete beside a
// first that may still be running, and it is refused instead until the op's
// grace has passed. A daemon that knows ids answers from its journal, and
// resending to it only waits for the op it already has.
func (s *Service) beginTeardown(ctx context.Context, userID string, project *db.Project, pd *core.ProjectDaemon, daemonID, kind, machine string) (op *db.CheckoutTeardown, done bool, err error) {
	before := "removing the project from that machine"
	if kind == db.CheckoutTeardownReclone {
		before = "recloning the project there"
	}
	guard := func(txCtx context.Context) error {
		return s.requireNoWorkspacesOn(txCtx, project, daemonID, machine, before)
	}
	m := teardownMachine{s: s, userID: userID, daemonID: daemonID}
	for attempt := 0; attempt < 3; attempt++ {
		op, err := s.deps.Repo.BeginCheckoutTeardown(ctx, project.ID, daemonID, kind, pd.Path, guard)
		var busy *db.CheckoutTeardownBusyError
		var pending *db.CheckoutTeardown
		switch {
		case errors.As(err, &busy):
			pending = busy.Teardown
		case err != nil:
			// The guard's refusal keeps its code and sentence.
			return nil, false, svcerr.WrapOr(err, svcerr.Internal("failed to record the teardown"))
		case op.Reused:
			pending = op
		default:
			return op, false, nil
		}
		settled, qerr := checkoutteardown.Query(ctx, s.deps.Repo, m, pending, time.Now())
		if qerr != nil {
			return nil, false, s.unsettledRefusal(pending, project, machine, qerr)
		}
		switch {
		case settled.State == db.CheckoutTeardownPending && busy == nil:
			// The machine has this very op and is still running it: send
			// it again, which waits for it there and deletes nothing more.
			return settled, false, nil
		case settled.State == db.CheckoutTeardownPending:
			return nil, false, s.unsettledRefusal(pending, project, machine, nil)
		case settled.State == db.CheckoutTeardownDone && busy == nil:
			return settled, true, nil
		}
		// Settled otherwise (failed, not run, expired, or another kind
		// that finished): record a new op.
	}
	return nil, false, svcerr.WithClass(svcerr.Aborted(fmt.Sprintf(
		"the checkout of %q on %s kept changing while this was being started; try again", project.Name, machine)), svcerr.ClassUser)
}

// unsettledRefusal is the refusal for a teardown that must wait for pending,
// whose outcome the machine could not (cause) or did not yet give.
func (s *Service) unsettledRefusal(pending *db.CheckoutTeardown, project *db.Project, machine string, cause error) error {
	what := fmt.Sprintf("a %s of %q on %s", pending.Kind, project.Name, machine)
	var legacy *checkoutteardown.LegacyPendingError
	switch {
	case errors.As(cause, &legacy):
		return svcerr.WithCause(svcerr.WithClass(svcerr.FailedPrecondition(fmt.Sprintf(
			"%s was sent to a machine whose Reliant is too old to confirm it finished, so nothing more is sent to it "+
				"until %s; try again then, or as soon as the machine is updated",
			what, legacy.Until.UTC().Format("15:04 MST"))), svcerr.ClassUser), cause)
	case cause != nil && daemonerr.Unreachable(cause):
		return svcerr.WithCause(svcerr.WithClass(svcerr.Unavailable(fmt.Sprintf(
			"%s has not been confirmed finished and the machine is offline; bring it online and try again", what)), svcerr.ClassUser), cause)
	case cause != nil:
		return svcerr.WithCause(svcerr.WithClass(svcerr.Unavailable(fmt.Sprintf(
			"%s has not been confirmed finished and the machine could not say how it ended; nothing was sent, try again", what)), svcerr.ClassUser), cause)
	}
	return svcerr.WithClass(svcerr.FailedPrecondition(fmt.Sprintf(
		"%s has not finished yet; try again once it has", what)), svcerr.ClassUser)
}

// teardownReply is the part of a git.remove / git.reclone reply the settle
// reads: the old fields every daemon sends, and the acknowledgement a daemon
// that knows op ids adds.
type teardownReply struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	checkoutteardown.Ack
}

// settleTeardown records how op ended from the command's reply (or its
// failure) and returns nil when it is done. Every other outcome is the error
// for the caller to return; what said "still running" leaves op pending.
func (s *Service) settleTeardown(ctx context.Context, op *db.CheckoutTeardown, project *db.Project, machine string, reply teardownReply, sendErr error) error {
	what := fmt.Sprintf("the %s of %q on %s", op.Kind, project.Name, machine)
	if sendErr != nil {
		// Only the daemon's own failure is an outcome: a daemon that knows
		// ids reports one only before it journaled the op (nothing ran), an
		// older one after its synchronous attempt. Every other failure —
		// unreachable, a timeout, a dropped connection, the caller going
		// away, an unreadable reply — says nothing about whether the
		// machine is deleting, so the op stays pending and holds
		// creation until the machine says (a retry, or a status query).
		if text, ok := toolexec.DaemonReportedText(sendErr); ok {
			if _, err := s.deps.Repo.SettleCheckoutTeardown(ctx, op.ID, db.CheckoutTeardownFailed, text); err != nil {
				return svcerr.WrapOr(err, svcerr.Internal("failed to record the teardown"))
			}
			return daemonerr.Error(teardownVerb(op.Kind), sendErr)
		}
		switch {
		case daemonerr.Unreachable(sendErr):
			if op.Kind == db.CheckoutTeardownRemove {
				return svcerr.WithReason(svcerr.WithCause(svcerr.WithClass(
					svcerr.Unavailable(removeFromOfflineMachine), svcerr.ClassUser), sendErr), errreason.DaemonNotConnected)
			}
			return daemonerr.Error(teardownVerb(op.Kind), sendErr)
		case toolexec.IsDaemonTimeout(sendErr):
			return svcerr.WithCause(svcerr.WithClass(svcerr.DeadlineExceeded(fmt.Sprintf(
				"%s is still running; workspaces are not created there until it finishes. Try again to see how it ended",
				what)), svcerr.ClassUser), sendErr)
		}
		return svcerr.WithCause(svcerr.WithClass(svcerr.Unavailable(fmt.Sprintf(
			"%s was sent, but its answer was lost; workspaces are not created there until the machine says how it ended. Try again to see",
			what)), svcerr.ClassUser), sendErr)
	}

	var settled *db.CheckoutTeardown
	var err error
	if reply.Acknowledged() {
		settled, err = checkoutteardown.Settle(ctx, s.deps.Repo, op, reply.Ack)
	} else {
		// A daemon from before op ids: its reply is the outcome.
		state := db.CheckoutTeardownDone
		if !reply.Success {
			state = db.CheckoutTeardownFailed
		}
		settled, err = s.deps.Repo.SettleCheckoutTeardown(ctx, op.ID, state, reply.Error)
	}
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("failed to record the teardown"))
	}
	switch settled.State {
	case db.CheckoutTeardownDone:
		return nil
	case db.CheckoutTeardownPending:
		return svcerr.WithClass(svcerr.DeadlineExceeded(fmt.Sprintf(
			"%s is still running; workspaces are not created there until it finishes. Try again to see how it ended", what)), svcerr.ClassUser)
	case db.CheckoutTeardownNotRun:
		return svcerr.WithClass(svcerr.Aborted(fmt.Sprintf(
			"%s did not run: a workspace was being created there at the same time; try again", what)), svcerr.ClassUser)
	}
	reason := strings.TrimSpace(settled.Detail)
	if reason == "" {
		reason = "the machine did not report it finished"
	}
	return svcerr.WithClass(svcerr.FailedPrecondition(fmt.Sprintf("could not %s: %s", teardownVerb(op.Kind), reason)), svcerr.ClassUser)
}

// teardownVerb names what a teardown kind does, for a sentence.
func teardownVerb(kind string) string {
	if kind == db.CheckoutTeardownReclone {
		return "reclone the project on the machine"
	}
	return "remove the project from the machine"
}
