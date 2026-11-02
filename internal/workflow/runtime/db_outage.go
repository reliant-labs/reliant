// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/dbconn"
	"github.com/reliant-labs/reliant/internal/logging"
	"go.temporal.io/sdk/temporal"
)

// ============================================================================
// Riding out a database outage.
// ============================================================================
//
// A database that stops answering for a while — a Cloud SQL maintenance
// restart, an HA failover, a tier change — is routine, and a conversation must
// survive it. On 2026-11-02 a 28-second restart of reliant-pg failed three
// agent runs for good (reliant#2100): the step's retry ladder (5 attempts,
// 1+2+4+8s apart) is sized for a flaky call, not for a server that is gone, and
// PgBouncer's server_login_retry makes every attempt during the outage fail in
// milliseconds, so the whole ladder was spent inside the outage.
//
// Two places wait instead of failing:
//
//   - writeMessageWithRetry: a node's delegated save_message already holds the
//     activity's result (for call_llm, a paid-for LLM turn). Failing the
//     activity would re-run that work, so the write itself waits for the
//     database, for up to dbOutageRideOut.
//   - ActivityWrapper.awaitDatabase: any activity that failed because the
//     database did not answer holds its attempt — heartbeating, so Temporal
//     knows the worker is alive — until a probe through the same pool answers,
//     then fails retryably with a short NextRetryDelay. One outage costs one
//     attempt, however long it lasts, and the retry runs seconds after the
//     database is back rather than at the next rung of the ladder.
//
// Both recognise the outage with dbconn.Unavailable, which is specific to the
// database: an LLM provider's dropped connection is not held here.

// DatabaseUnavailableErrType is the ApplicationError type of an activity that
// failed because the database did not answer. It is always retryable, and
// classifyError passes it through untouched: the message carries whatever the
// layers above wrapped the driver error in, and the text rules must never get
// to call it "not found".
const DatabaseUnavailableErrType = "DatabaseUnavailable"

// dbOutageRideOut is how long one attempt waits for the database to answer
// again. Sized to a Cloud SQL maintenance restart (typically under a minute,
// occasionally several) with room to spare. An outage longer than this fails
// the attempt retryably and the next attempt waits again, so a step's ladder
// covers stepActivityMaxAttempts × dbOutageRideOut before the run stops.
const dbOutageRideOut = 10 * time.Minute

// dbOutageFirstBackoff and dbOutageMaxBackoff bound the delay between probes
// (or write retries) while the database is down: doubling from the first,
// capped at the max, so a short blip resumes within a second and a long outage
// costs one cheap query every few seconds.
const (
	dbOutageFirstBackoff = 500 * time.Millisecond
	dbOutageMaxBackoff   = 5 * time.Second
)

// dbProbeTimeout bounds one availability probe. A probe that times out is a
// database that did not answer.
const dbProbeTimeout = 5 * time.Second

// dbOutageDeadlineMargin is left of the activity's own deadline when holding
// an attempt, so the attempt still fails by itself — with a typed, retryable
// error — instead of being killed by its start-to-close timeout.
const dbOutageDeadlineMargin = 5 * time.Second

// inlineSaveMessageStartToClose is executeSaveMessageInline's per-attempt
// timeout. The SaveMessage activity has no heartbeat, so this deadline is what
// bounds its hold: inlineSaveMessageMaxAttempts × ~2.5 minutes covers
// dbOutageRideOut, where the previous 30 seconds covered two and a half
// minutes in all.
const inlineSaveMessageStartToClose = 2*time.Minute + 30*time.Second

// dbOutageRetryDelay is the NextRetryDelay of an attempt whose hold ended with
// the database answering again: retry now, the wait already happened.
const dbOutageRetryDelay = time.Second

