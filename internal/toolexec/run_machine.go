package toolexec

import (
	"context"
	"errors"

	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// RunMachine sends daemon commands to the machine a run's tools execute on.
//
// A server-side tool receives the run's daemon selector on its context
// (executeOnServer sets it with WithDaemonSelector), the same selector
// ExecuteTools routes daemon-placed tools by. RunMachine resolves it to one
// daemon id, so a tool that sends several commands AND records where they
// ran (the worktree tool records a worktree's owner) sends them all to the
// machine it records.
type RunMachine struct {
	router DaemonRouter
}

// NewRunMachine returns a RunMachine over router.
func NewRunMachine(router DaemonRouter) *RunMachine {
	return &RunMachine{router: router}
}

// DaemonID returns the daemon the run's tools execute on: the selector on
// ctx, or default resolution when the run carries none.
func (m *RunMachine) DaemonID(ctx context.Context, userID string) (string, error) {
	return ResolveDaemonIDForSelector(ctx, m.router, userID, DaemonSelectorFromContext(ctx))
}

// Send delivers one command to daemonID and returns its reply.
func (m *RunMachine) Send(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return m.router.SendDaemonCommandToDaemon(ctx, userID, daemonID, commandType, payload, timeoutMs)
}

var _ tools.MachineObserver = (*RunMachine)(nil)

// machineStateReader is the registry view MachineStatus needs beyond
// connectivity. NATSDaemonRouter satisfies it; a router that does not simply
// leaves a disconnected machine's reason unknown.
type machineStateReader interface {
	MachineState(ctx context.Context, userID string, selector *DaemonSelector) (MachineState, error)
}

// MachineStatus reports whether the run's machine is attached, and if not,
// why. It reads connectivity and the registry only and never wakes anything.
// A registry lookup that fails is not fatal: the machine reads as not
// connected with no reason, so a waiter keeps waiting.
func (m *RunMachine) MachineStatus(ctx context.Context, userID string) (tools.MachineStatus, error) {
	if m == nil || m.router == nil {
		return tools.MachineStatus{}, errors.New("no daemon router configured")
	}
	selector := DaemonSelectorFromContext(ctx)
	online, err := m.router.IsDaemonOnline(ctx, userID, selector)
	if err != nil {
		return tools.MachineStatus{}, err
	}
	if online {
		return tools.MachineStatus{Connected: true}, nil
	}
	reader, ok := m.router.(machineStateReader)
	if !ok {
		return tools.MachineStatus{}, nil
	}
	state, err := reader.MachineState(ctx, userID, selector)
	if err != nil {
		return tools.MachineStatus{}, nil
	}
	return tools.MachineStatus{
		Removed:       !state.Exists,
		Failed:        state.Failed,
		Suspended:     state.Suspended,
		Starting:      state.Starting,
		StatusMessage: state.StatusMessage,
	}, nil
}
