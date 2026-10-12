package toolexec

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// statusRouter answers IsDaemonOnline and records every selector it is asked
// about. Embedding DaemonRouter makes any other call (Wake included) panic,
// which is how the tests prove MachineStatus touches nothing else.
type statusRouter struct {
	DaemonRouter
	online    bool
	onlineErr error
	selectors []*DaemonSelector
}

func (r *statusRouter) IsDaemonOnline(_ context.Context, _ string, sel *DaemonSelector) (bool, error) {
	r.selectors = append(r.selectors, sel)
	return r.online, r.onlineErr
}

// stateStatusRouter is a statusRouter that also exposes the registry view.
type stateStatusRouter struct {
	statusRouter
	state MachineState
	err   error
}

func (r *stateStatusRouter) MachineState(context.Context, string, *DaemonSelector) (MachineState, error) {
	return r.state, r.err
}

func TestRunMachineStatus(t *testing.T) {
	cases := []struct {
		name   string
		router DaemonRouter
		want   tools.MachineStatus
	}{
		{
			name:   "an attached machine is connected, without asking the registry",
			router: &statusRouter{online: true},
			want:   tools.MachineStatus{Connected: true},
		},
		{
			name:   "a machine with no record is removed",
			router: &stateStatusRouter{},
			want:   tools.MachineStatus{Removed: true},
		},
		{
			name: "a failed machine carries the control plane's reason",
			router: &stateStatusRouter{state: MachineState{
				Exists: true, Failed: true, StatusMessage: "tools-daemon exited with code 1"}},
			want: tools.MachineStatus{Failed: true, StatusMessage: "tools-daemon exited with code 1"},
		},
		{
			name:   "a suspended machine is suspended",
			router: &stateStatusRouter{state: MachineState{Exists: true, Suspended: true}},
			want:   tools.MachineStatus{Suspended: true},
		},
		{
			name:   "a starting machine is starting",
			router: &stateStatusRouter{state: MachineState{Exists: true, Starting: true, StatusMessage: "cloning"}},
			want:   tools.MachineStatus{Starting: true, StatusMessage: "cloning"},
		},
		{
			name:   "a lookup failure is not fatal and not removed",
			router: &stateStatusRouter{err: errors.New("db down")},
			want:   tools.MachineStatus{},
		},
		{
			name:   "a router with no registry view reads as not connected",
			router: &statusRouter{},
			want:   tools.MachineStatus{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewRunMachine(tc.router).MachineStatus(context.Background(), "u")
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRunMachineStatus_UsesTheRunsSelector(t *testing.T) {
	router := &statusRouter{online: true}
	sel := &DaemonSelector{ID: "daemon-b"}
	ctx := WithDaemonSelector(context.Background(), sel)

	_, err := NewRunMachine(router).MachineStatus(ctx, "u")
	require.NoError(t, err)
	require.Len(t, router.selectors, 1)
	assert.Equal(t, sel, router.selectors[0])
}

func TestRunMachineStatus_OnlineErrorIsReturned(t *testing.T) {
	router := &statusRouter{onlineErr: errors.New("nats down")}
	_, err := NewRunMachine(router).MachineStatus(context.Background(), "u")
	require.Error(t, err)
}

func TestRunMachineStatus_NilRouter(t *testing.T) {
	_, err := NewRunMachine(nil).MachineStatus(context.Background(), "u")
	require.Error(t, err)
	var nilMachine *RunMachine
	_, err = nilMachine.MachineStatus(context.Background(), "u")
	require.Error(t, err)
}
