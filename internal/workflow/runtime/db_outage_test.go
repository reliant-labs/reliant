// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	rtemporal "github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// ============================================================================
// reliant#2100: a database outage must never permanently fail a run.
//
// On 2026-11-02 reliant-pg restarted for 28 seconds. PgBouncer answered every
// client with its cached login failure, threads.SaveMessage labelled that
// "thread … does not exist", classifyError made "does not exist" terminal, and
// three agent runs failed for good.
//
// These tests run the PRODUCTION outage policy (dbOutageRideOut, its backoff)
// on a virtual clock, so a 30-second — or nine-minute — outage takes
// milliseconds, and the step's real retry ladder (stepActivityRetryPolicy) on
// Temporal's test environment.
// ============================================================================

// virtualClock is an outageClock whose time moves only when something sleeps.
type virtualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newVirtualClock() *virtualClock {
	return &virtualClock{now: time.Date(2026, 11, 2, 16, 59, 48, 0, time.UTC)}
}

func (c *virtualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *virtualClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	return nil
}

// incidentError is the error the workers logged on 2026-11-02, built the way
// the pre-fix threads.SaveMessage built it: PgBouncer's real refusal under the
// misleading "does not exist" label. Using the mislabelled form on purpose —
// the runtime must ride out an outage whatever a layer above called it.
func incidentError(thread string) error {
	return fmt.Errorf("thread %s does not exist - must be created before saving messages: failed to get latest context window: %w",
		thread, &pgconn.PgError{
			Severity: "FATAL",
			Code:     "08P01",
			Message:  "server login has been failing, cached error: server conn crashed? (server_login_retry)",
		})
}

// outageDatabase stands in for reliant-pg behind PgBouncer: messages keyed by
// idempotency key (threads.SaveMessage's dedup), and a window of virtual time
// in which every operation fails with the incident's error.
type outageDatabase struct {
	clock *virtualClock

	mu        sync.Mutex
	downUntil time.Time
	// commitThenDrop makes the next write commit and then lose its reply —
	// the connection dies between COMMIT and the client reading it — and
	// starts an outage. Its retry must converge on the committed row.
	commitThenDrop time.Duration
	rows           map[string]writtenMessage
	writeCalls     int
}

func newOutageDatabase(clock *virtualClock) *outageDatabase {
	return &outageDatabase{clock: clock, rows: map[string]writtenMessage{}}
}

// goDown starts an outage of d from now (virtual time).
func (d *outageDatabase) goDown(dur time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.downUntil = d.clock.Now().Add(dur)
}

func (d *outageDatabase) unavailable(thread string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.unavailableLocked(thread)
}

func (d *outageDatabase) unavailableLocked(thread string) error {
	if d.clock.Now().Before(d.downUntil) {
		return incidentError(thread)
	}
	return nil
}

func (d *outageDatabase) WriteMessage(
	_ context.Context,
	rtx types.RuntimeContext,
	args *reliantv1.SaveMessageNodeArgs,
	idempotencyKey string,
	attempt int32,
) (*reliantv1.SaveMessageOutput, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.writeCalls++
	if err := d.unavailableLocked(rtx.Thread); err != nil {
		return nil, err
	}
	// Upsert on the idempotency key, as threads.SaveMessage does: a retry of
	// the same write converges on its own row.
	d.rows[idempotencyKey] = writtenMessage{Runtime: rtx, Args: args, IdempotencyKey: idempotencyKey, Attempt: attempt}
	if d.commitThenDrop > 0 {
		d.downUntil = d.clock.Now().Add(d.commitThenDrop)
		d.commitThenDrop = 0
		return nil, incidentError(rtx.Thread)
	}
	return &reliantv1.SaveMessageOutput{MessageId: "msg-" + idempotencyKey, Thread: rtx.Thread}, nil
}

func (d *outageDatabase) messages() []writtenMessage {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]writtenMessage, 0, len(d.rows))
	for _, m := range d.rows {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Runtime.LoopIteration < out[j].Runtime.LoopIteration })
	return out
}

// outageStore is the wrapper's ActivityStore over the same database: its
// GetWorkflow is what the wrapper probes availability with.
type outageStore struct {
	*wrapperTestRepo
	db *outageDatabase
}

func (s outageStore) GetWorkflow(context.Context, string) (*db.Workflow, error) {
	if err := s.db.unavailable("probe"); err != nil {
		return nil, err
	}
	return nil, nil
}

