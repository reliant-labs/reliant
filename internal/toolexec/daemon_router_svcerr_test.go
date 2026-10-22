// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/daemonoffline"
)

// The machine-state sentinels are svcerr kinds, so a handler's bare
// svcerr.Wrap answers the state's code with its marker — not Internal
// "internal server error", which is what an unclassified error becomes and
// which would erase the marker the web's daemon-wait machinery keys on.
func TestMachineStateSentinelsAreClassified(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code connect.Code
		text string
	}{
		{"pending", ErrDaemonPending, connect.CodeUnavailable, "no daemon connected: daemon record exists but has not registered yet (still starting)"},
		{"suspended", ErrDaemonSuspended, connect.CodeUnavailable, "no daemon connected: the machine is asleep (suspended)"},
		{"no machine", ErrNoDaemon, connect.CodeFailedPrecondition, "no daemon available"},
		{"no responder", errDaemonNotConnected, connect.CodeUnavailable, "no daemon connected for user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.code, svcerr.Code(tc.err))
			assert.Equal(t, svcerr.ClassUser, svcerr.Classify(tc.err))
			assert.Equal(t, tc.text, tc.err.Error(), "the sentinel's text is unchanged")
			wrapped := svcerr.ToConnect(fmt.Errorf("rpc failed: %w", tc.err))
			assert.Equal(t, tc.code, wrapped.Code())
			assert.Equal(t, tc.text, wrapped.Message(), "svcerr publishes the sentinel's text, not the wrapper's")
		})
	}
	assert.True(t, IsDaemonPending(ErrDaemonSuspended), "a suspended machine is a pending one")
	assert.True(t, IsDaemonSuspended(fmt.Errorf("x: %w", ErrDaemonSuspended)))
	assert.False(t, IsDaemonSuspended(ErrDaemonPending))
}

// The router's sentence for the user is what svcerr publishes, with the
// marker in it; Error() reads exactly as it did when the sentence was an
// fmt.Errorf wrapper, and errors.Is still finds the state.
func TestRoutableDaemonID_ClientText(t *testing.T) {
	sel := &DaemonSelector{ID: "d-1"}
	for _, tc := range []struct {
		name   string
		err    func() error
		state  error
		code   connect.Code
		text   string
		marker []string
	}{
		{
			name: "suspended",
			err: func() error {
				_, err := routableDaemonID("u", sel, daemonRecord{id: "d-1", state: recordSuspended}, true, true)
				return err
			},
			state:  ErrDaemonSuspended,
			code:   connect.CodeUnavailable,
			text:   "the machine for this request is suspended and will wake when you next message it: no daemon connected: the machine is asleep (suspended)",
			marker: []string{daemonoffline.ErrorSubstring, "suspended"},
		},
		{
			name:   "starting, selected",
			err:    func() error { _, err := routableDaemonID("u", sel, daemonRecord{}, false, true); return err },
			state:  ErrDaemonPending,
			code:   connect.CodeUnavailable,
			text:   "the machine for this request is still starting: no daemon connected: daemon record exists but has not registered yet (still starting)",
			marker: []string{daemonoffline.ErrorSubstring, "still starting"},
		},
		{
			name:   "starting",
			err:    func() error { _, err := routableDaemonID("u", nil, daemonRecord{}, false, true); return err },
			state:  ErrDaemonPending,
			code:   connect.CodeUnavailable,
			text:   "your machine is still starting: no daemon connected: daemon record exists but has not registered yet (still starting)",
			marker: []string{daemonoffline.ErrorSubstring, "still starting"},
		},
		{
			name:   "no machine, selected",
			err:    func() error { _, err := routableDaemonID("u", sel, daemonRecord{}, false, false); return err },
			state:  ErrNoDaemon,
			code:   connect.CodeFailedPrecondition,
			text:   "no daemon available: the machine this request asked for is not connected",
			marker: []string{"no daemon available"},
		},
		{
			name:   "no machine",
			err:    func() error { _, err := routableDaemonID("u", nil, daemonRecord{}, false, false); return err },
			state:  ErrNoDaemon,
			code:   connect.CodeFailedPrecondition,
			text:   "no daemon available: no machine is connected to your account yet",
			marker: []string{"no daemon available"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.err()
			assert.Equal(t, tc.text, err.Error())
			assert.True(t, errors.Is(err, tc.state))
			// As a handler returns it: wrapped in plumbing, through svcerr.
			got := svcerr.ToConnect(fmt.Errorf("resolving daemon for command: %w", err))
			assert.Equal(t, tc.code, got.Code())
			assert.Equal(t, tc.text, got.Message())
			for _, m := range tc.marker {
				assert.Contains(t, got.Message(), m)
			}
		})
	}
}

// The daemon's own failure text is recoverable by daemonerr; a round-trip
// failure carries none.
func TestDaemonReportedText(t *testing.T) {
	err := fmt.Errorf("push: %w", DaemonReportedFailure("git.push", "! [rejected] main -> main"))
	assert.Equal(t, `push: daemon command "git.push" failed: ! [rejected] main -> main`, err.Error())
	text, ok := DaemonReportedText(err)
	assert.True(t, ok)
	assert.Equal(t, "! [rejected] main -> main", text)
	assert.True(t, errors.Is(err, errDaemonReportedFailure))

	_, ok = DaemonReportedText(fmt.Errorf("daemon command %q failed: %s", "git.push", "gateway round trip failed"))
	assert.False(t, ok)
}
