package cli

// `forge env up`'s PRESENCE REPORTING (doc §7.4, owner decision O-8).
//
// One row per running stack, so the Live view can show what is running where
// without asking a daemon. A session is an OBSERVATION: never a promotion,
// never a release, never a deploy target, and no input to policy or billing.
//
// IT NEVER BLOCKS `forge env up`, AND THAT IS THE WHOLE DESIGN OF THIS FILE.
// A dev stack that would not start because a presence row could not be
// written would be a hosted dependency inserted into the one workflow that
// has no business having one — and the failure would not even look like a
// network failure, it would look like "forge is broken". So every call here
// runs under its own timeout, in its own goroutine where a caller would
// otherwise wait, and every error is logged at most once per failure mode
// and then dropped. There is NO retry: a retry is a delay, and the server
// GCs an unreported session after 24 h anyway.
//
// WHAT A CRASH LOOKS LIKE: nothing. A stack that dies without reporting
// stopped simply goes quiet — the row stops being refreshed, a reader greys
// it out after release.SessionStaleAfter, and the server reaps it. There is
// no crash detection here and there must not be: forge cannot tell a crashed
// stack from a laptop that closed its lid, and a heartbeat gap is the honest
// rendering of both.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/devstack"
	"github.com/reliant-labs/forge/pkg/release"
)

// sessionReportTimeout bounds ONE report.
//
// Short on purpose. This is the only thing standing between an unreachable
// control plane and the startup path, and the report carries no information
// the stack needs — so a budget that would be generous for a deploy is a
// budget that holds a dev loop hostage here. Three missed heartbeats before
// a reader greys the session out (sessionHeartbeat vs
// release.SessionStaleAfter) means dropping one costs nothing visible.
const sessionReportTimeout = 5 * time.Second

// upSession is one stack's presence, reported for as long as the stack runs.
//
// A concrete type with methods rather than an interface: there is one
// implementation and no substitution point. The seam that IS substituted is
// sessionReporter, injected at construction, which is what lets the tests
// state an erroring or blocking backend.
type upSession struct {
	reporter sessionReporter
	session  release.LocalSession
	// log is where the one-off failure notes go. Never nil after
	// newUpSession; a test reads it to assert what was said.
	log io.Writer
	// hosted shapes the failure note only.
	hosted bool

	// heartbeat is how often a supervising run re-reports. A field rather
	// than the constant inline so a test can drive many beats without
	// waiting a minute for the second one.
	heartbeat time.Duration

	// mu guards the mutable report state below. A heartbeat and a stop can
	// race: Ctrl-C arrives while a beat is mid-flight, and both then write
	// LastSeenAt and read loggedFailures.
	mu sync.Mutex
	// loggedFailures is the failure modes already reported, so an
	// unreachable control plane says so ONCE rather than once a minute for
	// the length of a working day.
	loggedFailures map[string]bool
	// stopped guards against a second stop report: the Ctrl-C cascade and
	// procRegistry.shutdown can both reach teardown, and a stack that
	// reported stopped twice would be harmless but the second report would
	// also spend five seconds of a user's teardown for nothing.
	stopped bool
}

// newUpSession mints the session for this stack, or returns nil when there is
// nothing to report.
//
// NIL IS A FIRST-CLASS RETURN, not an error. Every method tolerates a nil
// receiver, so the caller in up.go never branches: a non-local env, a project
// that will not render, a ledger that cannot be opened, and an unreadable
// provenance all collapse to "no presence reporting", which is a strictly
// cosmetic loss. Making any of them an error at the call site is how a
// presence row ends up able to fail a dev stack.
//
// The ID IS DERIVED, not random: sha256 over the identity the stores key on
// (env, host, worktree). So a stack that is stopped and started again
// REPLACES its row rather than leaving a stopped twin beside a running one,
// on the hosted side too — where the upsert is keyed on the id the client
// sends.
func newUpSession(ctx context.Context, projectDir, env string, log io.Writer, now time.Time) *upSession {
	target, err := sessionTargetFor(ctx, projectDir, env)
	switch {
	case err != nil:
		fmt.Fprintf(log, "[up] note: not reporting this stack's presence: %v\n", err)
		return nil
	case !target.Report:
		// Said plainly rather than silently: a user looking for their
		// staging stack in Live needs to know forge chose not to
		// report it, not wonder whether the report failed.
		fmt.Fprintf(log, "[up] note: %s reports no local session — %s\n", env, target.Skip)
		return nil
	}

	prov := captureBuildProvenance(ctx, projectDir)
	// The worktree key is forge's EXISTING devstack key — the one this
	// checkout's port block is held under — so a session matches the stack
	// that actually holds those ports. CaptureProvenance was given the
	// same key, and reading it back from the capture rather than calling
	// devstack.Worktree a second time is what keeps the two from
	// disagreeing when only one of them is passed a projectDir.
	worktree := prov.Worktree
	if worktree.Host == "" {
		// Validate requires a host, and a session with no host could
		// not be keyed. Fall back rather than drop the session: an
		// empty os.Hostname is not a reason to go invisible.
		worktree.Host = release.HostID()
	}
	sess := release.LocalSession{
		ID:         sessionIDFor(env, worktree.Host, worktree.Key),
		Env:        env,
		Worktree:   worktree,
		Provenance: prov,
		StartedAt:  now,
		LastSeenAt: now,
	}
	if err := sess.Validate(); err != nil {
		fmt.Fprintf(log, "[up] note: not reporting this stack's presence: %v\n", err)
		return nil
	}
	return &upSession{
		reporter:       target.Reporter,
		session:        sess,
		log:            log,
		hosted:         target.Hosted,
		heartbeat:      sessionHeartbeat,
		loggedFailures: map[string]bool{},
	}
}

