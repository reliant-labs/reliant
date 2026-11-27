package core

// The merge queue's vocabularies (db/migrations/20261126160301_pipeline_queue.up.sql). Each All*
// set is mirrored by a CHECK that declares it with forge:closed-set, so
// `forge lint --closed-sets` fails the moment the two drift: a value added
// here needs a migration widening its CHECK.

// QueueEntryState is where an approved PR head is in the merge queue.
type QueueEntryState string

const (
	QueueEntryWaiting QueueEntryState = "waiting"
	QueueEntryBatched QueueEntryState = "batched"
	QueueEntryLanded  QueueEntryState = "landed"
	QueueEntryEjected QueueEntryState = "ejected"
	// QueueEntryRemoved left the queue without landing or being ejected
	// (withdrawn).
	QueueEntryRemoved QueueEntryState = "removed"
)

// AllQueueEntryStates is every QueueEntryState (queue_entries_state).
var AllQueueEntryStates = []QueueEntryState{
	QueueEntryWaiting,
	QueueEntryBatched,
	QueueEntryLanded,
	QueueEntryEjected,
	QueueEntryRemoved,
}

// QueueEjectReason is why an entry was ejected from the queue.
type QueueEjectReason string

const (
	QueueEjectConflict  QueueEjectReason = "conflict"
	QueueEjectGateRed   QueueEjectReason = "gate_red"
	QueueEjectHeadMoved QueueEjectReason = "head_moved"
	QueueEjectHold      QueueEjectReason = "hold"
	QueueEjectClosed    QueueEjectReason = "closed"
)

// AllQueueEjectReasons is every QueueEjectReason
// (queue_entries_eject_reason).
var AllQueueEjectReasons = []QueueEjectReason{
	QueueEjectConflict,
	QueueEjectGateRed,
	QueueEjectHeadMoved,
	QueueEjectHold,
	QueueEjectClosed,
}

// QueueBatchKind is why a batch was built.
type QueueBatchKind string

const (
	QueueBatchNormal QueueBatchKind = "normal"
	// QueueBatchBisect is a half of a red batch, gated on its own.
	QueueBatchBisect QueueBatchKind = "bisect"
	// QueueBatchRerun regates the same tree once.
	QueueBatchRerun  QueueBatchKind = "rerun"
	QueueBatchRevert QueueBatchKind = "revert"
	// QueueBatchVerify gates main's own tree after a landing mismatch.
	QueueBatchVerify          QueueBatchKind = "verify"
	QueueBatchEmergency       QueueBatchKind = "emergency"
	QueueBatchEmergencyVerify QueueBatchKind = "emergency_verify"
)

// AllQueueBatchKinds is every QueueBatchKind (queue_batches_kind).
var AllQueueBatchKinds = []QueueBatchKind{
	QueueBatchNormal,
	QueueBatchBisect,
	QueueBatchRerun,
	QueueBatchRevert,
	QueueBatchVerify,
	QueueBatchEmergency,
	QueueBatchEmergencyVerify,
}

// QueueBatchState is where a batch is: building → gating → passed → landed,
// or failed, unproven or abandoned.
type QueueBatchState string

const (
	QueueBatchBuilding  QueueBatchState = "building"
	QueueBatchGating    QueueBatchState = "gating"
	QueueBatchPassed    QueueBatchState = "passed"
	QueueBatchFailed    QueueBatchState = "failed"
	QueueBatchUnproven  QueueBatchState = "unproven"
	QueueBatchLanded    QueueBatchState = "landed"
	QueueBatchAbandoned QueueBatchState = "abandoned"
)

// AllQueueBatchStates is every QueueBatchState (queue_batches_state).
var AllQueueBatchStates = []QueueBatchState{
	QueueBatchBuilding,
	QueueBatchGating,
	QueueBatchPassed,
	QueueBatchFailed,
	QueueBatchUnproven,
	QueueBatchLanded,
	QueueBatchAbandoned,
}

// QueueIsolationBackend is the boundary a batch's gate ran in.
type QueueIsolationBackend string

const (
	// QueueIsolationVM is a per-gate microVM.
	QueueIsolationVM QueueIsolationBackend = "vm"
	// QueueIsolationSysbox is a sysbox-runc container.
	QueueIsolationSysbox QueueIsolationBackend = "sysbox"
	// QueueIsolationChecks ran nothing on a gate machine: GitHub check runs
	// judged the candidate.
	QueueIsolationChecks QueueIsolationBackend = "checks"
	// QueueIsolationRunc is a plain runc container: NOT an isolation
	// boundary. Shadow mode only; a batch gated on it never lands
	// (queue_batches_runc_never_lands).
	QueueIsolationRunc QueueIsolationBackend = "runc"
)

// AllQueueIsolationBackends is every QueueIsolationBackend
// (queue_batches_isolation_backend).
var AllQueueIsolationBackends = []QueueIsolationBackend{
	QueueIsolationVM,
	QueueIsolationSysbox,
	QueueIsolationChecks,
	QueueIsolationRunc,
}

// QueueGateRunner is how a batch's gate was dispatched (owner D6).
type QueueGateRunner string

const (
	// QueueGateRunnerMachine is an engine run on a member of the gate pool.
	QueueGateRunnerMachine QueueGateRunner = "machine"
	// QueueGateRunnerChecks waits for named GitHub check runs.
	QueueGateRunnerChecks QueueGateRunner = "checks"
	// QueueGateRunnerBoth needs both to pass.
	QueueGateRunnerBoth QueueGateRunner = "both"
)

// AllQueueGateRunners is every QueueGateRunner (queue_batches_gate_runner).
var AllQueueGateRunners = []QueueGateRunner{
	QueueGateRunnerMachine,
	QueueGateRunnerChecks,
	QueueGateRunnerBoth,
}

// MainHealthSource is what judged a sha of main.
type MainHealthSource string

const (
	MainHealthGate    MainHealthSource = "gate"
	MainHealthRecheck MainHealthSource = "recheck"
	MainHealthRelease MainHealthSource = "release"
	MainHealthNightly MainHealthSource = "nightly"
)

// AllMainHealthSources is every MainHealthSource (main_health_source).
var AllMainHealthSources = []MainHealthSource{
	MainHealthGate,
	MainHealthRecheck,
	MainHealthRelease,
	MainHealthNightly,
}

// MainHealthVerdict is a verdict on a sha of main.
type MainHealthVerdict string

const (
	MainHealthPass     MainHealthVerdict = "pass"
	MainHealthFail     MainHealthVerdict = "fail"
	MainHealthUnproven MainHealthVerdict = "unproven"
)

// AllMainHealthVerdicts is every MainHealthVerdict (main_health_verdict).
var AllMainHealthVerdicts = []MainHealthVerdict{
	MainHealthPass,
	MainHealthFail,
	MainHealthUnproven,
}
