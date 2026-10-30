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

type diagnosticStore struct{ machineStore }

func (diagnosticStore) WorkflowSourceDiagnostic(_ context.Context, _, daemonID, _ string) (string, error) {
	return `machine "` + daemonID + `" has no workflow "merge-steward"; it is defined on machine "default" at control-plane/.reliant/workflows/steward.yaml`, nil
}

func TestResolveMissingWorkflowIncludesSourceDiagnostic(t *testing.T) {
	_, err := Resolve(context.Background(), diagnosticStore{machineStore{machines: map[string][]config.StoredWorkflow{"cicd": nil}}}, Options{UserID: "u", ProjectID: "p", DaemonID: "cicd"}, "merge-steward")
	require.ErrorIs(t, err, workflowref.ErrNotFound)
	require.ErrorContains(t, err, `no project workflow is named "merge-steward"`)
	require.ErrorContains(t, err, `machine "cicd" has no workflow "merge-steward"; it is defined on machine "default" at control-plane/.reliant/workflows/steward.yaml`)
}

// machineStore is a project synced by two machines: the union (fakeStore's
// project) and each machine's own catalog.
type machineStore struct {
	fakeStore
	machines map[string][]config.StoredWorkflow
}

func (s machineStore) GetProjectMachineWorkflowsJSON(_ context.Context, _, daemonID string) (*string, bool, error) {
	files, ok := s.machines[daemonID]
	if !ok {
		return nil, false, nil
	}
	v, err := config.EncodeCatalogJSON(files)
	return v, true, err
}

func (s machineStore) GetProjectMachinePresetsJSON(context.Context, string, string) (*string, bool, error) {
	return nil, false, nil
}

// A run on one machine resolves against what that machine synced: a workflow
// only another machine provides is a miss there, while the union (listings,
// runs with no machine) still has it. A machine that never synced the project
// falls back to the union.
func TestResolve_OnAMachineReadsThatMachinesCatalog(t *testing.T) {
	release := config.StoredWorkflow{RelativePath: "control-plane/.reliant/workflows/release.yaml", YAMLContent: def("release")}
	store := machineStore{
		fakeStore: fakeStore{project: []config.StoredWorkflow{release}},
		machines:  map[string][]config.StoredWorkflow{"default": {release}, "cicd": nil},
	}
	ctx := context.Background()

	r, err := Resolve(ctx, store, Options{ProjectID: "p", DaemonID: "default"}, "release")
	require.NoError(t, err)
	assert.Equal(t, workflowref.SourceProject, r.Source)

	_, err = Resolve(ctx, store, Options{ProjectID: "p", DaemonID: "cicd"}, "release")
	require.ErrorIs(t, err, workflowref.ErrNotFound, "machine cicd synced the project without the workflow")

	_, err = Resolve(ctx, store, Options{ProjectID: "p"}, "release")
	require.NoError(t, err, "the union still carries what default provides")

	_, err = Resolve(ctx, store, Options{ProjectID: "p", DaemonID: "never-synced"}, "release")
	require.NoError(t, err, "a machine that never synced the project falls back to the union")
}

// A store that cannot read a machine's catalog must not answer a
// machine-scoped resolution from the union: that is the bug this replaced.
func TestResolve_OnAMachineRefusesAStoreWithoutMachineCatalogs(t *testing.T) {
	_, err := Resolve(context.Background(), fakeStore{}, Options{ProjectID: "p", DaemonID: "cicd"}, "release")
	var storeErr *StoreError
	require.ErrorAs(t, err, &storeErr)
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

// An automation's precedence: the project's workflow of a name before the
// owner's own, so an account draft cannot silently replace what a trigger
// runs. The shadow is still reported, both ways.
func TestResolve_ProjectFirstForAutomations(t *testing.T) {
	store := fakeStore{
		usable:  map[string]*db.WorkflowDraft{"flow": {Definition: def("flow")}, "mine": {Definition: def("mine")}},
		project: []config.StoredWorkflow{{RelativePath: ".reliant/workflows/flow.yaml", YAMLContent: def("flow")}},
	}
	auto := opts
	auto.ProjectFirst = true

	r, err := Resolve(context.Background(), store, auto, "flow")
	require.NoError(t, err)
	assert.Equal(t, workflowref.SourceProject, r.Source)
	assert.Equal(t, `your account workflow "flow"`, r.Shadowed)

	r, err = Resolve(context.Background(), store, opts, "flow")
	require.NoError(t, err)
	assert.Equal(t, workflowref.SourceUser, r.Source, "a chat keeps yours-wins-for-you")
	assert.Equal(t, "the project's .reliant/workflows/flow.yaml", r.Shadowed)

	r, err = Resolve(context.Background(), store, auto, "mine")
	require.NoError(t, err)
	assert.Equal(t, workflowref.SourceUser, r.Source, "a name the project lacks is still the owner's")
	assert.Empty(t, r.Shadowed)
}

// Fail closed: a broken project workflow is the answer for an automation,
// never the owner's same-named one in its place.
func TestResolve_ProjectFirstABrokenProjectWorkflowIsFinal(t *testing.T) {
	store := fakeStore{
		usable: map[string]*db.WorkflowDraft{"flow": {Definition: def("flow")}},
		project: []config.StoredWorkflow{
			{RelativePath: ".reliant/workflows/a.yaml", YAMLContent: def("flow")},
			{RelativePath: ".reliant/workflows/b.yaml", YAMLContent: def("flow")},
		},
	}
	auto := opts
	auto.ProjectFirst = true
	_, err := Resolve(context.Background(), store, auto, "flow")
	require.ErrorContains(t, err, "also declared")
}

// An unfinished account draft of the same name is the collision that
// motivated the automation precedence: it is reported too, never run.
func TestResolve_ProjectFirstReportsAnUnfinishedAccountDraft(t *testing.T) {
	store := fakeStore{
		notRunErr: map[string]bool{"flow": true},
		project:   []config.StoredWorkflow{{RelativePath: ".reliant/workflows/flow.yaml", YAMLContent: def("flow")}},
	}
	auto := opts
	auto.ProjectFirst = true
	r, err := Resolve(context.Background(), store, auto, "flow")
	require.NoError(t, err)
	assert.Equal(t, workflowref.SourceProject, r.Source)
	assert.Equal(t, `your account workflow "flow"`, r.Shadowed)
}
