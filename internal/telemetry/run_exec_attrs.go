// Copyright (c) 2026 Reliant Labs
package telemetry

// SpanNameRunExec is the prefix of a run node's command span: "run.exec <id>"
// (StartRunExec), a child of the node's "workflow.node <id>".
const SpanNameRunExec = "run.exec"

// The attributes of a run node's command span (StartRunExec), beside the run
// scope's (RunScope.GenAIAttributes: org.id, the run, the node, the chat as
// gen_ai.conversation.id). They name the command, never carry it: no command
// text, no output, no environment. Like ai_attrs.go's, they are a public
// contract once released: never rename one in place, add the new name beside it.
const (
	// AttrRunCommandSHA256 is the first 16 bytes, hex, of the SHA-256 of the
	// command as the step sent it (its expressions evaluated): the same
	// command hashes the same on every run. AttrRunCommandBytes is its length
	// in bytes, AttrRunCommandProgram the base name of its first word
	// ("bash", "rc.sh"), left out when that word is not a plain program name.
	AttrRunCommandSHA256  = "reliant.run.command.sha256"
	AttrRunCommandBytes   = "reliant.run.command.bytes"
	AttrRunCommandProgram = "reliant.run.command.program"
	// AttrProcessExitCode is the command's exit status (OpenTelemetry's
	// process semantic conventions), set only when the command returned one.
	AttrProcessExitCode = "process.exit.code"
	// AttrRunOutcome is how the command ended: a RunOutcome* value.
	AttrRunOutcome = "reliant.run.outcome"
	// AttrRunDurationMs is the span's own wall time, in milliseconds.
	AttrRunDurationMs = "reliant.run.duration_ms"
	// AttrRunInterrupted and AttrRunTruncated report a command that was
	// interrupted, and output its machine cut to its cap.
	AttrRunInterrupted = "reliant.run.interrupted"
	AttrRunTruncated   = "reliant.run.truncated"
	// AttrRunAttempt is the activity attempt (1 first). AttrRunReattached
	// reports a verdict read by reattaching to a command already sent: by a
	// retry (attempt > 1) to the command its previous attempt started, or by
	// this attempt after its transport dropped (attempt 1).
	AttrRunAttempt    = "reliant.run.attempt"
	AttrRunReattached = "reliant.run.reattached"
	// AttrRunCredentials is the node's credentials mode: "inherit" or "none".
	AttrRunCredentials = "reliant.run.credentials" //nolint:gosec // G101: an attribute's name, not a credential.
	// AttrRunWorktree reports a command run in a branch workspace's checkout.
	AttrRunWorktree = "reliant.run.worktree"
	// AttrRunTransportCode is the transport failure's code (toolexec's
	// ErrorCode*) when no verdict came back.
	AttrRunTransportCode = "reliant.run.transport.code"
)

// Values of AttrRunOutcome. Every one but RunOutcomeOK is also the span's
// error.type.
const (
	RunOutcomeOK          = "ok"
	RunOutcomeNonzeroExit = "nonzero_exit"
	// RunOutcomeTimeout: the command was stopped by its timeout.
	RunOutcomeTimeout = "timeout"
	// RunOutcomeMachineUnavailable: the command never reached its machine.
	RunOutcomeMachineUnavailable = "machine_unavailable"
	// RunOutcomeTransportError: no result came back; it may have run.
	RunOutcomeTransportError = "transport_error"
	// RunOutcomeReleased: the attempt was released (its worker stopped) while
	// the command ran; the retry reattaches to it.
	RunOutcomeReleased = "released"
	// RunOutcomeCanceled: the step was told to stop (a cancel, a pause).
	RunOutcomeCanceled = ErrorTypeCanceled
	// RunOutcomeRetryRefused: a retry that could not reattach, and refused
	// to run the command again.
	RunOutcomeRetryRefused = "retry_refused"
	// RunOutcomeError: any other failure to run it.
	RunOutcomeError = "error"
)
