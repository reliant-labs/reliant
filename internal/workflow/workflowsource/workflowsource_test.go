// Copyright (c) 2025 Reliant Labs
package workflowsource

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func def(name string) string {
	return "name: " + name + "\nentry: [x]\nnodes:\n  - id: x\n    type: save_message\n    args: {role: assistant, content: hi}\n"
}

// fakeStore holds one user's workflows by slug and one project's synced files.
type fakeStore struct {
	usable    map[string]*db.WorkflowDraft
	drafts    map[string]*db.WorkflowDraft
	project   []config.StoredWorkflow
	readErr   error
	notRunErr map[string]bool
}

func (f fakeStore) GetUsableWorkflowBySlug(_ context.Context, _, slug string) (*db.WorkflowDraft, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	if f.notRunErr[slug] {
		return nil, &db.WorkflowDraftNotRunnableError{Slug: slug}
	}
	return f.usable[slug], nil
}

func (f fakeStore) GetWorkflowDraftBySlug(_ context.Context, _, slug string) (*db.WorkflowDraft, error) {
	return f.drafts[slug], nil
}

func (f fakeStore) GetProjectWorkflowsJSON(context.Context, string) (*string, error) {
	data, err := json.Marshal(f.project)
	if err != nil {
		return nil, err
	}
	s := string(data)
	return &s, nil
}

var opts = Options{UserID: "u", ProjectID: "p"}

func TestResolve_ProjectWorkflowByName(t *testing.T) {
	store := fakeStore{project: []config.StoredWorkflow{
		{RelativePath: ".reliant/workflows/blog.yaml", YAMLContent: def("blog-content-pipeline")},
	}}
	r, err := Resolve(context.Background(), store, opts, "project://blog-content-pipeline")
	require.NoError(t, err)
	assert.Equal(t, workflowref.SourceProject, r.Source)
	assert.Equal(t, "blog.yaml", r.Path)

	_, err = Resolve(context.Background(), store, opts, "project://blog")
	require.ErrorIs(t, err, workflowref.ErrNotFound)
	assert.Contains(t, err.Error(), `blog.yaml is named "blog-content-pipeline"`)
}

func TestResolve_UserWorkflowShadowsProject(t *testing.T) {
	store := fakeStore{
		usable:  map[string]*db.WorkflowDraft{"flow": {Definition: def("flow")}},
		project: []config.StoredWorkflow{{RelativePath: ".reliant/workflows/flow.yaml", YAMLContent: def("flow")}},
	}
	r, err := Resolve(context.Background(), store, opts, "flow")
	require.NoError(t, err)
	assert.Equal(t, workflowref.SourceUser, r.Source)
}

func TestResolve_AUserDraftThatCannotRunIsFinal(t *testing.T) {
	store := fakeStore{
		notRunErr: map[string]bool{"flow": true},
		project:   []config.StoredWorkflow{{RelativePath: ".reliant/workflows/flow.yaml", YAMLContent: def("flow")}},
	}
	_, err := Resolve(context.Background(), store, opts, "flow")
	var notRunnable *db.WorkflowDraftNotRunnableError
	require.ErrorAs(t, err, &notRunnable, "never silently run the project's flow in place of the user's")
}

func TestResolve_AFailedReadIsAStoreError(t *testing.T) {
	store := fakeStore{readErr: errors.New("connection reset")}
	_, err := Resolve(context.Background(), store, opts, "flow")
	var storeErr *StoreError
	require.ErrorAs(t, err, &storeErr, "a failed read is retryable, not a verdict")
}

// untouchableStore fails the test if resolution reads anything from it.
type untouchableStore struct{ t *testing.T }

func (s untouchableStore) GetProjectWorkflowsJSON(context.Context, string) (*string, error) {
	s.t.Errorf("resolving a builtin read the project's workflows")
	return nil, errors.New("unexpected read")
}

func (s untouchableStore) GetUsableWorkflowBySlug(context.Context, string, string) (*db.WorkflowDraft, error) {
	s.t.Errorf("resolving a builtin read the user's workflows")
	return nil, errors.New("unexpected read")
}

func (s untouchableStore) GetWorkflowDraftBySlug(context.Context, string, string) (*db.WorkflowDraft, error) {
	s.t.Errorf("resolving a builtin read the user's drafts")
	return nil, errors.New("unexpected read")
}

// A builtin:// ref cannot be shadowed by a user or project workflow, so
// resolving one must not read either. Every send of the default workflow
// (builtin://agent) resolves it several times; each read used to fetch the
// whole project config record, 18 MB in prod.
func TestResolve_BuiltinReadsNothingFromTheStore(t *testing.T) {
	r, err := Resolve(context.Background(), untouchableStore{t}, opts, "builtin://agent")
	require.NoError(t, err)
	assert.Equal(t, workflowref.SourceBuiltin, r.Source)

	_, err = Resolve(context.Background(), untouchableStore{t}, opts, "builtin://no-such-builtin")
	require.ErrorIs(t, err, workflowref.ErrNotFound)
}

func TestResolve_DraftRootMayBeIncomplete(t *testing.T) {
	store := fakeStore{
		notRunErr: map[string]bool{"wip": true},
		drafts:    map[string]*db.WorkflowDraft{"wip": {Definition: def("wip")}},
	}
	r, err := Resolve(context.Background(), store, Options{UserID: "u", DraftRoot: "WIP"}, "project://wip")
	require.NoError(t, err)
	assert.Equal(t, "wip", r.Workflow.GetName())
}

func TestDraftLoader(t *testing.T) {
	self, err := wfyaml.ParseWorkflow([]byte(def("my-flow")))
	require.NoError(t, err)
	load := DraftLoader(context.Background(), fakeStore{}, "u", self)

	wf, err := load("project://My Flow")
	require.NoError(t, err)
	assert.Same(t, self, wf, "a draft resolves to itself")

	wf, err = load("project://somewhere-else")
	require.NoError(t, err, "a project ref outside any project is left for run start to resolve")
	assert.Nil(t, wf)

	_, err = load("builtin://no-such-builtin")
	require.ErrorIs(t, err, workflowref.ErrNotFound, "no project can supply a missing builtin")
}
