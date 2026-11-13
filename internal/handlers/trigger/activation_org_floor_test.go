// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/orgs"
)

var _ OrgResolver = flakyOrgs{}

// flakyOrgs is an org resolver whose default-org lookup fails: the moment an
// approval is evaluated may see that while the write a moment later does not.
type flakyOrgs struct{ defaultErr error }

func (flakyOrgs) Named(context.Context, string, string) (string, error) { return "", nil }
func (f flakyOrgs) DefaultOrg(context.Context, string) (string, error) {
	return "", f.defaultErr
}
func (flakyOrgs) LiveMembersFresh(context.Context, string, string) (int, bool, error) {
	return 0, false, nil
}
func (flakyOrgs) ActingOrg(context.Context, string, string) (string, error) { return "", nil }
func (flakyOrgs) ListScope(context.Context, string, string, orgs.Backfill) (*core.OrgScope, error) {
	return nil, nil
}

// A new activation's org is the one its write will decide. When it cannot be
// resolved while the approval is evaluated, the activation is refused, never
// evaluated with no org floor: a later write could still land it in an org
// that enforces one, without the card the floor demands.
func TestActivationOrgFloor_UnresolvableOrgRefusesTheActivation(t *testing.T) {
	env := setupTriggerTest(t)
	env.svc.deps.Orgs = flakyOrgs{defaultErr: errors.New("control plane unreachable")}
	env.saveWorkflow(t, "triage", declaringWorkflowYAML)
	machine := env.daemonID
	message := "Run nightly checks"
	create := tools.ActivationParams{Name: "nightly", Workflow: "triage", WorkflowTrigger: "nightly", ProjectID: env.projectID, MachineID: &machine, Message: &message}

	_, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolCreateTriggerActivation, create)
	require.ErrorContains(t, err, "organization could not be resolved")

	// With an org resolvable (none: a deployment without orgs), the same
	// activation is evaluated as before.
	env.svc.deps.Orgs = nil
	_, err = env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolCreateTriggerActivation, create)
	require.NoError(t, err)
}
