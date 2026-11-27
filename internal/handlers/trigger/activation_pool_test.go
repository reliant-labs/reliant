package trigger

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/machinepool"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/stretchr/testify/require"
)

// addPool defines one of the owner's pools, name, and labels members into it.
func (e *triggerTestEnv) addPool(t *testing.T, name string, members ...string) *core.MachinePool {
	t.Helper()
	for _, m := range members {
		found, err := store.SetDaemonLabels(e.ctx, e.repo.ORM(), e.userID, m, map[string]string{"pool": name})
		require.NoError(t, err)
		require.True(t, found)
	}
	pool, err := store.PutMachinePool(e.ctx, e.repo.ORM(), &core.MachinePool{UserID: e.userID, Name: name, Selector: machinepool.DefaultSelector(name), MemberIdleTimeout: core.DefaultMemberIdleTimeout}, uuid.NewString(), time.Now().UTC())
	require.NoError(t, err)
	return pool
}

// createOnMachine creates an activation of the nightly declaration pinned to
// the env's machine, approving its creation card.
func (e *triggerTestEnv) createOnMachine(t *testing.T) tools.TriggerSummary {
	t.Helper()
	machine, message := e.daemonID, "Run nightly checks"
	create := tools.ActivationParams{Name: "nightly", Workflow: "triage", WorkflowTrigger: "nightly", ProjectID: e.projectID, MachineID: &machine, Message: &message}
	preview, err := e.svc.PrepareActivation(e.ctx, e.userID, tools.ToolCreateTriggerActivation, create)
	require.NoError(t, err)
	created, err := e.svc.ManageActivation(e.ctx, e.userID, tools.ToolCreateTriggerActivation, create, preview.Hash)
	require.NoError(t, err)
	return created
}

// foreignMemberRepo reports one more pool member than the store does: a
// machine of another user, which store.ListMachinePoolMembers can never
// return, so the guard against it is exercised end to end.
type foreignMemberRepo struct {
	Pools
	foreign string
}

func (r foreignMemberRepo) ListMachinePoolMembers(ctx context.Context, pool *core.MachinePool, projectID string) ([]core.MachinePoolMemberRow, error) {
	rows, err := r.Pools.ListMachinePoolMembers(ctx, pool, projectID)
	return append(rows, core.MachinePoolMemberRow{DaemonID: r.foreign, Name: "theirs"}), err
}

