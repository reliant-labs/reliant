// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// Every path that turns a definition into a trigger row must bind the
// definition's org GitHub App installation (bindOrgGitHubApp): one that does
// not drops the binding, and the trigger silently routes on its owner's whole
// access instead of the one installation. This pins the structure: every
// function that calls triggerFromDefinition also calls bindOrgGitHubApp after
// it. A new path that converts a definition and skips the bind fails here;
// route it through activationRow, or bind it as CreateTrigger does.
func TestEveryDefinitionToRowPathBindsTheOrgInstallation(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	var callers []string
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		require.NoError(t, err)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name == "triggerFromDefinition" {
				continue
			}
			var convert, bind token.Pos
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "triggerFromDefinition":
					if !convert.IsValid() {
						convert = call.Pos()
					}
				case "bindOrgGitHubApp":
					bind = call.Pos()
				}
				return true
			})
			if !convert.IsValid() {
				continue
			}
			name := path + ":" + fn.Name.Name
			callers = append(callers, name)
			if !bind.IsValid() || bind < convert {
				t.Errorf("%s turns a definition into a trigger row (triggerFromDefinition, %s) without binding its org GitHub App installation after it (bindOrgGitHubApp): route it through activationRow",
					name, fset.Position(convert))
			}
		}
	}
	sort.Strings(callers)
	require.NotEmpty(t, callers, "the scan found no triggerFromDefinition call: it stopped seeing them")
	require.Contains(t, callers, "trigger.go:CreateTrigger")
	require.Contains(t, callers, "trigger.go:UpdateTrigger")
	require.Contains(t, callers, "trigger_org_github.go:activationRow")
}

// The other half of the class: a path that copies a row's connection into a
// new definition copies ConnectionID alone, which drops an installation
// binding (it has none). Such a copy goes through effectiveConnectionID.
func TestNoPathCarriesARowsConnectionWithoutItsInstallation(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	// The names this package gives a *core.Trigger (a row), not request
	// params such as tools.ActivationParams.ConnectionID.
	copyOfRow := regexp.MustCompile(`ConnectionId\s*=\s*&?(existing|row|trigger|updated|adopted|prev|stored|current|t)\.ConnectionID\b`)
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		require.NoError(t, err)
		for _, m := range copyOfRow.FindAll(src, -1) {
			t.Errorf("%s: %q copies a row's saved connection into a definition and drops an org GitHub App installation binding: use effectiveConnectionID", path, m)
		}
	}
}

