// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

func installationIn(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var cfg core.IntegrationConfig
	require.NoError(t, json.Unmarshal(raw, &cfg))
	return cfg.Installation
}

// An activation bound to its org's GitHub App installation keeps the binding
// when the reconciler re-projects it from its declaration, whether the
// declaration changed or not: the binding is the activation's, and no
// declaration states it.
func TestReconcileKeepsTheActivationsInstallation(t *testing.T) {
	issue := "new-issue"
	// A stale projection (the events the declaration no longer says) forces
	// a rewrite; the scoped one is rewritten from its params.
	stale := &core.Trigger{
		ID: "stale", UserID: "u1", ProjectID: "p1", Workflow: "triage", WorkflowTrigger: &issue,
		Kind:   core.TriggerKindIntegration,
		Config: json.RawMessage(`{"integration":"github","events":["issues.closed"],"installation":"4242"}`),
	}
	pr := "pr"
	scoped := &core.Trigger{
		ID: "scoped", UserID: "u1", ProjectID: "p1", Workflow: "scoped", WorkflowTrigger: &pr,
		Kind: core.TriggerKindIntegration, Params: map[string]any{"repos": []any{"acme/app"}},
		Config: json.RawMessage(`{"integration":"github","events":["pull_request.opened"],"installation":"7"}`),
	}
	unbound := &core.Trigger{
		ID: "unbound", UserID: "u1", ProjectID: "p1", Workflow: "triage", WorkflowTrigger: &issue,
		Kind:   core.TriggerKindIntegration,
		Config: json.RawMessage(`{"integration":"github","events":["issues.closed"]}`),
	}
	repo := &projectionRepo{triggers: map[string]*core.Trigger{"stale": stale, "scoped": scoped, "unbound": unbound}}
	r := NewReconciler(repo, fakeWorkflows{yaml: map[string]string{"triage": declaringWorkflow, "scoped": scopedWorkflow}}, nil)

	report, err := r.ReconcileAll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, report.Updated)
	assert.Equal(t, "4242", installationIn(t, repo.written["stale"].Config))
	assert.Equal(t, []string{"issues.opened"}, scopeOf(t, repo.written["stale"].Config).Events, "the declaration's part is re-projected")
	assert.Equal(t, "7", installationIn(t, repo.written["scoped"].Config))
	assert.Equal(t, []string{"acme/app"}, scopeOf(t, repo.written["scoped"].Config).MatchAny["repository"])
	assert.Empty(t, installationIn(t, repo.written["unbound"].Config), "nothing is invented for an unbound activation")

	report, err = r.ReconcileAll(context.Background())
	require.NoError(t, err)
	assert.Zero(t, report.Updated, "a bound projection is current: the binding does not make it look stale")
}

// The same against the real store: after a reconcile pass, a bound declared
// activation still routes only its installation's events, on its owner's
// access.
func TestReconcileKeepsInstallationOnlyRouting(t *testing.T) {
	repo := db.NewTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	user := "u-" + uuid.NewString()[:8]
	project := "p-" + user
	require.NoError(t, repo.CreateProject(ctx, &db.Project{ID: project, UserID: user, Name: "p", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now}))
	issue := "new-issue"
	mk := func(installation string) *core.Trigger {
		cfg := `{"integration":"github","events":["issues.closed"]` // stale: the reconcile rewrites it
		if installation != "" {
			cfg += `,"installation":"` + installation + `"`
		}
		tr := &core.Trigger{
			ID: uuid.NewString(), UserID: user, ProjectID: project, Name: "t-" + uuid.NewString()[:6],
			Kind: core.TriggerKindIntegration, Enabled: true, Workflow: "triage", WorkflowTrigger: &issue,
			Message: "go", DaemonID: "d", Config: json.RawMessage(cfg + `}`), CreatedAt: now, UpdatedAt: now,
		}
		require.NoError(t, repo.CreateTrigger(ctx, tr))
		return tr
	}
	bound, unbound := mk("100"), mk("")
	require.NoError(t, repo.ReplaceIntegrationAccess(ctx, user, "github", "11", now, []core.IntegrationAccessGrant{
		{AccountKey: "100", ResourceKey: "1"}, {AccountKey: "200", ResourceKey: "9"},
	}))

	report, err := NewReconciler(repo, fakeWorkflows{yaml: map[string]string{"triage": declaringWorkflow}}, nil).ReconcileAll(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, report.Updated, "both projections were stale and rewritten")

	got, err := repo.GetTrigger(ctx, bound.ID)
	require.NoError(t, err)
	assert.Equal(t, "100", installationIn(t, got.Config), "the binding survives the rewrite")

	routed := func(account, resource string) []string {
		ts, err := repo.ListAccessRoutedTriggers(ctx, "github", account, resource, now.Add(-time.Minute))
		require.NoError(t, err)
		ids := []string{}
		for _, tr := range ts {
			ids = append(ids, tr.ID)
		}
		return ids
	}
	assert.ElementsMatch(t, []string{bound.ID, unbound.ID}, routed("100", "1"))
	assert.Equal(t, []string{unbound.ID}, routed("200", "9"), "the bound activation still takes only its installation's events")
}

