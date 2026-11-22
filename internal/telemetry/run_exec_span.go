// Copyright (c) 2026 Reliant Labs
package telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path"
	"regexp"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// A run node's command is a span of its own under the node's (ai_spans.go):
//
//	workflow.node <node>
//	└ run.exec <node>      the command, from its send to its verdict
//
// Opened worker-side, in the run step's activity, like every span there.

// RunExec is the span of one run node's command: "run.exec <node>".
type RunExec struct {
	span  trace.Span
	start time.Time
	ended bool
}

// RunExecStart describes the command a RunExec runs. Command is read for its
// identity only (AttrRunCommandSHA256, AttrRunCommandBytes,
// AttrRunCommandProgram); its text never reaches the span.
type RunExecStart struct {
	Command string
	// Attempt is the activity attempt, 1 first.
	Attempt int32
	// Credentials is the node's parsed credentials mode ("" is inherit).
	Credentials string
	// InWorktree: the command runs in a branch workspace's checkout.
	InWorktree bool
}

// RunExecEnd is how a RunExec's command ended.
type RunExecEnd struct {
	// Outcome is a RunOutcome* value.
	Outcome string
	// ExitCode is the command's exit status; Verdict says it returned one
	// (a transport failure's synthetic -1 is not).
	ExitCode int
	Verdict  bool
	// Interrupted and Truncated are the verdict's own flags.
	Interrupted bool
	Truncated   bool
	// Reattached: the verdict came from reattaching to a command already
	// sent — a retry's to its previous attempt's, or this attempt's own
	// after its transport dropped.
	Reattached bool
	// TransportCode is the transport failure's code, when one ended it.
	TransportCode string
}

// StartRunExec opens the span for the command of the run node ctx's run scope
// names, carrying the scope's attributes (org.id among them). The returned ctx
// carries it, so the gateway's round trip to the machine is its child. End it
// with End.
func StartRunExec(ctx context.Context, s RunExecStart) (context.Context, *RunExec) {
	scope := RunScopeFromContext(ctx)
	name := SpanNameRunExec
	if scope.NodeID != "" {
		name += " " + scope.NodeID
	}
	credentials := s.Credentials
	if credentials == "" {
		credentials = "inherit"
	}
	sum := sha256.Sum256([]byte(s.Command))
	attrs := append(scope.GenAIAttributes(),
		attribute.String(AttrRunCommandSHA256, hex.EncodeToString(sum[:16])),
		attribute.Int(AttrRunCommandBytes, len(s.Command)),
		attribute.Int64(AttrRunAttempt, int64(s.Attempt)),
		attribute.String(AttrRunCredentials, credentials),
		attribute.Bool(AttrRunWorktree, s.InWorktree),
	)
	if program := runCommandProgram(s.Command); program != "" {
		attrs = append(attrs, attribute.String(AttrRunCommandProgram, program))
	}
	ctx, span := aiTracer().Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attrs...))
	return ctx, &RunExec{span: span, start: time.Now()}
}

// End records how the command ended and ends the span; a second call does
// nothing. A command that ran and exited nonzero (or was stopped by its
// timeout) is the command's verdict, not a failure of the step to run it: it
// is error.type=<outcome> and leaves the status alone, as a tool_error does.
// Only a failure to run the command at all sets the error status. The error
// itself is not recorded: its text can carry the machine's detail.
func (r *RunExec) End(e RunExecEnd) {
	if r == nil || r.ended {
		return
	}
	r.ended = true
	attrs := []attribute.KeyValue{
		attribute.String(AttrRunOutcome, e.Outcome),
		attribute.Int64(AttrRunDurationMs, time.Since(r.start).Milliseconds()),
		attribute.Bool(AttrRunInterrupted, e.Interrupted),
		attribute.Bool(AttrRunTruncated, e.Truncated),
		attribute.Bool(AttrRunReattached, e.Reattached),
	}
	if e.Verdict {
		attrs = append(attrs, attribute.Int(AttrProcessExitCode, e.ExitCode))
	}
	if e.TransportCode != "" {
		attrs = append(attrs, attribute.String(AttrRunTransportCode, e.TransportCode))
	}
	if e.Outcome != RunOutcomeOK {
		attrs = append(attrs, attribute.String(AttrErrorType, e.Outcome))
	}
	r.span.SetAttributes(attrs...)
	switch e.Outcome {
	case RunOutcomeMachineUnavailable, RunOutcomeTransportError, RunOutcomeRetryRefused, RunOutcomeError:
		desc := e.Outcome
		if e.TransportCode != "" {
			desc += " (" + e.TransportCode + ")"
		}
		r.span.SetStatus(codes.Error, desc)
	}
	r.span.End()
}

// runCommandWord is a first word that is a plain path, and runProgramName a
// base name safe to put on a span: a program or script name, nothing an
// expression, a quote or an assignment could have produced.
var (
	runCommandWord = regexp.MustCompile(`^[A-Za-z0-9._/+-]{1,256}$`)
	runProgramName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
)

// runCommandProgram is the base name of command's first word ("bash",
// "./scripts/rc.sh" -> "rc.sh"), or "" when that word is anything but a plain
// path: a variable assignment, an expansion, a quoted or escaped word.
func runCommandProgram(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 || !runCommandWord.MatchString(fields[0]) {
		return ""
	}
	base := path.Base(fields[0])
	if !runProgramName.MatchString(base) {
		return ""
	}
	return base
}