// The activation tools bind an activation to one of the owner's machine
// pools, by name or id. Re-pointing to the owner's own pool does not widen
// the activation's power (its members are the owner's machines), so it needs
// no card and an unattended run may make it; everything else keeps the
// existing rules: a create is new standing power, a move from a pool to one
// machine is a new machine, and a pool with anyone else's machine in it is
// refused outright.
func TestAgentActivationMachinePool(t *testing.T) {
	testkit.Slow(t, "DB-backed activation approval policy")
	unattendedCtx := func(ctx context.Context) context.Context {
		return automationpolicy.WithExecution(ctx, automationpolicy.Execution{Unattended: true})
	}
	pipeline := "pipeline"
	for _, tc := range []struct {
		name string
		run  func(t *testing.T, env *triggerTestEnv, pool *core.MachinePool)
	}{
		{"create with a pool: no machine capability, so only the create's own card (none under approvals: never)", func(t *testing.T, env *triggerTestEnv, pool *core.MachinePool) {
			message := "Run nightly checks"
			create := tools.ActivationParams{Name: "nightly", Workflow: "triage", WorkflowTrigger: "nightly", ProjectID: env.projectID, MachinePool: &pipeline, Message: &message}
			preview, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolCreateTriggerActivation, create)
			require.NoError(t, err)
			require.True(t, preview.CardRequired, "a new activation is new standing power whatever it runs on")
			require.Contains(t, preview.Reason, "A new activation")
			require.Contains(t, preview.Title, "Machine: Machine pool: pipeline (id "+pool.ID+"), 1 member(s)")
			require.Contains(t, preview.Title, "Machine pool id: "+pool.ID+"; members: "+env.daemonID)
			require.NotContains(t, preview.Title, "machine:", "a pool of the owner's machines is no capability item")

			env.setApprovalMode(t, nil, automationpolicy.ApprovalNever)
			preview, err = env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolCreateTriggerActivation, create)
			require.NoError(t, err)
			require.False(t, preview.CardRequired, preview.Reason)
			created, err := env.svc.ManageActivation(unattendedCtx(env.ctx), env.userID, tools.ToolCreateTriggerActivation, create, "")
			require.NoError(t, err)
			require.Equal(t, pool.ID, created.MachinePoolID)
			require.Equal(t, "pipeline", created.MachinePoolName)
			require.Empty(t, created.DaemonID)
			require.Contains(t, created.Placement(), "machine pool=pipeline (id "+pool.ID+"), 1 member(s)")
		}},
		{"update machine -> own pool: no card, unattended too, by name or by id", func(t *testing.T, env *triggerTestEnv, pool *core.MachinePool) {
			created := env.createOnMachine(t)
			for _, p := range []tools.ActivationParams{{TriggerID: created.ID, MachinePool: &pipeline}, {TriggerID: created.ID, MachinePoolID: &pool.ID}} {
				preview, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, p)
				require.NoError(t, err)
				require.False(t, preview.CardRequired, preview.Reason)
			}
			moved, err := env.svc.ManageActivation(unattendedCtx(env.ctx), env.userID, tools.ToolUpdateTriggerActivation, tools.ActivationParams{TriggerID: created.ID, MachinePool: &pipeline}, "")
			require.NoError(t, err)
			require.Equal(t, pool.ID, moved.MachinePoolID)
			stored, err := store.GetTrigger(env.ctx, env.repo.ORM(), created.ID)
			require.NoError(t, err)
			require.Equal(t, pool.ID, stored.MachinePoolID)
			require.Empty(t, stored.DaemonID)
			require.False(t, stored.NoMachine)
			history, err := store.TriggerChanges(env.ctx, env.repo.ORM(), env.userID, created.ID)
			require.NoError(t, err)
			require.Equal(t, "auto:on_capability_change", history[0].Approval)
			require.Empty(t, history[0].CapabilitiesAdded)
			require.Contains(t, history[0].CapabilitiesRemoved, "machine:"+env.daemonID)

			// Editing a pool-bound activation keeps its pool and needs no card.
			edited := "Run the nightly checks, then summarize"
			preview, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, tools.ActivationParams{TriggerID: created.ID, Message: &edited})
			require.NoError(t, err)
			require.False(t, preview.CardRequired, preview.Reason)
			_, err = env.svc.ManageActivation(unattendedCtx(env.ctx), env.userID, tools.ToolUpdateTriggerActivation, tools.ActivationParams{TriggerID: created.ID, Message: &edited}, "")
			require.NoError(t, err)
			stored, err = store.GetTrigger(env.ctx, env.repo.ORM(), created.ID)
			require.NoError(t, err)
			require.Equal(t, pool.ID, stored.MachinePoolID)
		}},
		{"update to an unknown or another user's pool: refused", func(t *testing.T, env *triggerTestEnv, _ *core.MachinePool) {
			created := env.createOnMachine(t)
			other := uuid.NewString()
			_, err := store.PutMachinePool(env.ctx, env.repo.ORM(), &core.MachinePool{UserID: other, Name: "theirs", Selector: machinepool.DefaultSelector("theirs"), MemberIdleTimeout: core.DefaultMemberIdleTimeout}, uuid.NewString(), time.Now().UTC())
			require.NoError(t, err)
			theirs, err := store.GetMachinePoolByName(env.ctx, env.repo.ORM(), other, "theirs")
			require.NoError(t, err)
			unknown, foreignName := "no-such-pool", "theirs"
			for _, p := range []tools.ActivationParams{
				{TriggerID: created.ID, MachinePool: &unknown},
				{TriggerID: created.ID, MachinePool: &foreignName},
				{TriggerID: created.ID, MachinePoolID: &theirs.ID},
			} {
				_, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, p)
				require.Error(t, err)
				require.Equal(t, poolNotFound, tools.ActivationRefusalReason(err))
				_, err = env.svc.ManageActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, p, "")
				require.Error(t, err)
			}
		}},
		{"a pool with machine_id, no_machine, or both pool spellings: refused", func(t *testing.T, env *triggerTestEnv, pool *core.MachinePool) {
			created := env.createOnMachine(t)
			machine, yes := env.daemonID, true
			for _, p := range []tools.ActivationParams{
				{TriggerID: created.ID, MachinePool: &pipeline, MachineID: &machine},
				{TriggerID: created.ID, MachinePoolID: &pool.ID, NoMachine: &yes},
				{TriggerID: created.ID, MachinePool: &pipeline, MachinePoolID: &pool.ID},
			} {
				_, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, p)
				require.Error(t, err)
				require.NotEmpty(t, tools.ActivationRefusalReason(err))
			}
			// Pause, enable and delete take the trigger id alone.
			_, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolPauseTrigger, tools.ActivationParams{TriggerID: created.ID, MachinePool: &pipeline})
			require.ErrorContains(t, err, "pause_trigger only accepts trigger_id")
		}},
		{"pool -> one machine: the existing new-machine rule (a card)", func(t *testing.T, env *triggerTestEnv, _ *core.MachinePool) {
			created := env.createOnMachine(t)
			_, err := env.svc.ManageActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, tools.ActivationParams{TriggerID: created.ID, MachinePool: &pipeline}, "")
			require.NoError(t, err)
			machine := env.daemonID
			back := tools.ActivationParams{TriggerID: created.ID, MachineID: &machine}
			preview, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, back)
			require.NoError(t, err)
			require.True(t, preview.CardRequired)
			require.Contains(t, preview.Reason, "machine:"+machine)
			_, err = env.svc.ManageActivation(unattendedCtx(env.ctx), env.userID, tools.ToolUpdateTriggerActivation, back, preview.Hash)
			require.ErrorContains(t, err, automationpolicy.Refusal())
			got, err := env.svc.ManageActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, back, preview.Hash)
			require.NoError(t, err)
			require.Equal(t, machine, got.DaemonID)
			require.Empty(t, got.MachinePoolID, "a machine replaces the pool")

			// An explicit empty pool unbinds it, and then needs a machine.
			_, err = env.svc.ManageActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, tools.ActivationParams{TriggerID: created.ID, MachinePool: &pipeline}, "")
			require.NoError(t, err)
			empty := ""
			_, err = env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, tools.ActivationParams{TriggerID: created.ID, MachinePool: &empty})
			require.ErrorContains(t, err, "an empty machine_pool unbinds the pool")
			preview, err = env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, tools.ActivationParams{TriggerID: created.ID, MachinePool: &empty, MachineID: &machine})
			require.NoError(t, err)
			require.True(t, preview.CardRequired, "a new machine")
		}},
		{"a pool holding another user's machine: refused, not merely carded", func(t *testing.T, env *triggerTestEnv, _ *core.MachinePool) {
			created := env.createOnMachine(t)
			theirs := uuid.NewString()
			require.NoError(t, env.repo.UpsertDaemon(env.ctx, &db.Daemon{ID: theirs, UserID: uuid.NewString()}))
			env.configure(t, func(d *Deps) { d.Pools = foreignMemberRepo{Pools: d.Pools, foreign: theirs} })
			p := tools.ActivationParams{TriggerID: created.ID, MachinePool: &pipeline}
			_, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, p)
			require.ErrorContains(t, err, "has a member that is not one of your machines")
			_, err = env.svc.ManageActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, p, "anything")
			require.ErrorContains(t, err, "has a member that is not one of your machines")
			stored, err := store.GetTrigger(env.ctx, env.repo.ORM(), created.ID)
			require.NoError(t, err)
			require.Empty(t, stored.MachinePoolID)
		}},
		{"re-point with no card: every member is checked again at write time, not only at preview", func(t *testing.T, env *triggerTestEnv, _ *core.MachinePool) {
			created := env.createOnMachine(t)
			p := tools.ActivationParams{TriggerID: created.ID, MachinePool: &pipeline}
			preview, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, p)
			require.NoError(t, err)
			require.False(t, preview.CardRequired, preview.Reason)
			// Between the preview and the write, the pool gains another
			// user's machine: the write, deciding again inside its
			// transaction, refuses.
			theirs := uuid.NewString()
			require.NoError(t, env.repo.UpsertDaemon(env.ctx, &db.Daemon{ID: theirs, UserID: uuid.NewString()}))
			env.configure(t, func(d *Deps) { d.Pools = foreignMemberRepo{Pools: d.Pools, foreign: theirs} })
			_, err = env.svc.ManageActivation(unattendedCtx(env.ctx), env.userID, tools.ToolUpdateTriggerActivation, p, "")
			require.ErrorContains(t, err, "has a member that is not one of your machines")
			stored, err := store.GetTrigger(env.ctx, env.repo.ORM(), created.ID)
			require.NoError(t, err)
			require.Empty(t, stored.MachinePoolID)
			require.Equal(t, env.daemonID, stored.DaemonID)
		}},
		{"a pool card is bound to the members it showed: a machine labelled in after approval voids it", func(t *testing.T, env *triggerTestEnv, pool *core.MachinePool) {
			created := env.createOnMachine(t)
			env.setApprovalMode(t, nil, automationpolicy.ApprovalAlways)
			p := tools.ActivationParams{TriggerID: created.ID, MachinePool: &pipeline}
			card, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, p)
			require.NoError(t, err)
			require.True(t, card.CardRequired)
			require.Contains(t, card.Title, "1 member(s)")
			other := uuid.NewString()
			require.NoError(t, env.repo.UpsertDaemon(env.ctx, &db.Daemon{ID: other, UserID: env.userID}))
			env.addPool(t, "pipeline", other)
			_, err = env.svc.ManageActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, p, card.Hash)
			require.ErrorContains(t, err, "changed after approval")
			stored, err := store.GetTrigger(env.ctx, env.repo.ORM(), created.ID)
			require.NoError(t, err)
			require.Empty(t, stored.MachinePoolID)

			again, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, p)
			require.NoError(t, err)
			require.Contains(t, again.Title, "2 member(s)")
			got, err := env.svc.ManageActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, p, again.Hash)
			require.NoError(t, err)
			require.Equal(t, pool.ID, got.MachinePoolID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupTriggerTest(t)
			env.saveWorkflow(t, "triage", declaringWorkflowYAML)
			pool := env.addPool(t, "pipeline", env.daemonID)
			tc.run(t, env, pool)
		})
	}
}