// outageClock is the time source the outage waits run on. Production uses the
// wall clock; tests substitute a virtual one, so a ten-minute outage runs in
// milliseconds against the production policy itself.
type outageClock interface {
	Now() time.Time
	// Sleep waits d, or returns ctx's error if ctx ends first.
	Sleep(ctx context.Context, d time.Duration) error
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

func (wallClock) Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// dbOutagePolicy is how long, and how often, a worker waits for a database
// that stopped answering. Carried per registry and copied into each wrapper,
// like messageWriteRetry, so a test changes its own and nobody else's.
type dbOutagePolicy struct {
	rideOut      time.Duration
	firstBackoff time.Duration
	maxBackoff   time.Duration
	clock        outageClock
}

func defaultDBOutagePolicy() dbOutagePolicy {
	return dbOutagePolicy{
		rideOut:      dbOutageRideOut,
		firstBackoff: dbOutageFirstBackoff,
		maxBackoff:   dbOutageMaxBackoff,
		clock:        wallClock{},
	}
}

// backoff is the delay before the n-th (1-based) retry or probe.
func (p dbOutagePolicy) backoff(n int) time.Duration {
	d := p.firstBackoff
	for i := 1; i < n && d < p.maxBackoff; i++ {
		d *= 2
	}
	return min(d, p.maxBackoff)
}

// databaseUnavailableError is the error an attempt fails with when the
// database did not answer. back reports that the hold ended with the database
// answering again, so the retry should run at once.
func databaseUnavailableError(activityType string, cause error, back bool) error {
	opts := temporal.ApplicationErrorOptions{Cause: cause}
	if back {
		opts.NextRetryDelay = dbOutageRetryDelay
	}
	return temporal.NewApplicationErrorWithOptions(
		fmt.Sprintf("database unavailable while running %s", activityType),
		DatabaseUnavailableErrType, opts)
}

// awaitDatabase holds a failed attempt whose cause is a database that did not
// answer, until a probe through the same pool answers, the ride-out elapses,
// or the attempt's own deadline approaches; then returns the retryable error
// the attempt fails with. The heartbeat loop keeps running throughout, so
// Temporal sees a live worker and the step is not re-dispatched elsewhere.
//
// No probe (no store, no workflow id) means no hold: the attempt fails at once,
// still typed and retryable.
func (w *ActivityWrapper[I, O]) awaitDatabase(ctx context.Context, workflowID, activityType, activityID string, cause error) error {
	if w.repo == nil || workflowID == "" {
		return databaseUnavailableError(activityType, cause, false)
	}
	policy := w.dbOutage

	// The attempt's own deadline bounds the hold through a context, not a
	// computed duration: every sleep AND every probe runs under holdCtx, so
	// neither can carry the attempt past deadline-margin, and the margin is
	// left to return the typed, retryable error before the start-to-close
	// timeout kills the attempt. The ride-out is measured on policy.clock.
	holdCtx := ctx
	if deadline, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		holdCtx, cancel = context.WithDeadline(ctx, deadline.Add(-dbOutageDeadlineMargin))
		defer cancel()
	}

	logging.WarnContext(ctx, "[ActivityWrapper] Database unavailable; holding the attempt until it answers",
		"activityType", activityType,
		"activityID", activityID,
		"workflowID", workflowID,
		"rideOut", policy.rideOut,
		"error", cause)

	start := policy.clock.Now()
	back := false
	for n := 1; holdCtx.Err() == nil && policy.clock.Now().Sub(start) < policy.rideOut; n++ {
		if policy.clock.Sleep(holdCtx, policy.backoff(n)) != nil {
			break
		}
		if w.databaseAnswers(holdCtx, workflowID) {
			back = true
			break
		}
	}
	waited := policy.clock.Now().Sub(start).Round(time.Millisecond)
	if back {
		logging.InfoContext(ctx, "[ActivityWrapper] Database answering again; retrying the attempt",
			"activityType", activityType,
			"activityID", activityID,
			"waited", waited)
	} else {
		logging.WarnContext(ctx, "[ActivityWrapper] Database still unavailable; failing the attempt so it retries",
			"activityType", activityType,
			"activityID", activityID,
			"waited", waited)
	}
	return databaseUnavailableError(activityType, cause, back)
}

// databaseAnswers probes the database through the store the wrapper already
// holds (and so through the same pool and PgBouncer the activity used). Any
// answer counts, a missing row included; only an outage or a probe that
// timed out does not.
func (w *ActivityWrapper[I, O]) databaseAnswers(ctx context.Context, workflowID string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, dbProbeTimeout)
	defer cancel()
	_, err := w.repo.GetWorkflow(probeCtx, workflowID)
	if err == nil {
		return true
	}
	return probeCtx.Err() == nil && !dbconn.Unavailable(err)
}
