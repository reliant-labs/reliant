package controlplane

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	daemonv1 "github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1"
)

// Managed-machine lifecycle ACTIONS go through controlplane.v1.DaemonService.
// There are deliberately no lookups: daemon records — identity, liveness, and
// the lifecycle phase control-plane mirrors over daemon.v1.state.<id>.lifecycle
// — live in reliant's own registry (docs/design/one-daemon-list.md, option
// (b)), so reliant reads them from its own database. What only control-plane
// can do is act on the machine: the Workspace CR is its, and so is the
// decision to spend compute on it.

// ResumeDaemon implements Service; see its contract.
func (c *client) ResumeDaemon(ctx context.Context, token, daemonID string) error {
	req := connect.NewRequest(&daemonv1.ResumeDaemonRequest{DaemonId: daemonID})
	if t := strings.TrimSpace(token); t != "" {
		req.Header().Set("Authorization", "Bearer "+t)
	}
	_, err := c.daemons.ResumeDaemon(ctx, req)
	return err
}
