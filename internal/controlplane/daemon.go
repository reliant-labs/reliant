package controlplane

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
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

// SuspendDaemon implements Service; see its contract.
func (c *client) SuspendDaemon(ctx context.Context, jwt, daemonID string) error {
	req := connect.NewRequest(&daemonv1.SuspendDaemonRequest{DaemonId: daemonID})
	attachAuthorization(req, "Bearer "+strings.TrimSpace(jwt))
	_, err := c.daemons.SuspendDaemon(ctx, req)
	return err
}

// ResizeDaemon implements Service; see its contract.
func (c *client) ResizeDaemon(ctx context.Context, jwt, daemonID, size string) error {
	pbSize, ok := daemonSizes[strings.ToLower(strings.TrimSpace(size))]
	if !ok {
		return fmt.Errorf("unknown machine size %q: want one of small, medium, large, xl, 2xl", size)
	}
	req := connect.NewRequest(&daemonv1.ResizeDaemonRequest{DaemonId: daemonID, Size: pbSize})
	attachAuthorization(req, "Bearer "+strings.TrimSpace(jwt))
	_, err := c.daemons.ResizeDaemon(ctx, req)
	return err
}

// daemonSizes are the size names reliant's daemon registry stores (the
// control plane's lifecycle mirror), mapped to the control plane's enum.
var daemonSizes = map[string]controlplanev1.DaemonSize{
	"small":  controlplanev1.DaemonSize_DAEMON_SIZE_SMALL,
	"medium": controlplanev1.DaemonSize_DAEMON_SIZE_MEDIUM,
	"large":  controlplanev1.DaemonSize_DAEMON_SIZE_LARGE,
	"xl":     controlplanev1.DaemonSize_DAEMON_SIZE_XL,
	"2xl":    controlplanev1.DaemonSize_DAEMON_SIZE_2XL,
}
