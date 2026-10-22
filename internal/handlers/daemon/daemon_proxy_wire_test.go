// Copyright (c) 2025 Reliant Labs
package daemon

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/nats-io/nats.go"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// What a client reads, over a real Connect client and the svcerr interceptor,
// when a daemon command fails. The markers are the web's contract
// (frontends/web/src/lib/daemon-errors.ts) and stay byte for byte: "no daemon connected"
// with "still starting" / "suspended" means wait, "no daemon available" means
// connect a machine. The daemon's own words reach the user; the router's and
// the transport's do not.
func TestOAuthHelper_OnTheWire(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		wantCode    connect.Code
		wantMessage string
	}{
		{
			name:        "machine still starting",
			err:         svcerr.WithMessage(toolexec.ErrDaemonPending, "your machine is still starting: "+toolexec.ErrDaemonPending.Error()),
			wantCode:    connect.CodeUnavailable,
			wantMessage: "open OAuth helper: your machine is still starting: no daemon connected: daemon record exists but has not registered yet (still starting)",
		},
		{
			name:        "machine asleep",
			err:         toolexec.ErrDaemonSuspended,
			wantCode:    connect.CodeUnavailable,
			wantMessage: "open OAuth helper: no daemon connected: the machine is asleep (suspended)",
		},
		{
			name:        "no NATS responder",
			err:         connect.NewError(connect.CodeUnavailable, svcerr.WithClass(svcerr.Unavailable("no daemon connected for user"), svcerr.ClassUser)),
			wantCode:    connect.CodeUnavailable,
			wantMessage: "open OAuth helper: no daemon connected for user",
		},
		{
			name:        "no machine at all",
			err:         svcerr.WithMessage(toolexec.ErrNoDaemon, "no daemon available: no machine is connected to your account yet"),
			wantCode:    connect.CodeFailedPrecondition,
			wantMessage: "open OAuth helper: no daemon available: no machine is connected to your account yet",
		},
		{
			name:        "machine too busy to answer",
			err:         fmt.Errorf("daemon.command via NATS failed: %w: %w", toolexec.ErrDaemonTimeout, nats.ErrTimeout),
			wantCode:    connect.CodeDeadlineExceeded,
			wantMessage: "open OAuth helper: the machine did not answer in time",
		},
		{
			name:        "the daemon's own failure",
			err:         toolexec.DaemonReportedFailure("auth.open_oauth_helper", "bind: address already in use"),
			wantCode:    connect.CodeInternal,
			wantMessage: "open OAuth helper: bind: address already in use",
		},
		{
			name:        "a transport failure",
			err:         fmt.Errorf("daemon.command via NATS failed: %w", errors.New("nats: connection closed (nats://gateway-7.internal:4222)")),
			wantCode:    connect.CodeInternal,
			wantMessage: "open OAuth helper",
		},
		{
			name:        "an outdated daemon",
			err:         toolexec.DaemonReportedFailure("auth.open_oauth_helper", `this machine is running an older version of Reliant (v1.7.2) that doesn't support "auth.open_oauth_helper" — update it to continue`),
			wantCode:    connect.CodeFailedPrecondition,
			wantMessage: `this machine is running an older version of Reliant (v1.7.2) that doesn't support "auth.open_oauth_helper" — update it to continue`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, client := NewTestDaemonServer(t, WithDaemonDeps(Deps{Router: &failingCommandRouter{err: tc.err}}))
			_, err := client.OpenOAuthHelper(context.Background(), connect.NewRequest(&reliantv1.OpenOAuthHelperRequest{WebOrigin: "https://app"}))
			var cerr *connect.Error
			require.ErrorAs(t, err, &cerr)
			assert.Equal(t, tc.wantCode, cerr.Code())
			assert.Equal(t, tc.wantMessage, cerr.Message())
			assert.NotContains(t, cerr.Message(), "resolving daemon", "the router's plumbing stays server-side")
			assert.NotContains(t, cerr.Message(), "nats", "the transport's words stay server-side")
		})
	}
}
