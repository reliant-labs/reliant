// Copyright (c) 2026 Reliant Labs
package daemonerr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/errclass"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// The UI polls the file tree, previews, directories, processes and topology
// while a user's machine sleeps, starts or wakes; in prod those refusals are
// nearly every reliant-api-server failure (43 in one minute, 2026-11-17). Each
// is the machine's state, not a fault: a user-class Unavailable, logged at
// INFO, with the reason the web waits on. A daemon that answered with a
// failure of its own is still a server error, logged at ERROR.
func TestMachineRefusalsAreUserErrorsAndDaemonFailuresAreNot(t *testing.T) {
	suspended := svcerr.WithMessage(toolexec.ErrDaemonSuspended,
		"the machine for this request is suspended and will wake when you next message it: "+toolexec.ErrDaemonSuspended.Error())
	refusals := map[string]struct {
		err    error
		reason string
	}{
		"not connected (no NATS responder)": {machineStates[2].err, errreason.DaemonNotConnected},
		"still starting":                    {machineStates[1].err, errreason.DaemonStarting},
		"asleep":                            {suspended, errreason.DaemonSuspended},
		"reconnecting (fe2d2455)":           {toolexec.ErrDaemonReconnecting, errreason.DaemonReconnecting},
	}
	for name, tc := range refusals {
		wrapped := fmt.Errorf("daemon command %q: %w", "fs.get_tree", tc.err)
		state, ok := MachineState("", wrapped)
		assert.True(t, ok, name)
		for spelling, got := range map[string]error{
			"MachineState": state,
			"Error":        Error("daemon command fs.get_tree", wrapped),
		} {
			t.Run(name+"/"+spelling, func(t *testing.T) {
				assert.Equal(t, connect.CodeUnavailable, svcerr.Code(got), "the wire code clients retry on")
				assert.Equal(t, tc.reason, svcerr.ToConnect(got).Meta().Get(svcerr.ReasonHeader))
				assert.Equal(t, svcerr.ClassUser, errclass.Classify(got))
				assert.Equal(t, slog.LevelInfo, observe.LevelForErrorContext(context.Background(), got))
			})
		}
	}

	genuine := map[string]error{
		"daemon reported a failure": toolexec.DaemonReportedFailure("fs.get_tree", "walk /src: input/output error"),
		"transport failure":         errors.New("nats: connection closed"),
	}
	for name, err := range genuine {
		t.Run(name, func(t *testing.T) {
			_, ok := MachineState("", err)
			assert.False(t, ok, "not a machine state")
			got := Error("daemon command fs.get_tree", err)
			assert.Equal(t, svcerr.ClassServer, errclass.Classify(got))
			assert.Equal(t, slog.LevelError, observe.LevelForErrorContext(context.Background(), got))
		})
	}
}

// A reconnecting machine is awake: it is a machine state, but not one that
// waking the machine helps, so it is not Unreachable (machinewake keys on it).
func TestReconnectingIsNotUnreachable(t *testing.T) {
	assert.False(t, Unreachable(fmt.Errorf("daemon command %q: %w", "exec.bg_list", toolexec.ErrDaemonReconnecting)))
}
