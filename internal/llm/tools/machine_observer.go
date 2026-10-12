// Copyright (c) 2025 Reliant Labs
package tools

import "context"

// MachineStatus is what wait_for_machine learns about the run's machine on one
// look. It reads the registry and the transport only: nothing here wakes a
// machine (tool-time traffic never does; see services.machineWake).
type MachineStatus struct {
	// Connected: a daemon the run's selector names is attached right now, so
	// the run's next machine tool call can reach it.
	Connected bool
	// Removed: no machine matches the run's selector any more — it was
	// deleted, or the user never had one. Waiting cannot bring it back.
	Removed bool
	// Failed: the machine is not attached and its lifecycle says it failed to
	// start. Only its owner (or the control plane) can bring it back.
	Failed bool
	// Suspended: the machine is asleep. A run's tools may not wake it, so it
	// comes back only when the user (or their next message) does.
	Suspended bool
	// Starting: provisioning or cloning — on its way up.
	Starting bool
	// StatusMessage is the control plane's latest reason, shown verbatim.
	StatusMessage string
}

// MachineObserver reports the state of the machine a run's tools execute on.
// The run's daemon selector travels on ctx (toolexec.WithDaemonSelector), the
// same way RunMachine reads it, so the answer is about the machine every other
// tool of the run reaches. Implemented by toolexec.RunMachine.
type MachineObserver interface {
	MachineStatus(ctx context.Context, userID string) (MachineStatus, error)
}

// ObservesMachine reports whether this factory can see a remote machine's
// connection state — true exactly where tools reach the machine over the
// network, which is where a run can lose it mid-turn and wait_for_machine is
// worth handing over.
func (f *ToolsFactory) ObservesMachine() bool {
	return f != nil && f.opts != nil && f.opts.MachineObserver != nil
}
