package toolexec

import "context"

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
