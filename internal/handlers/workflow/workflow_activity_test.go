package workflow_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	workflow "github.com/reliant-labs/reliant/internal/handlers/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
)

// testPrincipal names every call's caller as the test user.
type testPrincipal struct{}

func (testPrincipal) Actor(context.Context) activity.Actor { return activity.User("test-user", "") }
func (testPrincipal) Scope(context.Context) string         { return "test-user" }

// activityClient serves svc behind forge's real activity interceptor and
// returns a client for it plus the events the interceptor wrote. The
// declarations under test are the ones workflow.proto carries.
func activityClient(t *testing.T, svc reliantv1connect.WorkflowServiceHandler) (reliantv1connect.WorkflowServiceClient, func() []activity.Event) {
	t.Helper()
	var (
		mu     sync.Mutex
		events []activity.Event
	)
	ic, err := activity.Interceptor(activity.Deps{
		Principal: testPrincipal{},
		Writer: activity.WriterFunc(func(_ context.Context, ev activity.Event) error {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, ev)
			return nil
		}),
	})
	require.NoError(t, err)
	authed := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			return next(auth.WithUser(ctx, "test-user", "user", "test@example.com"), req)
		}
	})
	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewWorkflowServiceHandler(svc, connect.WithInterceptors(authed, ic)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return reliantv1connect.NewWorkflowServiceClient(srv.Client(), srv.URL), func() []activity.Event {
		mu.Lock()
		defer mu.Unlock()
		return append([]activity.Event(nil), events...)
	}
}

// DeleteWorkflow's event names the slug the delete resolved (the response's,
// since the request carries only the name) and labels it with the name.
func TestDeleteWorkflow_RecordsADeleteEventForTheSlug(t *testing.T) {
	t.Parallel()
	slug := workflowref.Slug("pr-review")
	store := newAutomationsStore(slug)
	svc := workflow.NewTestWorkflow(t, workflow.WithWorkflowDeps(workflow.Deps{Repo: store, Automations: &retirer{}}))
	client, events := activityClient(t, svc)

	resp, err := client.DeleteWorkflow(context.Background(), connect.NewRequest(&pb.DeleteWorkflowRequest{ProjectId: "p-1", Name: "pr-review"}))
	require.NoError(t, err)
	assert.Equal(t, slug, resp.Msg.GetSlug())

	got := events()
	require.Len(t, got, 1)
	assert.Equal(t, "delete", got[0].Verb)
	assert.Equal(t, activity.Target{Kind: "workflow", ID: slug, Label: "pr-review"}, got[0].Target)
	assert.Equal(t, "test-user", got[0].Actor.ID)
	assert.Equal(t, reliantv1connect.WorkflowServiceDeleteWorkflowProcedure, got[0].Procedure)
}

// An import that refuses to overwrite stores nothing, so it records nothing:
// the handler skips the declared "import" event.
func TestImportWorkflow_ConflictRecordsNoEvent(t *testing.T) {
	t.Parallel()
	slug := workflowref.Slug("pr-review")
	store := newAutomationsStore(slug)
	svc := workflow.NewTestWorkflow(t, workflow.WithWorkflowDeps(workflow.Deps{Repo: store}))
	client, events := activityClient(t, svc)

	resp, err := client.ImportWorkflow(context.Background(), connect.NewRequest(&pb.ImportWorkflowRequest{
		ProjectId:   "p-1",
		YamlContent: []byte("name: pr-review\nentry: [a]\nnodes:\n  - id: a\n    type: call_llm\n"),
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.GetConflict(), resp.Msg.GetMessage())
	assert.Empty(t, events(), "a refused import must not be recorded as one")
}
