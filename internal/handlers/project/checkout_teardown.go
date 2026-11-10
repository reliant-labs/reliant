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
// to send. A pending op of the same kind is reused (a retry). One of another
// kind is asked how it ended first, and refused while it has not.
func (s *Service) beginTeardown(ctx context.Context, userID string, project *db.Project, pd *core.ProjectDaemon, daemonID, kind, machine string) (*db.CheckoutTeardown, error) {
	before := "removing the project from that machine"
	if kind == db.CheckoutTeardownReclone {
		before = "recloning the project there"
	}
	guard := func(txCtx context.Context) error {
		return s.requireNoWorkspacesOn(txCtx, project, daemonID, machine, before)
	}
	for attempt := 0; ; attempt++ {
		op, err := s.deps.Repo.BeginCheckoutTeardown(ctx, project.ID, daemonID, kind, pd.Path, guard)
		var busy *db.CheckoutTeardownBusyError
		if !errors.As(err, &busy) {
			if err != nil {
				// The guard's refusal keeps its code and sentence.
				return nil, svcerr.WrapOr(err, svcerr.Internal("failed to record the teardown"))
			}
			return op, nil
		}
		settled, qerr := checkoutteardown.Query(ctx, s.deps.Repo, teardownMachine{s: s, userID: userID, daemonID: daemonID}, busy.Teardown, time.Now())
		if attempt > 0 || qerr != nil || settled.State == db.CheckoutTeardownPending {
			return nil, svcerr.WithClass(svcerr.FailedPrecondition(fmt.Sprintf(
				"a %s of %q on %s has not finished yet; try again once it has", busy.Teardown.Kind, project.Name, machine)), svcerr.ClassUser)
		}
	}
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
		switch {
		case daemonerr.Unreachable(sendErr):
			// Nothing ran. The op stays pending: the machine is offline,
			// so no workspace is created there anyway, and the first
			// question asked once it is back fences it.
			if op.Kind == db.CheckoutTeardownRemove {
				return svcerr.WithReason(svcerr.WithCause(svcerr.WithClass(
					svcerr.Unavailable(removeFromOfflineMachine), svcerr.ClassUser), sendErr), errreason.DaemonNotConnected)
			}
			return daemonerr.Error(teardownVerb(op.Kind), sendErr)
		case toolexec.IsDaemonTimeout(sendErr):
			// The machine may still be at it: pending, and said so.
			return svcerr.WithCause(svcerr.WithClass(svcerr.DeadlineExceeded(fmt.Sprintf(
				"%s is still running; workspaces are not created there until it finishes. Try again to see how it ended",
				what)), svcerr.ClassUser), sendErr)
		}
		// The machine answered with an error. A daemon that knows ids does
		// that only before it journals the op (nothing was removed); an older
		// one, after its synchronous attempt. Either way it is over.
		if _, err := s.deps.Repo.SettleCheckoutTeardown(ctx, op.ID, db.CheckoutTeardownFailed, sendErr.Error()); err != nil {
			return svcerr.WrapOr(err, svcerr.Internal("failed to record the teardown"))
		}
		return daemonerr.Error(teardownVerb(op.Kind), sendErr)
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
