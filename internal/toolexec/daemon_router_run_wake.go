// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/nomachine"
)

// ── Waking a machine for a RUN that resumes ────────────────────────────
//
// A run held off-machine (a provider usage limit, a timer, a sub-agent wait,
// a self-pause, a continue-as-new) sends the machine no traffic, so the idle
// policy suspends it — correctly: nobody should pay for a machine that does
// nothing for hours. When the run resumes, its next tool call would find the
// machine asleep, and tool-time traffic never wakes one (#406). So the
// workflow wakes it first, at the resume point, through WakeForRun
// (docs/research/machine-suspend-2026-10-20.md, in control-plane).
//
// The credential is what makes this a separate door from EnsureAwake. A run
// can resume hours after it started; the user's session token it captured
// then may have expired, and an attended run has no delegated token. The run
// was authorized to use this machine when it started, so the wake is made on
// the run owner's behalf by the worker itself (control-plane's
// DaemonInternalService/ResumeDaemonForRun, internal-service identity), which
// still runs every gate the owner's own resume runs.
//
// Tool-time code still cannot wake anything: it holds DaemonRouter, which has
// neither EnsureAwake nor WakeForRun.

// RunResumer wakes a run owner's suspended machine on the run's behalf.
// Declared here, at the consumer; controlplane.Service implements it.
//
// resumed reports whether this call started the wake (false: already up or on
// its way). An error wrapping ErrRunResumeUnsupported means the control plane
// predates the door, and the caller may fall back to the owner's token.
type RunResumer interface {
	ResumeDaemonForRun(ctx context.Context, userID, daemonID string) (resumed bool, err error)
}

// ErrRunResumeUnsupported: the control plane has no run-resume door (an older
// release). WakeForRun then wakes with the owner's token, as EnsureAwake does.
var ErrRunResumeUnsupported = errors.New("control plane does not support waking a machine for a resuming run")

// WithRunResumer lets WakeForRun wake a run owner's machine with the worker's
// own internal-service identity. Only the worker wires it.
func WithRunResumer(rr RunResumer) NATSRouterOption {
	return func(r *NATSDaemonRouter) { r.runResumer = rr }
}

// RunWaker is the wake a resuming run's workflow makes before its next machine
// step. Declared for consumers (the workflow's wake activity, preflight);
// *NATSDaemonRouter implements it. Tool-time code never holds it.
type RunWaker interface {
	WakeForRun(ctx context.Context, userID string, selector *DaemonSelector) (WakeResult, error)
}

var _ RunWaker = (*NATSDaemonRouter)(nil)

// runWakeCooldown is how long after one run's wake of a machine another
// run's wake of the SAME machine is answered from memory instead of asking
// the control plane again. Sub-agents that all resume when one usage limit
// resets ask within seconds of each other; one ResumeDaemonForRun covers
// them. Longer than any wake takes to show up in the registry's mirror, short
// enough that a machine suspended again later is woken again.
const runWakeCooldown = 2 * time.Minute

// runWakeGate collapses concurrent and back-to-back run wakes of one daemon
// in this process into one control-plane call. Across processes the control
// plane's own idempotency (an already-pending machine is not woken twice)
// finishes the job.
type runWakeGate struct {
	mu    sync.Mutex
	calls map[string]*runWakeCall
}

type runWakeCall struct {
	done chan struct{}
	res  WakeResult
	err  error
	at   time.Time
}

// do runs wake for daemonID unless another caller is running it right now
// (then it waits for that one's answer) or one finished successfully within
// runWakeCooldown (then it reports that daemon already on its way).
func (g *runWakeGate) do(ctx context.Context, daemonID string, now func() time.Time, wake func() (WakeResult, error)) (WakeResult, error) {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = map[string]*runWakeCall{}
	}
	if c, ok := g.calls[daemonID]; ok {
		select {
		case <-c.done:
			if c.err == nil && now().Sub(c.at) < runWakeCooldown {
				g.mu.Unlock()
				return WakeResult{DaemonID: daemonID}, nil
			}
		default:
			g.mu.Unlock()
			select {
			case <-c.done:
				return WakeResult{DaemonID: daemonID}, c.err
			case <-ctx.Done():
				return WakeResult{}, ctx.Err()
			}
		}
	}
	c := &runWakeCall{done: make(chan struct{})}
	g.calls[daemonID] = c
	g.mu.Unlock()

	c.res, c.err = wake()
	c.at = now()
	close(c.done)
	return c.res, c.err
}

// WakeForRun returns the id of the daemon for the selector, waking it when it
// is asleep, for a run that resumes after a hold. The daemon is chosen exactly
// as Wake chooses it; only the credential differs (see above). Success means
// the wake is under way, not that the daemon is attached yet.
func (r *NATSDaemonRouter) WakeForRun(ctx context.Context, userID string, selector *DaemonSelector) (WakeResult, error) {
	if nomachine.Is(ctx) {
		return WakeResult{}, nomachine.ErrNoMachine
	}
	selector = r.defaultSelector(ctx, userID, selector)
	if r.resolver != nil {
		if daemons, err := r.resolver.ResolveDaemons(ctx, userID, selector); err == nil && len(daemons) > 0 {
			return WakeResult{DaemonID: daemons[0].DaemonID}, nil
		}
	}
	rec, found, sawDaemonRecord, err := r.lookupDaemonRecord(ctx, userID, selector)
	if err != nil {
		return WakeResult{}, err
	}
	if !found || (r.runResumer == nil && r.resumer == nil) || !rec.wakeable() {
		daemonID, err := routableDaemonID(userID, selector, rec, found, sawDaemonRecord)
		return WakeResult{DaemonID: daemonID}, err
	}

	return r.runWakes.do(ctx, rec.id, time.Now, func() (WakeResult, error) {
		resumed, err := r.resumeForRun(ctx, userID, selector, rec.id)
		if err != nil && rec.state != recordSuspended {
			// Not known to be suspended: the resume only hedged against a
			// lagging mirror (see wakeable). Route as resolution would.
			logging.InfoContext(ctx, "[DaemonRouter] speculative run-resume wake of an unattached managed daemon failed; routing to it as-is",
				"user_id", userID, "daemon_id", rec.id, "error", err)
			daemonID, err := routableDaemonID(userID, selector, rec, found, sawDaemonRecord)
			return WakeResult{DaemonID: daemonID}, err
		}
		if err != nil {
			return WakeResult{}, err
		}
		if resumed {
			logging.InfoContext(ctx, "[DaemonRouter] woke the machine for a resuming run", "user_id", userID, "daemon_id", rec.id)
		}
		return WakeResult{DaemonID: rec.id, Resumed: resumed}, nil
	})
}

// resumeForRun asks the control plane to wake daemonID for a resuming run:
// through the run-resume door when this process has one, else — no door
// configured, or a control plane that predates it — with the owner's token,
// exactly as EnsureAwake would.
func (r *NATSDaemonRouter) resumeForRun(ctx context.Context, userID string, selector *DaemonSelector, daemonID string) (bool, error) {
	if r.runResumer != nil {
		resumed, err := r.runResumer.ResumeDaemonForRun(ctx, userID, daemonID)
		if !errors.Is(err, ErrRunResumeUnsupported) {
			return resumed, err
		}
		logging.InfoContext(ctx, "[DaemonRouter] control plane has no run-resume door; waking with the owner's token",
			"user_id", userID, "daemon_id", daemonID)
	}
	if r.resumer == nil {
		return false, ErrRunResumeUnsupported
	}
	return r.resume(ctx, userID, selector, daemonID)
}
