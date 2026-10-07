// Copyright (c) 2025 Reliant Labs

package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/controlplane"
)

// Waking a suspended workspace is a control-plane capability.
//
// The resume goes to controlplane.v1.DaemonService/ResumeDaemon. That endpoint
// derives the owner from the forwarded Bearer, which is why the caller's
// OAuth token travels with the request (see CallerToken): the resume happens
// AS THE USER, scoped to workspaces they own, rather than through a service
// credential that could wake anyone's.
//
// (It used to call reliant.v1.DaemonRegistryService/ResumeDaemon on the
// control plane, which control-plane no longer serves: reliant's api-server is
// the only host of that service now — docs/design/one-daemon-list.md.)
//
// Without a configured URL there is nothing to call, and Resume says so
// instead of pretending a wake is under way.

// resumeTimeout bounds the resume RPC. It only kicks off the wake — the pod
// schedule happens after it returns — so this is short on purpose.
const resumeTimeout = 15 * time.Second

// ControlPlaneResumer wakes managed workspaces via the control plane.
type ControlPlaneResumer struct {
	client *controlplane.DaemonClient
}

// NewControlPlaneResumer builds a resumer against baseURL. It returns nil when
// baseURL is empty, which callers pass straight to NewAttachmentReadiness —
// a nil resumer means "this deployment cannot start workspaces", reported
// honestly rather than waited on.
func NewControlPlaneResumer(baseURL string) *ControlPlaneResumer {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return nil
	}
	return &ControlPlaneResumer{client: controlplane.NewDaemonClient(trimmed)}
}

// ResumeDaemon asks the control plane to wake daemonID on the user's behalf.
//
// userID is not sent: the control plane derives the owner from the forwarded
// token, and a user id in the body would be a second, unverified claim about
// who is asking.
func (r *ControlPlaneResumer) ResumeDaemon(ctx context.Context, userID, daemonID string) error {
	if r == nil || r.client == nil {
		return errors.New("no control plane configured to start workspaces")
	}
	_ = userID

	token := CallerToken(ctx)
	if token == "" {
		// A connector-credential caller has no user token to forward. The
		// control plane would reject the call, so say the useful thing here
		// rather than surfacing an opaque 401.
		return errors.New(
			"starting a workspace requires signing in; this connection uses a connector " +
				"credential, so start the workspace from the app first")
	}

	ctx, cancel := context.WithTimeout(ctx, resumeTimeout)
	defer cancel()
	// A refusal (e.g. "cannot resume external daemon") arrives as the Connect
	// error's message and is surfaced verbatim: it is the actionable part.
	if err := r.client.ResumeDaemon(ctx, token, daemonID); err != nil {
		return fmt.Errorf("control plane could not start the workspace: %w", err)
	}
	return nil
}
