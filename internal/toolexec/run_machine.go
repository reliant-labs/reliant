package toolexec

import (
	"context"
	"errors"

	"github.com/reliant-labs/reliant/internal/db"
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

// machineHealthReader is the registry view of an attached machine's health.
// NATSDaemonRouter satisfies it; a router that does not leaves a connected
// machine's health unknown, which reads as healthy.
type machineHealthReader interface {
	MachineHealth(ctx context.Context, userID string, selector *DaemonSelector) (db.MachineHealth, error)
}

// dispatchProber confirms that a command sent now would be delivered.
// NATSDaemonRouter satisfies it; a router that does not leaves
// IsDaemonOnline's answer standing.
type dispatchProber interface {
	DispatchReachable(ctx context.Context, userID string, selector *DaemonSelector) bool
}

// notAnsweringMessage is the reason a machine reads as disconnected when its
// registration says attached but the daemon dispatch would reach does not
// answer.
const notAnsweringMessage = "the machine's registration says it is attached, but its daemon does not answer commands yet; its connection may be dropping and re-establishing"

// MachineStatus reports whether the run's machine is attached, and if not,
// why; and, for an attached one, whether its daemon reports it degraded. It
// reads connectivity and the registry only and never wakes anything.
// A registry lookup that fails is not fatal: the machine reads as not
// connected with no reason, so a waiter keeps waiting.
//
// Connected means the run's next machine tool call would reach the daemon,
// not only that the registry holds an attachment: wait_for_machine answers
// "retry now" on it. The two disagreed (reliant#2812): IsDaemonOnline with no
// selector is true while ANY of the user's daemons is up, but a command with
// no selector goes to the user's default machine; and liveness is answered by
// subscriptions other than the ones commands travel on. So an online answer
// is confirmed by DispatchReachable, which resolves the daemon the way every
// send does and probes the subject the gateway's bridge serves beside them.
func (m *RunMachine) MachineStatus(ctx context.Context, userID string) (tools.MachineStatus, error) {
	if m == nil || m.router == nil {
		return tools.MachineStatus{}, errors.New("no daemon router configured")
	}
	selector := DaemonSelectorFromContext(ctx)
	online, err := m.router.IsDaemonOnline(ctx, userID, selector)
	if err != nil {
		return tools.MachineStatus{}, err
	}
	notAnswering := false
	if prober, ok := m.router.(dispatchProber); online && ok && !prober.DispatchReachable(ctx, userID, selector) {
		online, notAnswering = false, true
	}
	if online {
		status := tools.MachineStatus{Connected: true}
		// A failed health read is not fatal: the machine is connected, and
		// reads as healthy, as it did before health was reported.
		if reader, ok := m.router.(machineHealthReader); ok {
			if h, err := reader.MachineHealth(ctx, userID, selector); err == nil && h.State == db.MachineHealthDegraded {
				status.Degraded, status.HealthReason = true, h.Reason
			}
		}
		return status, nil
	}
	status := m.disconnectedStatus(ctx, userID, selector)
	if notAnswering && status.StatusMessage == "" && !status.Removed {
		status.StatusMessage = notAnsweringMessage
	}
	return status, nil
}

// disconnectedStatus is why a machine that is not connected is not: the
// registry's view of it, or no reason when there is none to read.
func (m *RunMachine) disconnectedStatus(ctx context.Context, userID string, selector *DaemonSelector) tools.MachineStatus {
	reader, ok := m.router.(machineStateReader)
	if !ok {
		return tools.MachineStatus{}
	}
	state, err := reader.MachineState(ctx, userID, selector)
	if err != nil {
		return tools.MachineStatus{}
	}
	return tools.MachineStatus{
		Removed:       !state.Exists,
		Failed:        state.Failed,
		Suspended:     state.Suspended,
		Starting:      state.Starting,
		StatusMessage: state.StatusMessage,
	}
}
