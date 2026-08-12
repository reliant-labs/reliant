// Copyright (c) 2025 Reliant Labs

package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

// Waking a suspended workspace is a control-plane capability.
//
// The control plane serves reliant.v1.DaemonRegistryService/ResumeDaemon and
// translates it onto its own DaemonService, so this speaks reliant's own
// vocabulary and does not need to know the control-plane proto. That endpoint
// is JWT-authed and forwards the caller's Bearer token, which is why the
// caller's OAuth token travels with the request (see CallerToken): the resume
// happens AS THE USER, scoped to workspaces they own, rather than through a
// service credential that could wake anyone's.
//
// Without a configured URL there is nothing to call, and Resume says so
// instead of pretending a wake is under way.

// resumeTimeout bounds the resume RPC. It only kicks off the wake — the pod
// schedule happens after it returns — so this is short on purpose.
const resumeTimeout = 15 * time.Second

// ControlPlaneResumer wakes managed workspaces via the control plane.
type ControlPlaneResumer struct {
	client reliantv1connect.DaemonRegistryServiceClient
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
	return &ControlPlaneResumer{
		client: reliantv1connect.NewDaemonRegistryServiceClient(
			&http.Client{Timeout: resumeTimeout}, trimmed),
	}
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

	req := connect.NewRequest(&reliantv1.ResumeDaemonRequest{DaemonId: daemonID})
	req.Header().Set("Authorization", "Bearer "+token)

	resp, err := r.client.ResumeDaemon(ctx, req)
	if err != nil {
		return fmt.Errorf("control plane could not start the workspace: %w", err)
	}
	if !resp.Msg.GetResumed() {
		if msg := strings.TrimSpace(resp.Msg.GetErrorMessage()); msg != "" {
			return errors.New(msg)
		}
		return errors.New("the control plane declined to start this workspace")
	}
	return nil
}
