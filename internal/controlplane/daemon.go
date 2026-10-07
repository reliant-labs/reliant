package controlplane

import (
	"context"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	daemonv1 "github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1"
	daemonv1connect "github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1/controlplanev1connect"
)

// DaemonClient performs managed-machine lifecycle ACTIONS through
// controlplane.v1.DaemonService.
//
// It deliberately has no lookups. Daemon records — identity, liveness, and the
// lifecycle phase control-plane mirrors over daemon.v1.state.<id>.lifecycle —
// live in reliant's own registry (docs/design/one-daemon-list.md, option (b)),
// so reliant reads them from its own database. What only control-plane can do
// is act on the machine: the Workspace CR is its, and so is the decision to
// spend compute on it.
//
// A separate type rather than a method on Client: Client's consumers would all
// have to grow a method none of them call.
type DaemonClient struct {
	daemons daemonv1connect.DaemonServiceClient
}

// NewDaemonClient builds a DaemonClient against baseURL, falling back to the
// environment's control-plane URL exactly as NewClient does.
func NewDaemonClient(baseURL string) *DaemonClient {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		trimmed = getBaseURL()
	}
	return &DaemonClient{daemons: daemonv1connect.NewDaemonServiceClient(
		&http.Client{Timeout: 30 * time.Second}, strings.TrimRight(trimmed, "/"))}
}

// ResumeDaemon asks control-plane to wake one suspended managed daemon. It
// returns once the wake is under way, not once the daemon is attached.
//
// token is sent as the Bearer and is what control-plane derives the owner
// from: a user's JWT, or a `daemon:resume` access token bound to exactly this
// daemon (control-plane's svcdaemon.ownerForDaemon). There is no service
// credential, so a resume can only ever wake a daemon its caller owns.
//
// Errors are control-plane's Connect errors, unwrapped, so callers can read
// the code: FailedPrecondition means control-plane does not hold this daemon
// as suspended (already resumed, or not a managed machine).
func (c *DaemonClient) ResumeDaemon(ctx context.Context, token, daemonID string) error {
	req := connect.NewRequest(&daemonv1.ResumeDaemonRequest{DaemonId: daemonID})
	if t := strings.TrimSpace(token); t != "" {
		req.Header().Set("Authorization", "Bearer "+t)
	}
	_, err := c.daemons.ResumeDaemon(ctx, req)
	return err
}
