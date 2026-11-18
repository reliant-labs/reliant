// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"errors"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// orgSourcesStub stands in for the scoped loader: the scope's sources are
// anthropic alone when anthropic is set, none otherwise, and every scope
// asked is recorded.
type orgSourcesStub struct {
	mu        sync.Mutex
	anthropic bool
	err       error
	scopes    []drivers.Scope
}

func (s *orgSourcesStub) lookup(_ context.Context, scope drivers.Scope) (models.AvailableDrivers, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scopes = append(s.scopes, scope)
	if s.err != nil {
		return models.AvailableDrivers{}, s.err
	}
	available := models.AvailableDrivers{Drivers: map[models.DriverID]models.DriverConfig{}}
	if s.anthropic {
		available.Drivers["anthropic"] = models.DriverConfig{DriverID: "anthropic", APIKey: "sk-ant-api03-test", Enabled: true}
	}
	return available, nil
}

func (s *orgSourcesStub) asked() []drivers.Scope {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]drivers.Scope(nil), s.scopes...)
}

// The warning is the org_only stamp's alone, and is asked of the one scoped
// loader in the scope the fire's calls use: an enabled automation of an org
// whose model no source of that scope serves is warned and named; a paused
// one, one of no org, one that may use any source, one with no model and one
// on a local model are not, and none of those reads the org's accounts. A
// failed read is no warning: advice, never a guess.
func TestFundingWarning_WarnsOnlyAnOrgOnlyAutomationNoOrgAccountCanRun(t *testing.T) {
	t.Parallel()
	model := func(sel map[string]any) map[string]any { return map[string]any{"model": sel} }
	orgOnly := func(mutate func(*core.Trigger)) *core.Trigger {
		tr := &core.Trigger{
			ID: "t1", UserID: "owner", OrgID: ptr.Of("acme"), Enabled: true,
			UnattendedFunding: core.UnattendedFundingOrgOnly,
			Params:            model(map[string]any{"id": "gpt-5.6-sol@codex"}),
		}
		if mutate != nil {
			mutate(tr)
		}
		return tr
	}

	sources := &orgSourcesStub{anthropic: true}
	s := &Service{sources: sources.lookup}
	w := s.fundingWarning(context.Background(), orgOnly(nil))
	require.NotNil(t, w)
	assert.Equal(t, "acme", w.GetOrgId())
	assert.Equal(t, "gpt-5.6-sol", w.GetModel())
	assert.Contains(t, w.GetMessage(), "gpt-5.6-sol")
	assert.Equal(t, []drivers.Scope{{UserID: "owner", OrgID: "acme"}}, sources.asked(),
		"the unattended org-funded scope: the org, nobody attending, not any source")

	tagged := orgOnly(func(tr *core.Trigger) { tr.Params = model(map[string]any{"tags": []any{"flagship"}}) })
	assert.Nil(t, s.fundingWarning(context.Background(), tagged), "a model an org account serves")

	for name, mutate := range map[string]func(*core.Trigger){
		"paused":                 func(tr *core.Trigger) { tr.Enabled = false },
		"of no org":              func(tr *core.Trigger) { tr.OrgID = nil },
		"may use any source":     func(tr *core.Trigger) { tr.UnattendedFunding = core.UnattendedFundingAny },
		"unstamped (any)":        func(tr *core.Trigger) { tr.UnattendedFunding = "" },
		"with no model input":    func(tr *core.Trigger) { tr.Params = nil },
		"on a local model":       func(tr *core.Trigger) { tr.Params = model(map[string]any{"id": "qwen3@local"}) },
		"with a nil model input": func(tr *core.Trigger) { tr.Params = map[string]any{"model": nil} },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sources := &orgSourcesStub{}
			s := &Service{sources: sources.lookup}
			assert.Nil(t, s.fundingWarning(context.Background(), orgOnly(mutate)))
			assert.Empty(t, sources.asked(), "the org's accounts are not read")
		})
	}

	failing := &orgSourcesStub{err: errors.New("store down")}
	assert.Nil(t, (&Service{sources: failing.lookup}).fundingWarning(context.Background(), orgOnly(nil)))
}

