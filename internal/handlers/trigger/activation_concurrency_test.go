package trigger

import (
	"fmt"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/stretchr/testify/require"
)

// One turn's pause_trigger calls run at once. They must not refuse each
// other: each pauses its own activation, and every one is recorded.
func TestAgentActivationConcurrentPausesAllApply(t *testing.T) {
	testkit.Slow(t, "DB-backed concurrent activation changes")
	env := setupTriggerTest(t)
	env.saveWorkflow(t, "triage", declaringWorkflowYAML)
	env.setApprovalMode(t, nil, automationpolicy.ApprovalNever)
	machine := env.daemonID
	message := "Run nightly checks"
	const n = 4
	ids := make([]string, n)
	for i := range ids {
		create := tools.ActivationParams{Name: fmt.Sprintf("nightly-%d", i), Workflow: "triage", WorkflowTrigger: "nightly", ProjectID: env.projectID, MachineID: &machine, Message: &message}
		created, err := env.svc.ManageActivation(env.ctx, env.userID, tools.ToolCreateTriggerActivation, create, "")
		require.NoError(t, err)
		ids[i] = created.ID
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = env.svc.ManageActivation(env.ctx, env.userID, tools.ToolPauseTrigger, tools.ActivationParams{TriggerID: id}, "")
		}()
	}
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "pause %d", i)
	}
	for _, id := range ids {
		stored, err := env.repo.GetTrigger(env.ctx, id)
		require.NoError(t, err)
		require.False(t, stored.Enabled)
		history, err := env.repo.ListTriggerChanges(env.ctx, env.userID, id)
		require.NoError(t, err)
		require.Equal(t, "pause", history[0].Operation)
		require.Equal(t, "auto:pause", history[0].Approval)
	}
}