// sessionIDFor derives the stable id. Hashed rather than concatenated because
// a worktree label is a branch name and a host is a hash — neither is a safe
// thing to paste into an identifier a server stores.
func sessionIDFor(env, host, worktreeKey string) string {
	sum := sha256.Sum256([]byte(env + "\x00" + host + "\x00" + worktreeKey))
	return "sess-" + hex.EncodeToString(sum[:])[:24]
}

// report sends one presence row. Best-effort: the error is logged once per
// failure mode and dropped.
//
// It takes its OWN context derived from Background rather than the caller's,
// and that is deliberate for the stop report. Teardown runs while the
// caller's context is already cancelled (Ctrl-C cancelled it; that is how the
// cascade started), so a stop report on the caller's context would be
// cancelled before it was sent and the session would be left looking like a
// crash. The timeout bounds it instead.
func (s *upSession) report(now time.Time, stopped bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if stopped {
		if s.stopped {
			s.mu.Unlock()
			return
		}
		s.stopped = true
		at := now
		s.session.StoppedAt = &at
	}
	s.session.LastSeenAt = now
	sess := s.session
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), sessionReportTimeout)
	defer cancel()
	if err := s.reporter.ReportSession(ctx, sess); err != nil {
		s.noteFailure(err)
	}
}

// noteFailure logs a failure mode at most once.
//
// Keyed on the error's TEXT rather than on a classification, because the
// failure modes §7.4 names — no control plane, network down, token missing —
// are not distinguishable by type here and the only thing being prevented is
// repetition. A changed message is a changed situation and is worth a second
// line.
func (s *upSession) noteFailure(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := err.Error()
	if s.loggedFailures[key] {
		return
	}
	s.loggedFailures[key] = true
	where := "this machine's ledger"
	if s.hosted {
		where = "the control plane"
	}
	// The reassurance is part of the message, not politeness. Without it
	// the line reads as a startup failure in a stack that in fact came up
	// fine, and the next thing the reader does is restart it.
	fmt.Fprintf(s.log, "[up] note: could not report this stack's presence to %s: %v\n"+
		"     (presence is cosmetic — the stack is unaffected, and the record is discarded after 24h if nothing refreshes it)\n",
		where, err)
}

// heartbeatUntil re-reports while the stack supervises, and returns when ctx
// is done.
//
// Run as a goroutine, and it is the ONLY goroutine this file starts. It exits
// on ctx.Done() and nothing else — no internal timer that could outlive the
// stack, no channel a caller has to remember to close — so "the heartbeat
// stops when the stack stops" is a property of the one cancel the caller
// already has rather than a cleanup step that can be forgotten.
func (s *upSession) heartbeatUntil(ctx context.Context) {
	if s == nil {
		return
	}
	ticker := time.NewTicker(s.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.report(time.Now().UTC(), false)
		}
	}
}

// start reports the stack as running, WITHOUT waiting for the report.
//
// The goroutine is what makes §7.4's rule structural instead of a promise. A
// synchronous call here would hold the summary box — the thing the user is
// waiting for — behind a hosted write, and a backend that blocks rather than
// failing (a TCP connect to a black-holed address) would hold it for the
// whole timeout. Nothing downstream reads the result, so there is nothing to
// wait for.
func (s *upSession) start(now time.Time) {
	if s == nil {
		return
	}
	go s.report(now, false)
}

// stop reports teardown, SYNCHRONOUSLY, and that asymmetry with start is
// deliberate.
//
// A detached goroutine at teardown would be a goroutine racing process exit:
// the report would usually not be sent, and the session would read as a crash
// after every clean Ctrl-C — which is precisely the distinction StoppedAt
// exists to draw. Bounded by sessionReportTimeout, so the worst case is five
// seconds added to a teardown that is already signalling process trees and
// waiting up to ten for them.
func (s *upSession) stop(now time.Time) {
	if s == nil {
		return
	}
	s.report(now, true)
}

// reportStoppedSession is the teardown report for a stack this process did
// not start — `forge env down`, and the detached (`--background`) stack it
// stops.
//
// A separate entry point rather than a reconstructed upSession because the
// facts differ: there is no live session object here, and the StartedAt of
// the stack being stopped is not knowable from this process. The store keys
// on (env, host, worktree) and the id derives from the same three, so the row
// this writes is the row `forge env up` wrote — StartedAt is restated as now
// only because Validate requires a window, and a stopped row's start is not
// a field any reader uses.
//
// Best-effort, like everything else here: `forge env down` stops the stack
// whether or not the presence row can be updated.
func reportStoppedSession(ctx context.Context, projectDir, env string, log io.Writer, now time.Time) {
	target, err := sessionTargetFor(ctx, projectDir, env)
	if err != nil || !target.Report {
		return
	}
	prov := captureBuildProvenance(ctx, projectDir)
	worktree := prov.Worktree
	if worktree.Host == "" {
		worktree.Host = release.HostID()
	}
	if worktree.Key == "" {
		worktree.Key = devstack.Worktree(projectDir)
	}
	stoppedAt := now
	sess := release.LocalSession{
		ID:         sessionIDFor(env, worktree.Host, worktree.Key),
		Env:        env,
		Worktree:   worktree,
		Provenance: prov,
		StartedAt:  now,
		LastSeenAt: now,
		StoppedAt:  &stoppedAt,
	}
	if err := sess.Validate(); err != nil {
		return
	}
	reportCtx, cancel := context.WithTimeout(context.Background(), sessionReportTimeout)
	defer cancel()
	if err := target.Reporter.ReportSession(reportCtx, sess); err != nil {
		fmt.Fprintf(log, "[down] note: could not mark this stack's presence record stopped: %v (cosmetic; the stack is stopped)\n", err)
	}
}
