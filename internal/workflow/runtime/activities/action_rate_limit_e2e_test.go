// Copyright (c) 2025 Reliant Labs
package activities

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/netguard"
	rtemporal "github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// rateLimitStore is the minimum the activity wrapper writes to: liveness,
// step rows, node events and chat updates, all discarded.
type rateLimitStore struct{}

func (rateLimitStore) EnsureWorkflowRunning(context.Context, string, string)        {}
func (rateLimitStore) CreateStepExecution(context.Context, *db.StepExecution) error { return nil }
func (rateLimitStore) EmitNodeExecutionEvent(context.Context, string, *db.NodeExecutionState) error {
	return nil
}
func (rateLimitStore) CreateChatUpdate(context.Context, string, reliantv1.ChatUpdateType, string, string) error {
	return nil
}
func (rateLimitStore) LoadWorkflow(context.Context, string) (*db.Workflow, error) { return nil, nil }

// runRateLimitedAction runs a one-node workflow whose http/request action
// calls srv, with the action registered through the runtime's own activity
// wrapper (so its errors are classified as production classifies them). It
// returns the workflow's outputs and the (mock-clock) time each attempt of
// the action started.
func runRateLimitedAction(t *testing.T, srv *httptest.Server) (map[string]interface{}, []time.Time) {
	t.Helper()
	guard := netguard.New()
	guard.AllowLoopback = true
	registry := v2.NewActivityRegistry(v2.ActivityRegistryDeps{Store: rateLimitStore{}, Workflows: rateLimitStore{}})
	v2.RegisterActivity(registry, handlers.NewActionActivityWith(catalog.MustBuiltin(), httpaction.NewRunner(guard), nil))
	wrapped, err := registry.Get("Action")
	require.NoError(t, err)

	wf := &reliantv1.Workflow{
		Name:  "action-rate-limit",
		Entry: []string{"call"},
		Nodes: []*reliantv1.Node{{
			Id: "call", Type: model.NodeTypeAction,
			Args: &reliantv1.Node_Action{Action: &reliantv1.ActionArgs{
				Uses: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "http/request@1"}},
				With: map[string]*structpb.Value{"url": structpb.NewStringValue(srv.URL + "/limited")},
			}},
		}},
		Outputs: map[string]string{
			"status":    "{{nodes.call.status_code}}",
			"is_error":  "{{nodes.call.is_error}}",
			"retryable": "{{nodes.call.retryable}}",
		},
	}
	workflowBytes, err := protojson.Marshal(wf)
	require.NoError(t, err)

	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	// Production's converter, which carries the typed *ActionOutput the
	// wrapped activity returns into the workflow's map.
	env.SetDataConverter(rtemporal.NewFlexibleDataConverter())
	var mu sync.Mutex
	var starts []time.Time
	env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, _ converter.EncodedValues) {
		if info.ActivityType.Name == "Action" {
			mu.Lock()
			starts = append(starts, env.Now())
			mu.Unlock()
		}
	})
	env.RegisterActivityWithOptions(wrapped, activity.RegisterOptions{Name: "Action"})
	env.RegisterActivityWithOptions(func(_ context.Context, _ types.LoadWorkflowInput) (v2.LoadedWorkflow, error) {
		return v2.LoadedWorkflow{WorkflowJSON: workflowBytes}, nil
	}, activity.RegisterOptions{Name: "ActivityLoadWorkflow"})
	env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (interface{}, error) { return nil, nil },
		activity.RegisterOptions{Name: "WorkflowStatus"})
	env.RegisterActivityWithOptions(func(_ context.Context, _ types.WorkflowCheckpointInput) (map[string]interface{}, error) {
		return map[string]interface{}{"success": true}, nil
	}, activity.RegisterOptions{Name: "WorkflowCheckpoint"})
	env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{}, nil
	}, activity.RegisterOptions{Name: "Cleanup"})

	env.ExecuteWorkflow(v2.DynamicWorkflow, v2.WorkflowInput{
		ChatID: "chat-rate-limit", WorkflowName: "action-rate-limit", Inputs: map[string]interface{}{},
		ExecContext: &v2.ExecutionContext{
			WorkflowID: "wf-rate-limit", ChatID: "chat-rate-limit", Thread: "thread-rate-limit",
			ThreadMode: model.ThreadModeNew, WorkflowName: "action-rate-limit",
		},
	})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var result v2.WorkflowResult
	require.NoError(t, env.GetWorkflowResult(&result))
	mu.Lock()
	defer mu.Unlock()
	return result.Outputs, append([]time.Time(nil), starts...)
}

// A rate-limited action waits out the provider's Retry-After before its
// retry: Temporal schedules the next attempt with the NextRetryDelay the
// action computed, and the retry that gets through is what the graph sees.
func TestActionNode_RateLimitedCallRetriesAfterTheProvidersWait(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n <= 2 {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"slow down"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	outputs, starts := runRateLimitedAction(t, srv)
	require.EqualValues(t, 200, outputs["status"], "the retry that got through is what the graph sees")
	require.Equal(t, false, outputs["is_error"])
	require.Len(t, starts, 3, "two refused attempts, then the one that got through")
	for i := 1; i < len(starts); i++ {
		gap := starts[i].Sub(starts[i-1])
		require.GreaterOrEqual(t, gap, 120*time.Second, "attempt %d must wait the provider's Retry-After", i+1)
		require.LessOrEqual(t, gap, 132*time.Second, "attempt %d waits Retry-After plus at most a tenth of jitter", i+1)
	}
}

// A limit that never lifts is bounded by the step's retry ladder, and the
// LAST attempt answers the graph exactly as an action always did (is_error,
// retryable, the provider's status): the run never pauses for a rate limit.
func TestActionNode_RateLimitThatNeverLiftsAnswersTheGraphOnTheLastAttempt(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	outputs, starts := runRateLimitedAction(t, srv)
	require.EqualValues(t, 429, outputs["status"])
	require.Equal(t, true, outputs["is_error"])
	require.Equal(t, true, outputs["retryable"])
	require.Len(t, starts, int(types.StepActivityMaxAttempts), "every attempt of the step's ladder, and no more")
	for i := 1; i < len(starts); i++ {
		require.Equal(t, httpaction.RateLimitMaxDelay, starts[i].Sub(starts[i-1]),
			"an hour-long Retry-After is retried at the cap, not made permanent")
	}
}
