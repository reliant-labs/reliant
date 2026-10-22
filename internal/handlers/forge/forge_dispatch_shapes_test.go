// Copyright (c) 2025 Reliant Labs
package forge

import (
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/nats-io/nats.go"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// A dispatch failure reaches forge in two shapes: daemoncmd's today
// (daemonerr.DispatchConnectError, the router's text in the message) and
// daemonerr.Error's (the router's text withheld, the daemon's kept). A timed-out
// cluster read must read as UNREACHABLE in both, so it is recognised by the
// router's sentinel and the dispatch's code, never by "nats: timeout" — text
// daemonerr.Error removes.
func TestIsForgeClusterReadTimeout_AcrossDispatchShapes(t *testing.T) {
	natsTimeout := fmt.Errorf("daemon command forge.env_verify via NATS failed: %w: %w", toolexec.ErrDaemonTimeout, nats.ErrTimeout)
	forgeTimedOut := toolexec.DaemonReportedFailure("forge.env_verify", "forge command timed out: run forge: context deadline exceeded")
	forgeFailed := toolexec.DaemonReportedFailure("forge.env_verify", "forge command failed: exit 1: timeout reading config")
	missingDir := toolexec.DaemonReportedFailure("forge.env_verify", "forge project dir does not exist: /gone")

	shapes := map[string]func(error) error{
		"DispatchConnectError": func(err error) error { return daemonerr.DispatchConnectError("forge.env_verify", err) },
		"daemonerr.Error":      func(err error) error { return daemonerr.Error("daemon command forge.env_verify", err) },
	}
	for shape, dispatch := range shapes {
		t.Run(shape, func(t *testing.T) {
			assert.True(t, isForgeClusterReadTimeout(dispatch(natsTimeout)), "the machine did not answer in time")
			assert.True(t, isForgeClusterReadTimeout(dispatch(forgeTimedOut)), "forge ran out of its budget")
			assert.False(t, isForgeClusterReadTimeout(dispatch(forgeFailed)), "a substantive failure is not laundered into unknown")
			assert.False(t, isForgeClusterReadTimeout(dispatch(missingDir)))

			// Off a cluster read, the same failures keep their classes.
			assert.Equal(t, connect.CodeDeadlineExceeded, svcerr.Code(remapForgeDispatchError("forge.env_verify", dispatch(natsTimeout))))
			assert.Equal(t, connect.CodeDeadlineExceeded, svcerr.Code(remapForgeDispatchError("forge.env_verify", dispatch(forgeTimedOut))))
			failed := remapForgeDispatchError("forge.env_verify", dispatch(forgeFailed))
			assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(failed))
			assert.Contains(t, svcerr.ToConnect(failed).Message(), "forge command failed: exit 1: timeout reading config",
				"the daemon's words are the message")
			assert.Equal(t, connect.CodeNotFound, svcerr.Code(remapForgeDispatchError("forge.env_verify", dispatch(missingDir))))
		})
	}
}
