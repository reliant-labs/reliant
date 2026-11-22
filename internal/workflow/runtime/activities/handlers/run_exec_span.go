// Copyright (c) 2026 Reliant Labs
package handlers

import (
	"context"
	"errors"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// runExecEnd is how a run step's command ended, for its run.exec span: what
// execute's branches after the send make of result and err, in the same
// order (machine unavailable, stopped, released, transport, other).
func runExecEnd(ctx context.Context, result CommandResult, err error, reattached bool) telemetry.RunExecEnd {
	end := telemetry.RunExecEnd{Reattached: reattached}
	if err != nil {
		var unavailable *machineUnavailableError
		var transportErr *toolexec.TransportError
		switch {
		case errors.As(err, &unavailable):
			end.Outcome = telemetry.RunOutcomeMachineUnavailable
			if unavailable.Last != nil {
				end.TransportCode = unavailable.Last.Code
			}
		case stoppedByRequest(ctx):
			end.Outcome = telemetry.RunOutcomeCanceled
		case errors.As(err, &transportErr):
			end.Outcome = telemetry.RunOutcomeTransportError
			if attemptReleased(ctx) {
				end.Outcome = telemetry.RunOutcomeReleased
			}
			end.TransportCode = transportErr.Code
		case errors.Is(err, context.DeadlineExceeded):
			end.Outcome = telemetry.RunOutcomeTimeout
		default:
			end.Outcome = telemetry.RunOutcomeError
		}
		return end
	}
	end.Verdict = true
	end.ExitCode, end.Interrupted, end.Truncated = result.ExitCode, result.Interrupted, result.Truncated
	switch result.ExitCode {
	case 0:
		end.Outcome = telemetry.RunOutcomeOK
	case daemon.TimeoutExitCode:
		end.Outcome = telemetry.RunOutcomeTimeout
	default:
		end.Outcome = telemetry.RunOutcomeNonzeroExit
	}
	return end
}
