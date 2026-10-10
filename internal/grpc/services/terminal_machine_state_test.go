// Copyright (c) 2025 Reliant Labs
package services

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/toolexec"
)

// The terminal's expected states — no machine, starting or asleep, not
// connected — are told apart from real failures, which stay at ERROR.
func TestTerminalMachineState(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		state string
		ok    bool
	}{
		{"no machine at all", fmt.Errorf("resolving daemon for command: %w: no machine is connected to your account yet", toolexec.ErrNoDaemon), "no_machine", true},
		{"starting", fmt.Errorf("resolving daemon for command: your machine is still starting: %w", toolexec.ErrDaemonPending), "starting_or_asleep", true},
		{"suspended", fmt.Errorf("the machine for this request is suspended and will wake when you next message it: %w", toolexec.ErrDaemonPending), "starting_or_asleep", true},
		{"not connected", connect.NewError(connect.CodeUnavailable, errors.New("no daemon connected for user")), "not_connected", true},
		{"a real failure", errors.New("terminal.create via NATS failed: nats: timeout"), "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, ok := terminalMachineState(tc.err)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.state, state)
		})
	}
}