// withOutageClock runs a registry's outage waits on clock, with the
// production policy otherwise.
func withOutageClock(clock outageClock) func(*ActivityRegistry) {
	return func(r *ActivityRegistry) {
		r.dbOutage.clock = clock
	}
}

// outageTurnInput is one agent turn's call_llm dispatch.
func outageTurnInput(turn int) types.ActivityInput {
	in := callLLMInput(saveRequest(agentAssistantSave(), nil))
	in.Runtime.LoopIteration = turn
	in.Runtime.AssistantMessageID = fmt.Sprintf("msg-turn-%d", turn)
	return in
}

// outageAgentRun is an agent loop reduced to what this test is about: one
// call_llm per turn, dispatched on the step's real activity options.
func outageAgentRun(ctx workflow.Context, turns int) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * 24 * time.Hour,
		HeartbeatTimeout:    activityHeartbeatTimeout,
		RetryPolicy:         stepActivityRetryPolicy(),
	})
	for turn := 1; turn <= turns; turn++ {
		var out map[string]interface{}
		if err := workflow.ExecuteActivity(ctx, "CallLLM", outageTurnInput(turn)).Get(ctx, &out); err != nil {
			return fmt.Errorf("turn %d: %w", turn, err)
		}
	}
	return nil
}

// TestDatabaseOutage_RunRidesOutAndCompletes is reliant#2100 end to end: an
// agent run whose database goes away for 30 seconds — once while a turn is
// building its prompt, once after the model answered and the reply is being
// saved — resumes and completes, with every turn's message written exactly
// once and no paid-for LLM turn re-run.
func TestDatabaseOutage_RunRidesOutAndCompletes(t *testing.T) {
	t.Parallel()
	const outage = 30 * time.Second
	clock := newVirtualClock()
	database := newOutageDatabase(clock)

	var mu sync.Mutex
	llmCalls := map[int]int{}
	outageStarted := map[int]bool{}

	callLLM := func(_ context.Context, in types.ActivityInput) (*reliantv1.CallLLMOutput, error) {
		turn := in.Runtime.LoopIteration
		mu.Lock()
		first := !outageStarted[turn]
		outageStarted[turn] = true
		mu.Unlock()

		// Turn 2: the restart begins as the turn starts, so reading the
		// thread to build the prompt fails.
		if turn == 2 && first {
			database.goDown(outage)
		}
		if err := database.unavailable(in.Runtime.Thread); err != nil {
			return nil, fmt.Errorf("failed to load latest context window for thread %s: %w", in.Runtime.Thread, err)
		}

		mu.Lock()
		llmCalls[turn]++
		mu.Unlock()

		// Turn 3: the restart begins after the model answered — the save
		// commits, its reply is lost, and the database is gone for 30s.
		if turn == 3 && first {
			database.mu.Lock()
			database.commitThenDrop = outage
			database.mu.Unlock()
		}
		return &reliantv1.CallLLMOutput{
			Message:      &reliantv1.MessageOutput{Role: "assistant", Text: fmt.Sprintf("turn %d", turn)},
			ResponseText: fmt.Sprintf("turn %d", turn),
		}, nil
	}

	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetDataConverter(rtemporal.NewFlexibleDataConverter())
	env.SetTestTimeout(30 * time.Second)
	registry := NewActivityRegistry(outageStore{wrapperTestRepo: &wrapperTestRepo{}, db: database})
	registry.SetMessageWriter(database)
	withOutageClock(clock)(registry)
	registerWrapped(env, registry, "CallLLM", callLLM)
	env.RegisterWorkflowWithOptions(outageAgentRun, workflow.RegisterOptions{Name: "outageAgentRun"})

	env.ExecuteWorkflow("outageAgentRun", 3)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError(), "a 30-second database outage must not fail the run")

	msgs := database.messages()
	require.Len(t, msgs, 3, "exactly one message per turn: none lost, none duplicated")
	for i, m := range msgs {
		assert.Equal(t, i+1, m.Runtime.LoopIteration)
		assert.Equal(t, fmt.Sprintf("turn %d", i+1), m.Args.GetResolvedContent())
		assert.Equal(t, fmt.Sprintf("msg-turn-%d", i+1), m.Runtime.AssistantMessageID)
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, map[int]int{1: 1, 2: 1, 3: 1}, llmCalls,
		"turn 2 failed before calling the model and turn 3's save waited in place: no LLM turn runs twice")
	assert.GreaterOrEqual(t, clock.Now().Sub(newVirtualClock().Now()), 2*outage, "both outages were waited out")
}

