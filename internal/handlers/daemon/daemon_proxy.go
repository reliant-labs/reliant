// Copyright (c) 2025 Reliant Labs
package daemon

import (
	"context"
	"encoding/json"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
)

// Every rpc forwards its request to the caller's daemon as one DaemonCommand
// (request/response) through Deps.Router.

// errNoRouter answers every rpc of a service composed without a daemon
// router: this server has no way to reach a daemon.
func errNoRouter() *connect.Error {
	return svcerr.ToConnect(svcerr.Unavailable("no daemon router: this server cannot reach a daemon"))
}

// StartOAuthFlow proxies the OAuth flow to the daemon which starts a localhost
// callback server and opens the browser.
func (s *Service) StartOAuthFlow(
	ctx context.Context,
	req *connect.Request[reliantv1.StartOAuthFlowRequest],
) (*connect.Response[reliantv1.StartOAuthFlowResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.Unauthenticated("user ID not found in context")
	}

	payload, err := json.Marshal(map[string]any{
		"authorize_url_template": req.Msg.AuthorizeUrlTemplate,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("marshal request: %w", err))
	}

	if s.deps.Router == nil {
		return nil, errNoRouter()
	}
	// Use a generous NATS-level timeout (1 hour). OAuth flows have no
	// application-level timeout — the frontend AbortController handles
	// cancellation, which propagates via ctx.
	respBytes, err := s.deps.Router.SendDaemonCommand(ctx, userID, "auth.start_oauth", payload, 3_600_000)
	if err != nil {
		return nil, oauthDaemonError("OAuth flow failed", err)
	}

	var resp struct {
		Code        string `json:"code"`
		State       string `json:"state"`
		RedirectURI string `json:"redirect_uri"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("unmarshal response: %w", err))
	}

	return connect.NewResponse(&reliantv1.StartOAuthFlowResponse{
		Code:        resp.Code,
		State:       resp.State,
		RedirectUri: resp.RedirectURI,
	}), nil
}

// oauthDaemonError maps a failed OAuth daemon command onto the Connect code
// for what went wrong, wrapped with what was being done:
//
//   - The daemon is older than this server: FailedPrecondition, the daemon's
//     own message. The production report that started this was
//     `open OAuth helper: daemon command "auth.open_oauth_helper" failed:
//     unknown daemon command type` from a daemon that predated the handler —
//     nothing was broken server-side, but `code: internal` said otherwise.
//   - The command never reached a machine: the machine's state picks the code
//     (daemonerr.MachineStateConnectError). "No machine is connected to your account
//     yet" used to leave here as Internal (ELECTRON-B1); the settings page
//     asks for the helper whether or not the user has connected a machine.
//   - Anything else — the daemon ran the command and it failed, the reply
//     timed out — is Internal.
func oauthDaemonError(doing string, err error) *connect.Error {
	if daemonerr.IsOutdated(err) {
		return daemonerr.OutdatedConnectError(err)
	}
	if cerr, ok := daemonerr.MachineStateConnectError(doing, err); ok {
		return cerr
	}
	return connect.NewError(connect.CodeInternal, fmt.Errorf("%s: %w", doing, err))
}

// openHelperTimeoutMs bounds the open/close commands. These return as soon as
// the listener is bound or closed — no human is in the loop — so the generous
// hour StartOAuthFlow needs would only delay reporting a dead daemon.
const openHelperTimeoutMs = 10_000

// OpenOAuthHelper asks the user's daemon to open its localhost OAuth helper
// port for one linking session.
func (s *Service) OpenOAuthHelper(
	ctx context.Context,
	req *connect.Request[reliantv1.OpenOAuthHelperRequest],
) (*connect.Response[reliantv1.OpenOAuthHelperResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.Unauthenticated("user ID not found in context")
	}

	payload, err := json.Marshal(map[string]any{
		"web_origin": req.Msg.WebOrigin,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("marshal request: %w", err))
	}

	if s.deps.Router == nil {
		return nil, errNoRouter()
	}
	respBytes, err := s.deps.Router.SendDaemonCommand(ctx, userID, "auth.open_oauth_helper", payload, openHelperTimeoutMs)
	if err != nil {
		return nil, oauthDaemonError("open OAuth helper", err)
	}

	var resp struct {
		Port           int32  `json:"port"`
		Addr           string `json:"addr"`
		AlreadyRunning bool   `json:"already_running"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("unmarshal response: %w", err))
	}

	return connect.NewResponse(&reliantv1.OpenOAuthHelperResponse{
		Port:           resp.Port,
		Addr:           resp.Addr,
		AlreadyRunning: resp.AlreadyRunning,
	}), nil
}

// CloseOAuthHelper closes the helper port when the linking session ends.
func (s *Service) CloseOAuthHelper(
	ctx context.Context,
	_ *connect.Request[reliantv1.CloseOAuthHelperRequest],
) (*connect.Response[reliantv1.CloseOAuthHelperResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.Unauthenticated("user ID not found in context")
	}

	if s.deps.Router == nil {
		return nil, errNoRouter()
	}
	respBytes, err := s.deps.Router.SendDaemonCommand(ctx, userID, "auth.close_oauth_helper", []byte("{}"), openHelperTimeoutMs)
	if err != nil {
		return nil, oauthDaemonError("close OAuth helper", err)
	}

	var resp struct {
		Closed bool `json:"closed"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("unmarshal response: %w", err))
	}

	return connect.NewResponse(&reliantv1.CloseOAuthHelperResponse{Closed: resp.Closed}), nil
}
