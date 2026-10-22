// Copyright (c) 2025 Reliant Labs
package temporal

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/workflow"
)

// TestFlexibleDataConverterActivityBindingDropsDeadlockPause pins the binding
// chain the SDK applies to a local activity's converter: serialization
// context and workflow context when the activity is scheduled, then
// WithContext on the local-activity goroutine, which encodes the result. That
// last converter runs off the workflow goroutine, so it must not be (or wrap)
// Temporal's deadlock-pausing converter: pausing the workflow's detector from
// there races the dispatcher on the detector's unordered locks and can wedge
// the workflow task forever.
func TestFlexibleDataConverterActivityBindingDropsDeadlockPause(t *testing.T) {
	dc := NewFlexibleDataConverter()

	var wfBound converter.DataConverter = dc
	wfBound = converter.WithDataConverterSerializationContext(wfBound, converter.ActivitySerializationContext{IsLocal: true})
	wfBound = wfBound.(converter.ContextAware).WithWorkflowContext(nil)
	if _, ok := wfBound.(*workflowDeadlockPausingConverter); !ok {
		t.Fatalf("workflow-bound converter is %T; want it to keep pausing the deadlock detector", wfBound)
	}

	actBound := wfBound.(converter.ContextAware).WithContext(context.Background())
	switch got := fmt.Sprintf("%T", actBound); got {
	case "*temporal.workflowDeadlockPausingConverter", "*internal.dataConverterWithoutDeadlock":
		t.Fatalf("activity-bound converter is %s; it would pause the workflow's deadlock detector off the workflow goroutine", got)
	}

	// Still the same codec: what one side encodes the other decodes.
	payloads, err := actBound.ToPayloads(bigResult{Content: "x", Meta: map[string]string{"k": "v"}})
	if err != nil {
		t.Fatalf("ToPayloads: %v", err)
	}
	var out bigResult
	if err := wfBound.FromPayloads(payloads, &out); err != nil || out.Content != "x" || out.Meta["k"] != "v" {
		t.Fatalf("round trip: err=%v out=%+v", err, out)
	}
}

func echoLocalActivity(_ context.Context, i int) (int, error) { return i, nil }

// fanOutLocalActivitiesWorkflow keeps many local activities in flight at once,
// so their results are encoded while the dispatcher is running other
// coroutines — the overlap that deadlocked under the old converter.
func fanOutLocalActivitiesWorkflow(ctx workflow.Context, n int) (int, error) {
	ctx = workflow.WithLocalActivityOptions(ctx, workflow.LocalActivityOptions{StartToCloseTimeout: time.Minute})
	futures := make([]workflow.Future, n)
	for i := range futures {
		futures[i] = workflow.ExecuteLocalActivity(ctx, echoLocalActivity, i)
	}
	sum := 0
	for _, f := range futures {
		var v int
		if err := f.Get(ctx, &v); err != nil {
			return 0, err
		}
		sum += v
	}
	return sum, nil
}

// TestFlexibleDataConverterConcurrentLocalActivitiesDoNotDeadlock drives the
// real SDK path end to end. Under the old converter (a bare
// workflow.DataConverterWithoutDeadlockDetection) this hung in
// deadlockTicker.end / deadlockDetector.resume within a few rounds.
func TestFlexibleDataConverterConcurrentLocalActivitiesDoNotDeadlock(t *testing.T) {
	t.Parallel()
	const rounds, fanOut = 20, 32
	for round := range rounds {
		var suite temporaltest.WorkflowTestSuite
		env := suite.NewTestWorkflowEnvironment()
		env.SetDataConverter(NewFlexibleDataConverter())
		env.RegisterActivity(echoLocalActivity)

		done := make(chan struct{})
		go func() {
			defer close(done)
			env.ExecuteWorkflow(fanOutLocalActivitiesWorkflow, fanOut)
		}()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("round %d: workflow wedged with %d local activities in flight (deadlock-detector lock inversion?)", round, fanOut)
		}

		if err := env.GetWorkflowError(); err != nil {
			t.Fatalf("round %d: workflow error: %v", round, err)
		}
		var sum int
		if err := env.GetWorkflowResult(&sum); err != nil {
			t.Fatalf("round %d: result: %v", round, err)
		}
		if want := fanOut * (fanOut - 1) / 2; sum != want {
			t.Fatalf("round %d: sum = %d, want %d", round, sum, want)
		}
	}
}