const orgGitHubWorkflowYAML = `name: gh
triggers:
  - name: issues
    integration: {integration: github, events: [issues.opened]}
    prompt: "Triage."
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

const orgInstallation = "controlplane:github-app:4242"

// orgGitHubEnv is a deployment where acme has linked installation 4242,
// GitHub triggers route on access, and the binding gate is as given; the
// caller works in a project of acme.
func orgGitHubEnv(t *testing.T, gate *configv1.OrgsConfig) (*triggerTestEnv, string) {
	t.Helper()
	env := setupTriggerTest(t).withOrgs(t)
	env.configure(t, func(d *Deps) {
		d.Integrations = &configv1.IntegrationsConfig{PublicUrl: "https://api.example.com/"}
		d.Sealer = fakeSealer{}
		d.Catalog = fakeCatalog{"github": true}
		d.Intake = &fakeIntake{}
		d.Access = map[string]IntegrationAccess{"github": &fakeAccess{}}
		d.OrgGitHubApps = linkedApps()
		d.OrgRollout = gate
	})
	acme := "acme"
	project := env.orgProject(t, &acme)
	env.saveWorkflow(t, "gh", orgGitHubWorkflowYAML)
	return env, project
}

func (e *triggerTestEnv) onlyTrigger(t *testing.T) *core.Trigger {
	t.Helper()
	stored := e.userTriggers(t)
	require.Len(t, stored, 1)
	return stored[0]
}

func TestEnableWorkflow_BindsTheNamedOrgInstallationAndKeepsItOnReapply(t *testing.T) {
	testkit.Slow(t, "a DB-backed trigger handler test with many round trips to pgtest's Postgres")
	t.Parallel()
	env, project := orgGitHubEnv(t, gateOn)
	req := func(mutate func(*reliantv1.EnableWorkflowRequest)) *reliantv1.EnableWorkflowRequest {
		r := &reliantv1.EnableWorkflowRequest{Workflow: "gh", ProjectId: project, DaemonId: env.daemonID,
			ActingIdentity: reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER}
		mutate(r)
		return r
	}

	resp := env.enable(t, req(func(r *reliantv1.EnableWorkflowRequest) {
		r.ConnectionIds = map[string]string{"github": orgInstallation}
	}))
	require.True(t, resp.Enabled, "%v", resp.Results)
	assert.Equal(t, orgInstallation, resp.Results[0].Activation.GetConnectionId())
	assert.Equal(t, "4242", installationOf(t, env.onlyTrigger(t)))

	// Re-applying with a changed setting and no connection named keeps the
	// installation the activation listens through. It used to drop it: the
	// carried connection was the row's ConnectionID, which a binding has none of.
	resp = env.enable(t, req(func(r *reliantv1.EnableWorkflowRequest) { r.NotifyOnComplete = true }))
	require.True(t, resp.Enabled, "%v", resp.Results)
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_UPDATED, resp.Results[0].Outcome)
	assert.Equal(t, orgInstallation, resp.Results[0].Activation.GetConnectionId())
	assert.Equal(t, "4242", installationOf(t, env.onlyTrigger(t)), "the re-apply kept the binding")

	// Naming an installation the org has not linked refuses the re-apply
	// before anything is written.
	resp = env.enable(t, req(func(r *reliantv1.EnableWorkflowRequest) {
		r.NotifyOnComplete = false
		r.ConnectionIds = map[string]string{"github": "controlplane:github-app:999"}
	}))
	require.False(t, resp.Enabled)
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_FAILED, resp.Results[0].Outcome)
	assert.Equal(t, "4242", installationOf(t, env.onlyTrigger(t)))
}

// With the rollout gate off, enabling a workflow that names an org
// installation is refused in the validation phase: nothing is written, so no
// trigger is bound (or created unbound).
func TestEnableWorkflow_RefusesAnOrgInstallationWhileTheGateIsOff(t *testing.T) {
	testkit.Slow(t, "a DB-backed trigger handler test with many round trips to pgtest's Postgres")
	t.Parallel()
	env, project := orgGitHubEnv(t, nil)

	resp := env.enable(t, &reliantv1.EnableWorkflowRequest{
		Workflow: "gh", ProjectId: project, DaemonId: env.daemonID,
		ActingIdentity: reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER,
		ConnectionIds:  map[string]string{"github": orgInstallation},
	})
	require.False(t, resp.Enabled)
	assert.Equal(t, reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_FAILED, resp.Results[0].Outcome)
	assert.Contains(t, resp.Results[0].Message, "not enabled on this deployment")
	assert.Empty(t, env.userTriggers(t), "nothing was written")
}

func TestAgentActivation_BindsTheNamedOrgInstallationAndKeepsItOnUpdate(t *testing.T) {
	testkit.Slow(t, "DB-backed activation approval and apply")
	env, project := orgGitHubEnv(t, gateOn)
	ctx := automationpolicy.WithActor(env.ctx, automationpolicy.Actor{ChatID: "caller", RunID: "thread", ToolCallID: "call"})
	conn, owner := orgInstallation, "owner"
	p := tools.ActivationParams{ActingIdentity: &owner, Name: "agent issues", Workflow: "gh", WorkflowTrigger: "issues", ProjectID: project, MachineID: &env.daemonID, ConnectionID: &conn}

	preview, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolCreateTriggerActivation, p)
	require.NoError(t, err)
	created, err := env.svc.ManageActivation(ctx, env.userID, tools.ToolCreateTriggerActivation, p, preview.Hash)
	require.NoError(t, err)
	// It used to be applied unbound: the preview replaced the definition's
	// connection with the row's ConnectionID, which a binding has none of.
	assert.Equal(t, orgInstallation, created.ConnectionID)
	assert.Equal(t, "4242", installationOf(t, env.onlyTrigger(t)))

	message := "Triage, carefully."
	edit := tools.ActivationParams{TriggerID: created.ID, Message: &message}
	preview, err = env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, edit)
	require.NoError(t, err)
	_, err = env.svc.ManageActivation(ctx, env.userID, tools.ToolUpdateTriggerActivation, edit, preview.Hash)
	require.NoError(t, err)
	assert.Equal(t, "4242", installationOf(t, env.onlyTrigger(t)), "an edit that names no connection keeps the binding")
}

// With the gate off, the agent's activation is refused at the preview, before
// any approval card or write.
func TestAgentActivation_RefusesAnOrgInstallationWhileTheGateIsOff(t *testing.T) {
	testkit.Slow(t, "DB-backed activation preview")
	env, project := orgGitHubEnv(t, nil)
	conn, owner := orgInstallation, "owner"
	_, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolCreateTriggerActivation,
		tools.ActivationParams{ActingIdentity: &owner, Name: "agent issues", Workflow: "gh", WorkflowTrigger: "issues", ProjectID: project, MachineID: &env.daemonID, ConnectionID: &conn})
	require.Error(t, err)
	assert.Empty(t, env.userTriggers(t))
}
