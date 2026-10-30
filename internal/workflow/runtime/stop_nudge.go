// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"time"
)

// stopNudgeSource tells a running activity that a stop was just requested for
// its workflow (pause, interrupt, cancel, terminate, reset). Declared here, at
// its one consumer; internal/temporal/stopnudge.Hub is the production one,
// fed over NATS by the client that made the request.
//
// Why activities need it: Temporal hands a cancellation to an activity only in
// a heartbeat response, and the SDK heartbeats once per throttle window
// (maxHeartbeatThrottleInterval, internal/workersetup). That window is sized
// for liveness, not for a user waiting on the stop button, so a nudge makes
// the activity ask the server straight away instead. See stopnudge's package
// doc for the load this removed.
type stopNudgeSource interface {
	Watch(workflowID string) (nudged <-chan struct{}, unwatch func())
}

// SetStopNudges installs the source every wrapped activity watches for stop
// nudges. nil (the default) leaves cancellation to the regular heartbeat.
func (r *ActivityRegistry) SetStopNudges(src stopNudgeSource) {
	r.stopNudges = src
}

// stopNudgeProbes are the offsets, from a nudge, at which a nudged activity
// sends a direct heartbeat and acts on the verdict.
//
// A nudge arrives as soon as the stop request is accepted (the signal is in
// history), but the cancel the activity is waiting for is recorded only when
// the workflow has processed that signal and completed its workflow task —
// usually a few hundred milliseconds later, seconds when Temporal is loaded.
// So one probe at once would nearly always be early. Four probes spread over
// five seconds catch the common case within ~250ms-1s and a loaded server
// within 5s, for four extra heartbeat writes per running activity per stop;
// anything later is caught by the regular heartbeat. A nudge arriving
// mid-burst restarts it.
var stopNudgeProbes = []time.Duration{
	250 * time.Millisecond,
	1 * time.Second,
	2500 * time.Millisecond,
	5 * time.Second,
}

// followStopNudges runs until ctx ends. On each nudge it probes the server at
// the given offsets from the nudge (stopNudgeProbes in production), and the first probe whose verdict is a real
// stop instruction (realStopInstruction: cancel, pause, reset, attempt gone)
// calls stop with that verdict and returns. Any other probe error is a slow
// or unreachable server: ignored, the regular heartbeat stays in charge.
func followStopNudges(ctx context.Context, nudged <-chan struct{}, probes []time.Duration, probe func(context.Context) error, stop func(cause error)) {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	var (
		timerC     <-chan time.Time
		burstStart time.Time
		next       int
	)
	arm := func() {
		if next >= len(probes) {
			timerC = nil
			return
		}
		timer.Reset(max(time.Until(burstStart.Add(probes[next])), 0))
		timerC = timer.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-nudged:
			burstStart, next = time.Now(), 0
			arm()
		case <-timerC:
			next++
			rpcCtx, cancel := context.WithTimeout(ctx, shieldedHeartbeatRPCTimeout)
			err := probe(rpcCtx)
			cancel()
			if err != nil && realStopInstruction(err) {
				stop(err)
				return
			}
			arm()
		}
	}
}
