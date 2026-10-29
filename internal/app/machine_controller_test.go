// Copyright (c) 2025 Reliant Labs
package app

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/stretchr/testify/require"
)

type fakeMachineRegistry struct {
	daemons map[string]*db.Daemon
	event   *core.TriggerEvent
	trigger *core.Trigger
}

func (f *fakeMachineRegistry) ListDaemonsByUserID(_ context.Context, userID string) ([]*db.Daemon, error) {
	var out []*db.Daemon
	for _, d := range f.daemons {
		if d.UserID == userID {
			out = append(out, d)
		}
	}
	return out, nil
}
func (f *fakeMachineRegistry) GetDaemon(_ context.Context, id string) (*db.Daemon, error) {
	if d, ok := f.daemons[id]; ok {
		return d, nil
	}
	return nil, errors.New("daemon not found")
}
func (f *fakeMachineRegistry) GetTriggerEventByChatID(context.Context, string) (*core.TriggerEvent, error) {
	if f.event == nil {
		return nil, core.ErrTriggerEventNotFound
	}
	return f.event, nil
}
func (f *fakeMachineRegistry) GetTrigger(context.Context, string) (*core.Trigger, error) {
	return f.trigger, nil
}

type fakeWaker struct{ woke []string }

func (f *fakeWaker) Wake(_ context.Context, _ string, s *toolexec.DaemonSelector) (toolexec.WakeResult, error) {
	f.woke = append(f.woke, s.ID)
	return toolexec.WakeResult{DaemonID: s.ID, Resumed: true}, nil
}

type fakeMachineActions struct{ calls []string }

func (f *fakeMachineActions) SuspendDaemon(_ context.Context, jwt, id string) error {
	f.calls = append(f.calls, "suspend "+id+" as "+jwt)
	return nil
}
func (f *fakeMachineActions) ResizeDaemon(_ context.Context, jwt, id, size string) error {
	f.calls = append(f.calls, "resize "+id+" "+size+" as "+jwt)
	return nil
}

func TestMachineControllerActsOnlyOnTheUsersMachinesAsTheUser(t *testing.T) {
	size, phase := "xl", "suspended"
	repo := &fakeMachineRegistry{daemons: map[string]*db.Daemon{
		"cicd":   {ID: "cicd", UserID: "machine-owner", Name: "cicd", Size: &size, LifecyclePhase: &phase},
		"others": {ID: "others", UserID: "someone-else", Name: "theirs"},
	}}
	waker, actions := &fakeWaker{}, &fakeMachineActions{}
	m := &machineController{repo: repo, waker: waker, actions: actions}
	ctx := context.Background()

	list, err := m.ListMachines(ctx, "machine-owner")
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "xl", list[0].Size)
	require.Equal(t, "suspended", list[0].State)

	require.Error(t, m.StartMachine(ctx, "machine-owner", "others", false))
	require.Error(t, m.StopMachine(ctx, "machine-owner", "others"))
	require.Empty(t, waker.woke)

	auth.SetUserJWT("machine-owner", "")
	require.ErrorContains(t, m.ResizeMachine(ctx, "machine-owner", "cicd", "2xl"), "no live sign-in")
	auth.SetUserJWT("machine-owner", "owner-session")
	require.NoError(t, m.ResizeMachine(ctx, "machine-owner", "cicd", "2xl"))
	require.NoError(t, m.StopMachine(ctx, "machine-owner", "cicd"))
	require.Equal(t, []string{"resize cicd 2xl as owner-session", "suspend cicd as owner-session"}, actions.calls)
	require.NoError(t, m.StartMachine(ctx, "machine-owner", "cicd", true))
	require.Equal(t, []string{"cicd"}, waker.woke)
}

func TestMachineControllerActivationMachineIsTheUnattendedTriggersMachine(t *testing.T) {
	triggerID := "trigger"
	repo := &fakeMachineRegistry{trigger: &core.Trigger{ID: triggerID, DaemonID: "cicd"}}
	m := &machineController{repo: repo}
	ctx := context.Background()

	got, err := m.ActivationMachine(ctx, "chat")
	require.NoError(t, err)
	require.Empty(t, got, "no launch event: no activation")

	repo.event = &core.TriggerEvent{Kind: core.TriggerEventKindChatStart, TriggerID: &triggerID}
	got, err = m.ActivationMachine(ctx, "chat")
	require.NoError(t, err)
	require.Empty(t, got, "an attended launch is no activation")

	repo.event = &core.TriggerEvent{Kind: core.TriggerEventKindSchedule, TriggerID: &triggerID}
	got, err = m.ActivationMachine(ctx, "chat")
	require.NoError(t, err)
	require.Equal(t, "cicd", got)
}
