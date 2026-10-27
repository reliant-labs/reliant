//go:build integration

package privatepipelineharness

import (
	"context"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
	runtime "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"strings"
	"sync/atomic"
	"testing"
)

type reviewerCANControl struct {
	initial  string
	pressure atomic.Bool
	calls    atomic.Int32
}

func (c *reviewerCANControl) loaded(run string) {
	if run != c.initial {
		c.pressure.Store(false)
	}
}
func registerCANReviewModelBoundary(t *testing.T, w worker.Worker, p *checkProvider, chat, initial string) *reviewerCANControl {
	t.Helper()
	c := &reviewerCANControl{initial: initial}
	runtime.PrivatePipelineHistoryPressure(t, chat, c.pressure.Load)
	w.RegisterActivityWithOptions(func(context.Context, types.ActivityInput) (map[string]any, error) {
		if c.calls.Add(1) == 1 {
			c.pressure.Store(true)
		}
		p.mu.Lock()
		head := p.head
		p.mu.Unlock()
		verdict := "changes"
		if head == strings.Repeat("b", 40) {
			verdict = "approve"
		}
		return map[string]any{"response_text": verdict, "tool_calls": nil}, nil
	}, activity.RegisterOptions{Name: "CallLLM"})
	w.RegisterActivityWithOptions(func(context.Context, map[string]any) (map[string]any, error) {
		return map[string]any{"daemon_available": true, "daemon_id": "private-fixture"}, nil
	}, activity.RegisterOptions{Name: "PreflightDaemonCheck"})
	return c
}
func assertActualReviewCAN(t *testing.T, ctx context.Context, f *resources, root, initial string) {
	t.Helper()
	proof, err := runcontinuity.Resolve(ctx, runcontinuity.TemporalReader{Service: f.Temporal.WorkflowService(), Namespace: f.Namespace}, runcontinuity.Execution{WorkflowID: root, RunID: initial})
	if err != nil {
		t.Fatal(err)
	}
	if len(proof.Edges) != 1 || proof.Current.RunID == initial || proof.Current.WorkflowID != root || proof.Live {
		t.Fatalf("actual reviewer did not retain authenticated private continuation: %+v", proof)
	}
	desc, err := f.Temporal.DescribeWorkflow(ctx, root, proof.Current.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if desc.WorkflowType.Name != "PrivateTrustedReviewer" {
		t.Fatalf("private hook lost across actual CAN: %s", desc.WorkflowType.Name)
	}
}
