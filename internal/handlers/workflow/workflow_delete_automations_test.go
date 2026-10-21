package workflow_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	workflow "github.com/reliant-labs/reliant/internal/handlers/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
)

type retirer struct{ retired []*core.Trigger }

func (r *retirer) RetireDeletedTriggers(_ context.Context, deleted []*core.Trigger) {
	r.retired = append(r.retired, deleted...)
}

// automationsStore is a draftsRepo that also holds automations. RunTx runs the
// function and, as a transaction does, discards the deletions it made when the
// function fails.
type automationsStore struct {
	*workflow.StubRepo
	triggers []*core.Trigger
	deleted  []string
}

func newAutomationsStore(slug string, triggers ...*core.Trigger) *automationsStore {
	s := &automationsStore{StubRepo: draftsRepo(callerDraftNoT(slug)), triggers: triggers}
	s.DeleteWorkflowDraftBySlugFunc = func(context.Context, string, string) error { return nil }
	s.ListTriggersFunc = func(context.Context, core.TriggerFilters) ([]*core.Trigger, error) { return s.triggers, nil }
	s.DeleteTriggerFunc = func(_ context.Context, id string) error {
		s.deleted = append(s.deleted, id)
		return nil
	}
	s.RunTxFunc = func(ctx context.Context, _ orm.TxPolicy, f func(context.Context) error) error {
		before := len(s.deleted)
		if err := f(ctx); err != nil {
			s.deleted = s.deleted[:before]
			return err
		}
		return nil
	}
	s.GetProjectWorkflowsJSONFunc = func(context.Context, string) (*string, error) { return nil, nil }
	return s
}

func automation(id, ref, project string) *core.Trigger {
	return &core.Trigger{ID: id, UserID: "test-user", ProjectID: project, Name: "Autopilot · " + id, Workflow: ref, Kind: core.TriggerKindWebhook}
}

func deleteFlow(t *testing.T, store *automationsStore, r *retirer, name string) (*connect.Response[pb.DeleteWorkflowResponse], error) {
	t.Helper()
	deps := workflow.Deps{Repo: store}
	if r != nil {
		deps.Automations = r
	}
	svc := workflow.NewTestWorkflow(t, workflow.WithWorkflowDeps(deps))
	return svc.DeleteWorkflow(workflow.AuthedContext(t), connect.NewRequest(&pb.DeleteWorkflowRequest{ProjectId: "p-1", Name: name}))
}

func TestDeleteWorkflow_DeletesItsAutomationsAcrossProjects(t *testing.T) {
	t.Parallel()
	slug := workflowref.Slug("pr-review")
	store := newAutomationsStore(slug,
		automation("a1", "pr-review", "p-1"),
		automation("a2", "project://PR Review", "p-1"),
		automation("a3", "pr-review", "p-2"),
		automation("other", "issue-worker", "p-1"),
		automation("builtin", "builtin://pr-review", "p-1"),
	)
	r := &retirer{}

	resp, err := deleteFlow(t, store, r, "pr-review")

	require.NoError(t, err)
	assert.True(t, resp.Msg.GetSuccess())
	assert.Contains(t, resp.Msg.GetMessage(), "3 automations")
	assert.ElementsMatch(t, []string{"a1", "a2", "a3"}, store.deleted, "only the automations that ran the deleted user workflow go")
	assert.Len(t, r.retired, 3, "their schedules and access are retired")
	assert.Len(t, store.Calls("DeleteWorkflowDraftBySlug"), 1)
}

func TestDeleteWorkflow_WithNoAutomationsRetiresNothing(t *testing.T) {
	t.Parallel()
	slug := workflowref.Slug("pr-review")
	store := newAutomationsStore(slug, automation("other", "issue-worker", "p-1"))
	r := &retirer{}

	resp, err := deleteFlow(t, store, r, "pr-review")

	require.NoError(t, err)
	assert.Equal(t, "Workflow deleted successfully", resp.Msg.GetMessage())
	assert.Empty(t, store.deleted)
	assert.Empty(t, r.retired)
}

func TestDeleteWorkflow_KeepsAutomationsOfAProjectWorkflowOfTheSameName(t *testing.T) {
	t.Parallel()
	slug := workflowref.Slug("pr-review")
	store := newAutomationsStore(slug,
		automation("synced", "pr-review", "p-synced"),
		automation("gone", "pr-review", "p-1"),
	)
	store.GetProjectWorkflowsJSONFunc = func(_ context.Context, project string) (*string, error) {
		if project != "p-synced" {
			return nil, nil
		}
		js := `[{"slug":"pr-review","name":"pr-review","relative_path":".reliant/workflows/pr-review.yaml","yaml_content":"name: pr-review\nentry: [a]\nnodes:\n  - id: a\n    type: approval\n    args: {title: ok}\n"}]`
		return &js, nil
	}

	_, err := deleteFlow(t, store, &retirer{}, "pr-review")

	require.NoError(t, err)
	assert.Equal(t, []string{"gone"}, store.deleted, "an automation that still resolves to the project's workflow keeps running it")
}

func TestDeleteWorkflow_IsAllOrNothing(t *testing.T) {
	t.Parallel()
	slug := workflowref.Slug("pr-review")
	store := newAutomationsStore(slug, automation("a1", "pr-review", "p-1"))
	store.DeleteWorkflowDraftBySlugFunc = func(context.Context, string, string) error { return errors.New("db down") }
	r := &retirer{}

	_, err := deleteFlow(t, store, r, "pr-review")

	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	assert.Empty(t, store.deleted, "the automation deletion rolled back with the workflow's")
	assert.Empty(t, r.retired, "nothing is retired for a delete that did not happen")
}

func TestDeleteWorkflow_WorksWithoutARetirer(t *testing.T) {
	t.Parallel()
	slug := workflowref.Slug("pr-review")
	store := newAutomationsStore(slug, automation("a1", "pr-review", "p-1"))

	_, err := deleteFlow(t, store, nil, "pr-review")

	require.NoError(t, err)
	assert.Equal(t, []string{"a1"}, store.deleted)
}

func callerDraftNoT(slug string) *db.WorkflowDraft {
	return &db.WorkflowDraft{ID: "wf-" + slug, UserID: "test-user", Name: slug, Slug: slug, Definition: "name: x", Status: db.WorkflowDraftStatusComplete}
}
