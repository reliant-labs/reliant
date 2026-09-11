// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// DaemonProxyService implements DaemonServiceHandler by forwarding
// requests to the user's daemon via DaemonCommand (request/response).
type DaemonProxyService struct {
	reliantv1connect.UnimplementedDaemonServiceHandler
	router toolexec.DaemonRouter
}

// NewDaemonProxyService creates a new DaemonProxyService.
func NewDaemonProxyService(router toolexec.DaemonRouter) *DaemonProxyService {
	return &DaemonProxyService{router: router}
}

// StartOAuthFlow proxies the OAuth flow to the daemon which starts a localhost
// callback server and opens the browser.
func (s *DaemonProxyService) StartOAuthFlow(
	ctx context.Context,
	req *connect.Request[reliantv1.StartOAuthFlowRequest],
) (*connect.Response[reliantv1.StartOAuthFlowResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user ID not found in context"))
	}

	payload, err := json.Marshal(map[string]any{
		"authorize_url_template": req.Msg.AuthorizeUrlTemplate,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("marshal request: %w", err))
	}

	// Use a generous NATS-level timeout (1 hour). OAuth flows have no
	// application-level timeout — the frontend AbortController handles
	// cancellation, which propagates via ctx.
	respBytes, err := s.router.SendDaemonCommand(ctx, userID, "auth.start_oauth", payload, 3_600_000)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("OAuth flow failed: %w", err))
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

// openHelperTimeoutMs bounds the open/close commands. These return as soon as
// the listener is bound or closed — no human is in the loop — so the generous
// hour StartOAuthFlow needs would only delay reporting a dead daemon.
const openHelperTimeoutMs = 10_000

// OpenOAuthHelper asks the user's daemon to open its localhost OAuth helper
// port for one linking session.
func (s *DaemonProxyService) OpenOAuthHelper(
	ctx context.Context,
	req *connect.Request[reliantv1.OpenOAuthHelperRequest],
) (*connect.Response[reliantv1.OpenOAuthHelperResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user ID not found in context"))
	}

	payload, err := json.Marshal(map[string]any{
		"web_origin": req.Msg.WebOrigin,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("marshal request: %w", err))
	}

	respBytes, err := s.router.SendDaemonCommand(ctx, userID, "auth.open_oauth_helper", payload, openHelperTimeoutMs)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("open OAuth helper: %w", err))
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
func (s *DaemonProxyService) CloseOAuthHelper(
	ctx context.Context,
	_ *connect.Request[reliantv1.CloseOAuthHelperRequest],
) (*connect.Response[reliantv1.CloseOAuthHelperResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user ID not found in context"))
	}

	respBytes, err := s.router.SendDaemonCommand(ctx, userID, "auth.close_oauth_helper", []byte("{}"), openHelperTimeoutMs)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("close OAuth helper: %w", err))
	}

	var resp struct {
		Closed bool `json:"closed"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("unmarshal response: %w", err))
	}

	return connect.NewResponse(&reliantv1.CloseOAuthHelperResponse{Closed: resp.Closed}), nil
}