// Create, update and resume of an org_only automation whose model no org
// account serves are stored as asked and answered with the warning; a pause
// is not warned. Once an org account serves the model the warning is gone.
func TestFundingWarning_OnCreateUpdateAndResume(t *testing.T) {
	t.Parallel()
	e := setupAdoption(t)
	e.configure(t, func(d *Deps) { d.Orgs = &countedOrgs{members: 2, known: true} })
	sources := &orgSourcesStub{}
	e.svc.sources = sources.lookup

	def := func(mutate func(*reliantv1.TriggerDefinition)) *reliantv1.TriggerDefinition {
		return e.definition(func(d *reliantv1.TriggerDefinition) {
			d.ProjectId = e.acmeProject
			d.Params = mockModelTriggerParams(t)
			if mutate != nil {
				mutate(d)
			}
		})
	}
	row, created := e.made(t, def(nil))
	require.Equal(t, core.UnattendedFundingOrgOnly, row.UnattendedFunding, "a two-member org stamps org_only")
	require.NotNil(t, created.GetFundingWarning(), "the write stands, with the warning")
	assert.Equal(t, "mock", created.GetFundingWarning().GetModel())
	assert.True(t, row.Enabled)

	paused, err := e.svc.SetTriggerEnabled(e.creatorCtx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: row.ID, Enabled: false}))
	require.NoError(t, err)
	assert.Nil(t, paused.Msg.GetFundingWarning(), "a paused automation spends nothing")

	resumed, err := e.svc.SetTriggerEnabled(e.creatorCtx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: row.ID, Enabled: true}))
	require.NoError(t, err)
	require.NotNil(t, resumed.Msg.GetFundingWarning())

	updated, err := e.svc.UpdateTrigger(e.creatorCtx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{Id: row.ID, Trigger: def(nil)}))
	require.NoError(t, err)
	require.NotNil(t, updated.Msg.GetFundingWarning())

	anthropic, err := structpb.NewValue(map[string]any{"tags": []any{"flagship"}})
	require.NoError(t, err)
	sources.mu.Lock()
	sources.anthropic = true
	sources.mu.Unlock()
	served, err := e.svc.UpdateTrigger(e.creatorCtx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{Id: row.ID, Trigger: def(func(d *reliantv1.TriggerDefinition) {
		d.Params = map[string]*structpb.Value{"model": anthropic}
	})}))
	require.NoError(t, err)
	assert.Nil(t, served.Msg.GetFundingWarning(), "an org account serves the model")
}

const fundingWorkflowYAML = `name: funded
inputs:
  model: {type: model}
triggers:
  - name: nightly
    schedule: {cron: "0 9 * * *"}
    prompt: "Do the nightly sweep."
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

// Enabling a workflow in an org reports the warning on the trigger's result,
// and also as one of its warnings, for a client that predates the field.
func TestFundingWarning_OnEnableWorkflow(t *testing.T) {
	t.Parallel()
	e := setupAdoption(t)
	e.configure(t, func(d *Deps) { d.Orgs = &countedOrgs{members: 2, known: true} })
	e.svc.sources = (&orgSourcesStub{}).lookup
	e.saveWorkflow(t, "funded", fundingWorkflowYAML)

	resp, err := e.svc.EnableWorkflow(e.creatorCtx, connect.NewRequest(&reliantv1.EnableWorkflowRequest{
		Workflow: "funded", ProjectId: e.acmeProject, DaemonId: e.daemonID, Params: mockModelTriggerParams(t),
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.GetEnabled(), "%v", resp.Msg.GetResults())
	require.Len(t, resp.Msg.GetResults(), 1)
	r := resp.Msg.GetResults()[0]
	require.NotNil(t, r.GetFundingWarning())
	assert.Equal(t, "mock", r.GetFundingWarning().GetModel())
	assert.Contains(t, r.GetWarnings(), r.GetFundingWarning().GetMessage())
}
