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

	"github.com/reliant-labs/forge/pkg/observe"
)

// ServicePath is the Connect path prefix of control-plane's internal service.
const ServicePath = "/controlplane.v1.GitCredentialInternalService/"

// Reason codes control-plane attaches to a FailedPrecondition, in the
// X-Forge-Error-Reason response header.
const (
	reasonHeader         = "X-Forge-Error-Reason"
	reasonNotConnected   = "git_credential_not_connected"
	reasonNeedsReconnect = "git_credential_needs_reconnect"

	reasonAppNotConfigured = "github_app_not_configured"
	reasonAppNotInstalled  = "github_app_not_installed"
	reasonAppPermissions   = "github_app_permissions"
	reasonOwnerCannotPush  = "github_owner_cannot_push"
	reasonAppNotLinked     = "github_app_not_linked"
	reasonNotOrgMember     = "not_org_member"
)

// Client calls control-plane's GitCredentialInternalService.
type Client struct {
	baseURL string
	http    *http.Client
	sign    Signer
}

// New returns a client for the control-plane deps.ControlPlane names.
//
// forge:constructor
func New(deps Deps) Service {
	httpClient := deps.HTTPClient
	if httpClient == nil {
		httpClient = observe.NewHTTPClient(observe.HTTPClientOptions{Timeout: 10 * time.Second})
	}
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(deps.ControlPlane.GetUrl()), "/"),
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
	var out struct {
		AccessToken string     `json:"accessToken"`
		ExpiresAt   *time.Time `json:"expiresAt"`
	}
	if err := c.call(ctx, "GetUserAccessToken", map[string]string{"userId": externalUserID, "provider": provider}, &out); err != nil {
		return Token{}, err
	}
	if out.AccessToken == "" {
		return Token{}, errors.New("gitcredentialclient: control-plane returned an empty token")
	}
	return Token{accessToken: out.AccessToken, ExpiresAt: out.ExpiresAt}, nil
}

// MintInstallationToken asks control-plane for an installation token scoped to
// one repository.
func (c *Client) MintInstallationToken(ctx context.Context, req InstallationRequest) (InstallationToken, error) {
	if strings.TrimSpace(req.ExternalUserID) == "" {
		return InstallationToken{}, errors.New("gitcredentialclient: user id is required")
	}
	if strings.TrimSpace(req.Owner) == "" || strings.TrimSpace(req.Repo) == "" {
		return InstallationToken{}, errors.New("gitcredentialclient: owner and repo are required")
	}
	body := map[string]any{"userId": req.ExternalUserID, "owner": req.Owner, "repo": req.Repo}
	if len(req.Permissions) > 0 {
		body["permissions"] = req.Permissions
	}
	if org := strings.TrimSpace(req.OrgID); org != "" {
		body["orgId"] = org
	}
	var out struct {
		Token          string    `json:"token"`
		ExpiresAt      time.Time `json:"expiresAt"`
		BotLogin       string    `json:"botLogin"`
		ActingForLogin string    `json:"actingForLogin"`
		Repository     string    `json:"repository"`
		// int64 crosses Connect JSON as a string; absent from a control
		// plane that predates it.
		InstallationID json.Number `json:"installationId"`
	}
	if err := c.call(ctx, "MintInstallationToken", body, &out); err != nil {
		return InstallationToken{}, err
	}
	if out.Token == "" {
		return InstallationToken{}, errors.New("gitcredentialclient: control-plane returned an empty installation token")
	}
	var installationID int64
	if out.InstallationID != "" {
		id, err := out.InstallationID.Int64()
		if err != nil || id < 0 {
			return InstallationToken{}, errors.New("gitcredentialclient: control-plane returned an invalid installation id")
		}
		installationID = id
	}
	return InstallationToken{
		Token:          Token{accessToken: out.Token, ExpiresAt: &out.ExpiresAt},
		ExpiresAt:      out.ExpiresAt,
		BotLogin:       out.BotLogin,
		ActingForLogin: out.ActingForLogin,
		Repository:     out.Repository,
		InstallationID: installationID,
	}, nil
}

// ListOrgGitHubInstallations asks control-plane for the installations orgID
// has linked.
func (c *Client) ListOrgGitHubInstallations(ctx context.Context, orgID string) ([]OrgInstallation, error) {
	if strings.TrimSpace(orgID) == "" {
		return nil, errors.New("gitcredentialclient: org id is required")
	}
	var out struct {
		Installations []struct {
			// int64 crosses Connect JSON as a string.
			InstallationID json.Number `json:"installationId"`
			AccountLogin   string      `json:"accountLogin"`
			AccountType    string      `json:"accountType"`
			Suspended      bool        `json:"suspended"`
		} `json:"installations"`
	}
	if err := c.call(ctx, "ListOrgGitHubInstallations", map[string]string{"orgId": orgID}, &out); err != nil {
		return nil, err
	}
	list := make([]OrgInstallation, 0, len(out.Installations))
	for _, in := range out.Installations {
		id, err := in.InstallationID.Int64()
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("gitcredentialclient: control-plane returned an invalid installation id")
		}
		list = append(list, OrgInstallation{InstallationID: id, AccountLogin: in.AccountLogin, AccountType: in.AccountType, Suspended: in.Suspended})
	}
	return list, nil
}

// call POSTs one Connect unary JSON request to the internal service and decodes
// a 200 body into out. A non-200 becomes a typed error when control-plane's
// X-Forge-Error-Reason names one, else an *RPCError. Neither the request nor
// the response body is ever echoed into an error: a response carries a token.
func (c *Client) call(ctx context.Context, method string, payload, out any) error {
	if c.baseURL == "" {
		return errors.New("gitcredentialclient: control-plane URL is not configured")
	}
	if c.sign == nil {
		return errors.New("gitcredentialclient: no internal-service signer configured")
	}
	bearer, err := c.sign()
	if err != nil {
		return fmt.Errorf("gitcredentialclient: signing internal-service token: %w", err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+ServicePath+method, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Authorization", "Bearer "+bearer)

	resp, err := c.http.Do(req)
	if err != nil {
		// Never wrap the request: its URL is harmless, but keep the habit of
		// errors that cannot carry a credential.
		return fmt.Errorf("gitcredentialclient: %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("gitcredentialclient: reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		if typed, ok := reasonErrors[resp.Header.Get(reasonHeader)]; ok {
			return typed
		}
		rpcErr := &RPCError{Code: http.StatusText(resp.StatusCode)}
		_ = json.Unmarshal(body, rpcErr)
		return rpcErr
	}
	if err := json.Unmarshal(body, out); err != nil {
		// The body holds a token: do not echo it into the error.
		return fmt.Errorf("gitcredentialclient: decoding %s response failed", method)
	}
	return nil
}

// reasonErrors maps control-plane's X-Forge-Error-Reason values onto this
// package's typed outcomes.
var reasonErrors = map[string]error{
	reasonNotConnected:     ErrNotConnected,
	reasonNeedsReconnect:   ErrNeedsReconnect,
	reasonAppNotConfigured: ErrAppNotConfigured,
	reasonAppNotInstalled:  ErrAppNotInstalled,
	reasonAppPermissions:   ErrAppPermissions,
	reasonOwnerCannotPush:  ErrOwnerCannotPush,
	reasonAppNotLinked:     ErrAppNotLinked,
	reasonNotOrgMember:     ErrNotOrgMember,
}

// A Token holds plaintext by design, so it must not print it.
func (t Token) String() string             { return "gitcredentialclient.Token{[redacted]}" }
func (t Token) GoString() string           { return t.String() }
func (t Token) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, t.String()) }
