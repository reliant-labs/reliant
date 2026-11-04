// Copyright (c) 2025 Reliant Labs
package types

import "time"

// RunStepMachineWaitBudget is how long a run step keeps a command that never
// reached its machine, waiting for that machine to come back, before the step
// fails. The wait is inside one activity attempt: it spends none of the node's
// retries and never pauses the run.
//
// 30 minutes rides out what a machine blip actually is — a gateway rollout
// (seconds), a daemon restart, a managed machine's VM restarting (the cicd
// machine's kata container on 2026-10-30, a few minutes) — and is short
// enough that an automation whose machine is really gone fails, frees its
// capacity slot and lets the next firing try, instead of holding the slot for
// hours.
const RunStepMachineWaitBudget = 30 * time.Minute

// RunStepSendOverhead bounds what one send of a run step's command costs on
// top of the command's own timeout: the router rides out a short disconnect
// inside each send (toolexec.DaemonReconnectGrace, 90s), and the requester
// waits the command timeout plus 30s for the machine's verdict. The run
// activity's deadline is command timeout + RunStepMachineWaitBudget + this,
// and the wait's clock starts when the FIRST send starts, so a send begun
// anywhere inside the budget — the first included — finishes before the
// deadline with its full command timeout.
const RunStepSendOverhead = 2 * time.Minute

// RunStepMachineUnavailableErrType is the Temporal error type of a run step
// whose command never reached its machine within RunStepMachineWaitBudget, or
// whose machine was removed or failed to start. The command never ran. The
// workflow ends the run with it rather than pausing: the wait has already
// been spent, and a pause would only hold the run (and an automation's
// capacity slot) for a resume nobody may send.
const RunStepMachineUnavailableErrType = "RunStepMachineUnavailable"