// TestDatabaseOutage_SaveRidesOutTheWholeWindow pins the window: a reply whose
// save meets a nine-minute outage is still written by the same attempt; one
// that meets a longer outage fails the attempt RETRYABLY, never terminally.
func TestDatabaseOutage_SaveRidesOutTheWholeWindow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		outage  time.Duration
		written bool
	}{
		{"nine minutes", 9 * time.Minute, true},
		{"beyond the ride-out", dbOutageRideOut + time.Minute, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clock := newVirtualClock()
			database := newOutageDatabase(clock)
			h := newWrapperHarness(t, "CallLLM", func(context.Context, types.ActivityInput) (*reliantv1.CallLLMOutput, error) {
				database.goDown(tc.outage) // the model answered; now the database is gone
				return thinkingCallLLMOutput(), nil
			}, withOutageClock(clock), func(r *ActivityRegistry) { r.SetMessageWriter(database) })

			_, err := h.run(t, "CallLLM", callLLMInput(saveRequest(agentAssistantSave(), nil)))
			if tc.written {
				require.NoError(t, err)
				assert.Len(t, database.messages(), 1)
				return
			}
			require.Error(t, err)
			assert.Empty(t, database.messages())
			var appErr *temporal.ApplicationError
			require.ErrorAs(t, err, &appErr)
			assert.False(t, appErr.NonRetryable(), "an outage must never fail a step terminally: %v", err)
			// Typed, so the workflow side and the error summary see an outage
			// even though this cause still carries the incident's mislabel.
			assert.Equal(t, DatabaseUnavailableErrType, appErr.Type())
		})
	}
}

// TestClassifyError_DatabaseOutageIsRetryable is the classification half on
// its own, with the incident's exact error: before the fix, its "does not
// exist" made it terminal.
func TestClassifyError_DatabaseOutageIsRetryable(t *testing.T) {
	t.Parallel()
	for name, err := range map[string]error{
		"as raised":     fmt.Errorf("save_message for call_llm: writing message (after 5 tries): %w", incidentError("8bf7a9b0")),
		"text only":     fmt.Errorf("save_message for call_llm: %s", incidentError("8bf7a9b0").Error()),
		"admin restart": &pgconn.PgError{Severity: "FATAL", Code: "57P01", Message: "terminating connection due to administrator command"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			classified := classifyError(err)
			assert.False(t, isTerminal(classified), "a database outage must stay retryable: %v", classified)
			assert.Equal(t, "database_unavailable", categorizeError(err))
		})
	}
}

// hangingProbeStore is a database that accepts the probe's connection and
// never answers: GetWorkflow returns only when its context ends.
type hangingProbeStore struct {
	*wrapperTestRepo
}

func (hangingProbeStore) GetWorkflow(ctx context.Context, _ string) (*db.Workflow, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestAwaitDatabase_ProbeNeverOutrunsTheDeadline: a hold whose probe hangs
// still returns the typed, retryable error by deadline-margin, not when the
// probe's own five-second timeout fires past the attempt's deadline.
func TestAwaitDatabase_ProbeNeverOutrunsTheDeadline(t *testing.T) {
	t.Parallel()
	w := &ActivityWrapper[types.ActivityInput, *reliantv1.CallLLMOutput]{
		repo: hangingProbeStore{&wrapperTestRepo{}},
		dbOutage: dbOutagePolicy{
			rideOut:      dbOutageRideOut,
			firstBackoff: time.Millisecond,
			maxBackoff:   time.Millisecond,
			clock:        wallClock{},
		},
	}
	deadline := time.Now().Add(dbOutageDeadlineMargin + 200*time.Millisecond)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	err := w.awaitDatabase(ctx, "wf-1", "CallLLM", "act-1", incidentError("8bf7a9b0"))

	assert.True(t, time.Now().Before(deadline.Add(-dbOutageDeadlineMargin/2)),
		"the hold must end at deadline-margin, leaving the margin to fail by itself")
	var appErr *temporal.ApplicationError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, DatabaseUnavailableErrType, appErr.Type())
	assert.False(t, appErr.NonRetryable())
}
