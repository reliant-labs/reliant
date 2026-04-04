// Copyright (c) 2025 Reliant Labs
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
	TimeoutSeconds       int    `json:"timeout_seconds"`        // default 120
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

	result, err := oauthcallback.Run(ctx, req.AuthorizeURLTemplate, req.TimeoutSeconds)
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
