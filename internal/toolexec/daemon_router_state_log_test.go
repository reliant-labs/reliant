// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/logging"
)

// A Files tab polling while its machine boots resolved the same "still
// starting" state 63 times a minute, and each resolution wrote a WARN (313 in
// a day from one account). The router now writes one INFO line per user and
// state per window, carrying the count it stands for, and the request's error
// is unchanged.
func TestRoutableDaemonID_MachineStateLinesAreThrottledPerUserAndState(t *testing.T) {
	var out bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	now := time.Unix(1_700_000_000, 0)
	previousLog := routerMachineStateLog
	routerMachineStateLog = logging.NewThrottle(machineStateLogWindow, func() time.Time { return now })
	t.Cleanup(func() { routerMachineStateLog = previousLog })

	for i := 0; i < 63; i++ {
		_, err := routableDaemonID("u1", nil, daemonRecord{}, false, true)
		require.True(t, errors.Is(err, ErrDaemonPending), "the error is the router's, whatever the log does")
		now = now.Add(time.Second / 2)
	}
	assert.Equal(t, 1, strings.Count(out.String(), "not routable yet"), "one line for 63 requests inside the window")
	assert.NotContains(t, out.String(), "level=WARN")

	// Another user, and "no machine at all" for the same user, are different news.
	_, err := routableDaemonID("u2", nil, daemonRecord{}, false, true)
	require.Error(t, err)
	_, err = routableDaemonID("u1", nil, daemonRecord{}, false, false)
	require.True(t, IsNoDaemon(err))
	assert.Equal(t, 2, strings.Count(out.String(), "not routable yet"))
	assert.Equal(t, 1, strings.Count(out.String(), "no daemon could be resolved"))

	out.Reset()
	now = now.Add(machineStateLogWindow)
	_, _ = routableDaemonID("u1", nil, daemonRecord{}, false, true)
	assert.Contains(t, out.String(), "not routable yet")
	assert.Contains(t, out.String(), "suppressed=62", "the next window says how many it held back")
}
