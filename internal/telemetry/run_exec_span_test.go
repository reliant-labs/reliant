// Copyright (c) 2026 Reliant Labs
package telemetry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestRunCommandProgram(t *testing.T) {
	t.Parallel()
	for command, want := range map[string]string{
		"bash -c 'make test'":            "bash",
		"./scripts/rc.sh --cut":          "rc.sh",
		"  \n/usr/bin/env python3 x.py":  "env",
		"task ci\nsecond line":           "task",
		"FOO=bar ./run.sh":               "", // an assignment, not a program
		"$TOOL --flag":                   "", // an expansion
		"\"/opt/my tool/run\" x":         "", // quoted
		"${{ inputs.tool }} build":       "",
		`C:\tools\run.exe`:               "",
		". ./env.sh":                     "",
		"":                               "",
		"a-very-long-name-" + long(80):   "",
		"cd frontends/web && npm ci":     "cd",
		"/home/u/proj/deploy-prod.sh -y": "deploy-prod.sh",
	} {
		assert.Equal(t, want, runCommandProgram(command), "%q", command)
	}
}

func long(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}

// A failure to run the command at all sets the error status; a verdict, even
// a nonzero one, does not. The second End is a no-op.
func TestRunExecEnd(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	for outcome, wantStatus := range map[string]codes.Code{
		RunOutcomeOK:                 codes.Unset,
		RunOutcomeNonzeroExit:        codes.Unset,
		RunOutcomeTimeout:            codes.Unset,
		RunOutcomeCanceled:           codes.Unset,
		RunOutcomeReleased:           codes.Unset,
		RunOutcomeMachineUnavailable: codes.Error,
		RunOutcomeTransportError:     codes.Error,
		RunOutcomeRetryRefused:       codes.Error,
		RunOutcomeError:              codes.Error,
	} {
		rec.Reset()
		_, run := StartRunExec(context.Background(), RunExecStart{Command: "echo hi", Attempt: 1})
		run.End(RunExecEnd{Outcome: outcome})
		run.End(RunExecEnd{Outcome: RunOutcomeError})
		ended := rec.Ended()
		if !assert.Len(t, ended, 1, outcome) {
			continue
		}
		assert.Equal(t, "run.exec", ended[0].Name(), "no node in scope")
		assert.Equal(t, wantStatus, ended[0].Status().Code, outcome)
		attrs := map[string]string{}
		for _, kv := range ended[0].Attributes() {
			attrs[string(kv.Key)] = kv.Value.Emit()
		}
		assert.Equal(t, outcome, attrs[AttrRunOutcome])
		if outcome == RunOutcomeOK {
			assert.NotContains(t, attrs, AttrErrorType)
		} else {
			assert.Equal(t, outcome, attrs[AttrErrorType])
		}
	}
}