// afterRead is the real store, with a step run once between the reconciler's
// read of every activation and its first write: a concurrent UpdateTrigger
// that commits in that window, deterministically.
type afterRead struct {
	*db.Repo
	step func()
}

func (a *afterRead) ListAllWorkflowTriggerActivations(ctx context.Context) ([]*core.Trigger, error) {
	ts, err := a.Repo.ListAllWorkflowTriggerActivations(ctx)
	if a.step != nil {
		a.step()
		a.step = nil
	}
	return ts, err
}

// A binding committed after the reconciler read its snapshot survives the
// reconciler's write: binding an unbound activation, moving a bound one to
// another installation, and unbinding one. The store keeps the row's binding
// as of the write, never the snapshot's.
func TestReconcileNeverOverwritesABindingCommittedAfterItsRead(t *testing.T) {
	cases := map[string]struct{ before, after string }{
		"bound after the read":   {"", "100"},
		"moved after the read":   {"100", "200"},
		"unbound after the read": {"100", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			repo := db.NewTestRepo(t)
			ctx := context.Background()
			now := time.Now().UTC()
			user := "u-" + uuid.NewString()[:8]
			project := "p-" + user
			require.NoError(t, repo.CreateProject(ctx, &db.Project{ID: project, UserID: user, Name: "p", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now}))
			issue := "new-issue"
			config := func(installation string) json.RawMessage {
				cfg := core.IntegrationConfig{Integration: "github", Events: []string{"issues.closed"}, Installation: installation}
				raw, err := json.Marshal(cfg)
				require.NoError(t, err)
				return raw
			}
			tr := &core.Trigger{
				ID: uuid.NewString(), UserID: user, ProjectID: project, Name: "t", Kind: core.TriggerKindIntegration,
				Enabled: true, Workflow: "triage", WorkflowTrigger: &issue, Message: "go", DaemonID: "d",
				Config: config(tc.before), CreatedAt: now, UpdatedAt: now,
			}
			require.NoError(t, repo.CreateTrigger(ctx, tr))

			store := &afterRead{Repo: repo, step: func() {
				// What UpdateTrigger commits: the row's whole config, rebound.
				_, err := repo.DB.DB(ctx).ExecContext(ctx, `UPDATE triggers SET config = $1 WHERE id = $2`, string(config(tc.after)), tr.ID)
				require.NoError(t, err)
			}}
			report, err := NewReconciler(store, fakeWorkflows{yaml: map[string]string{"triage": declaringWorkflow}}, nil).ReconcileAll(ctx)
			require.NoError(t, err)
			require.Equal(t, 1, report.Updated, "the stale events forced a write from the old snapshot")

			got, err := repo.GetTrigger(ctx, tr.ID)
			require.NoError(t, err)
			assert.Equal(t, tc.after, installationIn(t, got.Config), "the binding committed after the read is the one kept")
			assert.Equal(t, []string{"issues.opened"}, scopeOf(t, got.Config).Events, "the declaration's part is still re-projected")
		})
	}
}
