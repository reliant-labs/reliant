// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// A trigger may now have no machine, but only by saying so: an empty daemon_id
// without no_machine is still the mistake it always was, so an older client
// that omits the field cannot silently create a no-machine automation.
func TestCreateTrigger_EmptyDaemonWithoutNoMachineIsStillRequired(t *testing.T) {
	env := setupTriggerTest(t)
	_, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{
		Trigger: env.definition(func(d *reliantv1.TriggerDefinition) { d.DaemonId = "" }),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestCreateTrigger_NoMachineWithADaemonIsInvalid(t *testing.T) {
	env := setupTriggerTest(t)
	_, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{
		Trigger: env.definition(func(d *reliantv1.TriggerDefinition) { d.NoMachine = true }),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// The default builtin agent is given tag:coding:default — shell, view, edit —
// so a no-machine automation of it is refused at create, with the reason and
// the fix, instead of firing at 9 a.m. into a run that has none of its tools.
func TestCreateTrigger_NoMachineRefusesAWorkflowThatNeedsAMachine(t *testing.T) {
	env := setupTriggerTest(t)
	_, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{
		Trigger: env.definition(func(d *reliantv1.TriggerDefinition) {
			d.DaemonId = ""
			d.NoMachine = true
		}),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "needs a machine")
	assert.Contains(t, err.Error(), "shell", "the error names what needs the machine")
	assert.Contains(t, err.Error(), "Pick a machine", "the error says how to fix it")
	assert.Zero(t, env.backend.syncCount(), "a refused trigger must not touch the schedule backend")

	// A workflow that hard-requires a machine is refused whatever its params.
	_, err = env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{
		Trigger: env.definition(func(d *reliantv1.TriggerDefinition) {
			d.Workflow = "builtin://parallel-compete"
			d.DaemonId = ""
			d.NoMachine = true
		}),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

// The same agent given only web tools runs without a machine, so the trigger is
// accepted, stored with no daemon, and round-trips as no_machine.
func TestCreateTrigger_NoMachineAcceptsAServerOnlyWorkflow(t *testing.T) {
	env := setupTriggerTest(t)
	webOnly, err := structpb.NewValue([]any{"tag:web"})
	require.NoError(t, err)

	got := env.create(t, env.definition(func(d *reliantv1.TriggerDefinition) {
		d.DaemonId = ""
		d.NoMachine = true
		d.Params = map[string]*structpb.Value{"tools": webOnly}
	}))
	assert.True(t, got.GetNoMachine())
	assert.Empty(t, got.GetDaemonId())

	stored, err := env.repo.GetTrigger(context.Background(), got.GetId())
	require.NoError(t, err)
	assert.True(t, stored.NoMachine)
	assert.Empty(t, stored.DaemonID)

	// Switching it back to a machine is an ordinary update.
	updated, err := env.svc.UpdateTrigger(env.ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{
		Id: got.GetId(),
		Trigger: env.definition(func(d *reliantv1.TriggerDefinition) {
			d.Params = map[string]*structpb.Value{"tools": webOnly}
		}),
	}))
	require.NoError(t, err)
	assert.False(t, updated.Msg.GetTrigger().GetNoMachine())
	assert.Equal(t, env.daemonID, updated.Msg.GetTrigger().GetDaemonId())
}

// An update that turns an existing trigger into a no-machine one is checked the
// same way as a create.
func TestUpdateTrigger_NoMachineIsValidatedLikeCreate(t *testing.T) {
	env := setupTriggerTest(t)
	created := env.create(t, env.definition(nil))

	_, err := env.svc.UpdateTrigger(env.ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{
		Id: created.GetId(),
		Trigger: env.definition(func(d *reliantv1.TriggerDefinition) {
			d.DaemonId = ""
			d.NoMachine = true
		}),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}
