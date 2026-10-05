package gitcredentialclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ServicePath is the Connect path prefix of control-plane's internal service.
const ServicePath = "/controlplane.v1.GitCredentialInternalService/"

// Reason codes control-plane attaches to a FailedPrecondition, in the
// X-Forge-Error-Reason response header.
const (
	reasonHeader         = "X-Forge-Error-Reason"
	reasonNotConnected   = "git_credential_not_connected"
	reasonNeedsReconnect = "git_credential_needs_reconnect"
)

// Client calls control-plane's GitCredentialInternalService.
type Client struct {
	baseURL string
	http    *http.Client
	sign    Signer
}

// New returns a client for the control-plane at deps.BaseURL.
//
// forge:constructor
func New(deps Deps) *Client {
	httpClient := deps.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(deps.BaseURL), "/"),
		http:    httpClient,
		sign:    deps.Sign,
	}
}

// RPCError is a Connect error control-plane returned that is neither of the
// typed outcomes. Its Message is control-plane's client-safe text.
type RPCError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return "gitcredentialclient: control-plane " + e.Code + ": " + e.Message
}

// UserAccessToken asks control-plane for the user's current token.
func (c *Client) UserAccessToken(ctx context.Context, externalUserID, provider string) (Token, error) {
	if strings.TrimSpace(externalUserID) == "" {
		return Token{}, errors.New("gitcredentialclient: user id is required")
	}
	if c.baseURL == "" {
		return Token{}, errors.New("gitcredentialclient: control-plane URL is not configured")
	}
	if c.sign == nil {
		return Token{}, errors.New("gitcredentialclient: no internal-service signer configured")
	}
	bearer, err := c.sign()
	if err != nil {
		return Token{}, fmt.Errorf("gitcredentialclient: signing internal-service token: %w", err)
	}
	payload, err := json.Marshal(map[string]string{"userId": externalUserID, "provider": provider})
	if err != nil {
		return Token{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+ServicePath+"GetUserAccessToken", bytes.NewReader(payload))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Authorization", "Bearer "+bearer)

	resp, err := c.http.Do(req)
	if err != nil {
		// Never wrap the request: its URL is harmless, but keep the habit of
		// errors that cannot carry a credential.
		return Token{}, fmt.Errorf("gitcredentialclient: GetUserAccessToken: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Token{}, fmt.Errorf("gitcredentialclient: reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		switch resp.Header.Get(reasonHeader) {
		case reasonNotConnected:
			return Token{}, ErrNotConnected
		case reasonNeedsReconnect:
			return Token{}, ErrNeedsReconnect
		}
		rpcErr := &RPCError{Code: http.StatusText(resp.StatusCode)}
		_ = json.Unmarshal(body, rpcErr)
		return Token{}, rpcErr
	}
	var out struct {
		AccessToken string     `json:"accessToken"`
		ExpiresAt   *time.Time `json:"expiresAt"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		// The body holds a token: do not echo it into the error.
		return Token{}, errors.New("gitcredentialclient: decoding GetUserAccessToken response failed")
	}
	if out.AccessToken == "" {
		return Token{}, errors.New("gitcredentialclient: control-plane returned an empty token")
	}
	return Token{accessToken: out.AccessToken, ExpiresAt: out.ExpiresAt}, nil
}

// A Token holds plaintext by design, so it must not print it.
func (t Token) String() string             { return "gitcredentialclient.Token{[redacted]}" }
func (t Token) GoString() string           { return t.String() }
func (t Token) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, t.String()) }
