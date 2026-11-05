// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"
	"errors"
	"sync"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
	"github.com/reliant-labs/reliant/sdk/go/reliant/follow"
)

// FollowOptions tunes [RunsClient.Follow]. The zero value follows from the
// start of the run, polling every two seconds, until it ends.
type FollowOptions struct {
	// Interval is the poll interval; two seconds when zero.
	Interval time.Duration
	// Timeout bounds the follow; zero means until the run ends or ctx is done.
	// A follow that times out is not a failure of the run: Result.TimedOut.
	Timeout time.Duration
	// Tail skips history: only events after this call are delivered. The run's
	// open gates and its verdict are still reported.
	Tail bool
	// StopOnGate ends the follow as soon as a gate (question or approval) is
	// open, so a supervisor can block until the next one that needs input.
	// Result.OpenGates holds what it stopped on.
	StopOnGate bool
	// Buffer is how many events may queue before the follow waits for the
	// consumer; 64 when zero. A consumer that stops reading stalls the follow,
	// it does not lose events.
	Buffer int
}

// FollowResult is how a followed run ended.
type FollowResult struct {
	// Status is the run's LIFECYCLE: "completed", "failed" or "cancelled". Empty
	// when the follow ended before the run did (timeout, gate, cancellation).
	Status string
	// Outcome is the run's own VERDICT, "success" or "failure", declared by the
	// terminal node it reached; empty when the workflow declared none.
	Outcome string
	// Succeeded is true only when the run finished cleanly AND did not declare
	// itself a failure. A run that routed to its failure node has Status
	// "completed" and Succeeded false; reading Status alone is the false green.
	Succeeded bool
	// TimedOut is true when FollowOptions.Timeout elapsed first.
	TimedOut bool
	// StoppedOnGate is true when the follow ended on an open gate
	// (FollowOptions.StopOnGate). OpenGates are the events of the gates open.
	StoppedOnGate bool
	OpenGates     []Event
	// ExitCode is the code `reliant workflow follow` exits with: 0 succeeded,
	// 1 failed, cancelled or errored, 2 timed out, 3 stopped on a gate.
	ExitCode int
}

// Follow follows a run: events as they happen, then a verdict.
//
// The events are the run's control flow, not its transcript: node and workflow
// boundaries and gates. A node whose condition was false is node_skipped
// (neither passed nor failed); a run step that exited non-zero is node_failed;
// a gate is reported once, whether it opened while you were attached or before.
//
// Wait blocks until the run ends and returns the verdict; Events streams the
// boundaries and must be drained (or Wait called) so the follow can make
// progress. Calling Wait alone is fine: it drains for you.
func (r *RunsClient) Follow(ctx context.Context, runID string, opts FollowOptions) (*Follow, error) {
	sessionID, err := r.sessionOf(ctx, "runs.follow", runID)
	if err != nil {
		return nil, err
	}
	scope := newRunScope(runID)
	if roots, err := r.sessionTree(ctx, sessionID, reliantv1.WorkflowExecutionView_WORKFLOW_EXECUTION_VIEW_BASIC); err == nil {
		scope.load(roots) // best effort: every poll reads the tree again
	}
	return r.followSession(ctx, runID, sessionID, scope, opts)
}

// FollowSession is [RunsClient.Follow] for a run you hold the session id of:
// the chat_id on a [TriggerEvent], or the id `reliant workflow run` prints as
// the execution id. It follows the session's current run: the newest when it
// attaches, or the first to start when the session has none yet. An earlier
// run's rows are not that run's, so its verdict never ends this follow.
func (r *RunsClient) FollowSession(ctx context.Context, sessionID string, opts FollowOptions) (*Follow, error) {
	if sessionID == "" {
		return nil, r.c.fail("runs.followSession", ErrInvalidArgument, "session id is required")
	}
	scope := newSessionScope()
	if roots, err := r.sessionTree(ctx, sessionID, reliantv1.WorkflowExecutionView_WORKFLOW_EXECUTION_VIEW_BASIC); err == nil {
		scope.load(roots) // pins the current run; else the first poll that sees one does
	}
	return r.followSession(ctx, sessionID, sessionID, scope, opts)
}

// followSession follows the one run of the session that scope selects.
func (r *RunsClient) followSession(ctx context.Context, executionID, sessionID string, scope *runScope, opts FollowOptions) (*Follow, error) {
	buf := opts.Buffer
	if buf <= 0 {
		buf = 64
	}
	f := &Follow{events: make(chan Event, buf), done: make(chan struct{})}

	eng := &follow.Engine{
		Source:      &chatSource{chat: r.chat, runs: r, gates: r.c.Gates, sessionID: sessionID, scope: scope},
		ExecutionID: executionID,
		Interval:    opts.Interval,
		Timeout:     opts.Timeout,
		Tail:        opts.Tail,
		ExitOnGate:  opts.StopOnGate,
	}
	ctx, cancel := context.WithCancel(ctx)
	f.cancel = cancel
	// Delivery waits on the consumer, or on Close/the caller's context, but NOT
	// on the engine's own deadline: the engine's context is the one that expires
	// when FollowOptions.Timeout does, and the follow_timeout event it emits as
	// it does so is the last thing a consumer must see.
	eng.OnEvent = func(_ context.Context, ev follow.Event, _ []byte) {
		select {
		case f.events <- ev:
		case <-ctx.Done():
		}
	}

	go func() {
		defer close(f.done)
		defer close(f.events)
		code, err := eng.Run(ctx)
		res := FollowResult{
			Status:        eng.TerminalStatus(),
			Outcome:       eng.TerminalOutcome(),
			TimedOut:      code == follow.ExitTimeout,
			StoppedOnGate: code == follow.ExitGate,
			OpenGates:     eng.OpenGates(),
			ExitCode:      code,
		}
		res.Succeeded = res.Status != "" && follow.Succeeded(res.Status, res.Outcome)
		f.result, f.err = res, err
	}()
	return f, nil
}

// Follow is a followed run. See [RunsClient.Follow].
type Follow struct {
	events chan Event
	done   chan struct{}
	cancel context.CancelFunc

	once   sync.Once
	result FollowResult
	err    error
}

// Events is the stream of boundaries, closed when the follow ends.
func (f *Follow) Events() <-chan Event { return f.events }

// Wait drains any events not yet read and returns how the run ended. A
// follow that gave up (the server unreachable for too long, the context
// cancelled) returns the error alongside whatever it had learned.
func (f *Follow) Wait() (FollowResult, error) {
	for range f.events {
	}
	<-f.done
	return f.result, f.err
}

// Close stops the follow early and releases its goroutine. It is safe to call
// more than once, and after Wait.
func (f *Follow) Close() {
	f.once.Do(f.cancel)
	<-f.done
}

// ErrFollowStopped is returned by Wait when the follow was closed before the run
// ended.
var ErrFollowStopped = errors.New("reliant: follow stopped before the run ended")
