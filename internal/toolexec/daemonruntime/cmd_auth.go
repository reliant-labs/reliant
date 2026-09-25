// Copyright (c) 2025 Reliant Labs
//
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: the only dial is preview_forwarder.go's LOOPBACK dial to the user's own dev server (127.0.0.1/::1), which is the preview feature itself, not a third-party boundary
//forge:exclude-contract: the tools-daemon command handlers; shaped by the daemon command dispatch table, and every collaborator is injected by the daemon runtime
package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/reliant-labs/reliant/internal/auth/oauthcallback"
)

func init() {
	RegisterCommand("auth.start_oauth", handleAuthStartOAuth)
}

type authStartOAuthRequest struct {
	AuthorizeURLTemplate string `json:"authorize_url_template"` // URL with {redirect_uri} placeholder
}

type authStartOAuthResponse struct {
	Code        string `json:"code"`
	State       string `json:"state"`
	RedirectURI string `json:"redirect_uri"`
	CallbackURL string `json:"callback_url"` // full callback URL with query params
}

func handleAuthStartOAuth(ctx context.Context, payload []byte) ([]byte, error) {
	var req authStartOAuthRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	result, err := oauthcallback.Run(ctx, req.AuthorizeURLTemplate)
	if err != nil {
		return nil, err
	}

	resp := authStartOAuthResponse{
		Code:        result.Code,
		State:       result.State,
		RedirectURI: result.RedirectURI,
		CallbackURL: result.CallbackURL,
	}
	return json.Marshal(resp)
}
