// Copyright (c) 2025 Reliant Labs

package connections

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/vault"
)

// Environment variables for the GitHub App (CONNECTIONS_VAULT.md §4.5, §6).
// Deployment config, declared as forge secret_refs; never stored in the vault.
const (
	EnvGitHubAppClientID     = "RELIANT_GITHUB_APP_CLIENT_ID"
	EnvGitHubAppClientSecret = "RELIANT_GITHUB_APP_CLIENT_SECRET"

	// GitHubIntegrationID is the integration id GitHub connections carry.
	GitHubIntegrationID = "github"

	maxProviderResponse = 1 << 20
)

// HTTPDoer is the slice of *http.Client the broker, token source and probes use.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// NewHTTPClient returns the client used for provider calls: bounded, and it
// never follows a redirect, since an Authorization header must not be replayed
// at a host the catalog did not name.
func NewHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Identity is who a credential acts as at the provider.
type Identity struct {
	ExternalAccountID string
	AccountLabel      string
}

// Provider is one OAuth integration. Every URL is catalog-fixed: it comes from
// code, never from a request or a database row, so a user cannot point the
// broker's token exchange at an arbitrary host (§8.4).
type Provider struct {
	ID          string
	DisplayName string
	AuthKind    string

	AuthorizeURL string
	TokenURL     string
	// ProbeURL is the identity endpoint, used to label a new connection and by
	// TestConnection.
	ProbeURL string
	Scopes   []string

	ClientID     string
	ClientSecret vault.Secret

	// Extra authorize parameters.
	AuthorizeExtra url.Values
}

// Available reports whether the deployment configured this provider.
func (p *Provider) Available() bool { return p.ClientID != "" && p.ClientSecret.Len() > 0 }

// UnavailableReason explains, for an operator, why Available is false.
func (p *Provider) UnavailableReason() string {
	switch {
	case p.Available():
		return ""
	case p.ID == GitHubIntegrationID:
		return EnvGitHubAppClientID + " and " + EnvGitHubAppClientSecret + " are not set"
	default:
		return "client credentials are not configured"
	}
}

func (p *Provider) validate() error {
	for name, raw := range map[string]string{"authorize_url": p.AuthorizeURL, "token_url": p.TokenURL, "probe_url": p.ProbeURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("provider %q: %s must be an absolute https URL", p.ID, name)
		}
	}
	return nil
}

// Registry is the catalog of OAuth providers.
type Registry struct {
	order     []string
	providers map[string]*Provider
}

// NewRegistry validates and registers providers. A provider with a non-HTTPS
// endpoint is rejected outright.
func NewRegistry(providers ...*Provider) (*Registry, error) {
	r := &Registry{providers: map[string]*Provider{}}
	for _, p := range providers {
		if err := p.validate(); err != nil {
			return nil, err
		}
		if _, dup := r.providers[p.ID]; dup {
			return nil, fmt.Errorf("provider %q registered twice", p.ID)
		}
		r.providers[p.ID] = p
		r.order = append(r.order, p.ID)
	}
	return r, nil
}

// Get returns the provider for an id.
func (r *Registry) Get(id string) (*Provider, bool) {
	if r == nil {
		return nil, false
	}
	p, ok := r.providers[id]
	return p, ok
}

// List returns every registered provider, available or not, in registration order.
func (r *Registry) List() []*Provider {
	if r == nil {
		return nil
	}
	out := make([]*Provider, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.providers[id])
	}
	return out
}

// GitHubApp builds the GitHub App user-to-server provider. With empty
// credentials it is still registered, listed as unavailable.
func GitHubApp(clientID string, clientSecret vault.Secret) *Provider {
	return &Provider{
		ID:           GitHubIntegrationID,
		DisplayName:  "GitHub",
		AuthKind:     core.ConnectionAuthGitHubAppUser,
		AuthorizeURL: "https://github.com/login/oauth/authorize",
		TokenURL:     "https://github.com/login/oauth/access_token",
		ProbeURL:     "https://api.github.com/user",
		// A GitHub App's permissions are fixed on the App itself; the scope
		// parameter is ignored, so none is sent.
		ClientID:     clientID,
		ClientSecret: clientSecret,
	}
}

// ProvidersFromEnv builds the registry from deployment config. Unset GitHub
// credentials leave the provider listed but unavailable; they never fail boot.
func ProvidersFromEnv(getenv func(string) string) (*Registry, error) {
	id := strings.TrimSpace(getenv(EnvGitHubAppClientID))
	secret := strings.TrimSpace(getenv(EnvGitHubAppClientSecret))
	return NewRegistry(GitHubApp(id, vault.NewSecret([]byte(secret))))
}

// identify asks the provider who an access token acts as.
func (p *Provider) identify(ctx context.Context, doer HTTPDoer, accessToken string) (Identity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.ProbeURL, nil)
	if err != nil {
		return Identity{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "reliant-connections")
	resp, err := doer.Do(req)
	if err != nil {
		return Identity{}, &probeError{class: "unreachable"}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderResponse))
	if err != nil {
		return Identity{}, &probeError{class: "unreachable"}
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return Identity{}, &probeError{class: "unauthorized"}
	case resp.StatusCode >= 500:
		return Identity{}, &probeError{class: "unreachable"}
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return Identity{}, &probeError{class: "unauthorized"}
	}
	var who struct {
		ID    json.Number `json:"id"`
		Login string      `json:"login"`
	}
	if err := json.Unmarshal(body, &who); err != nil || who.ID.String() == "" {
		return Identity{}, &probeError{class: "unreachable"}
	}
	if _, err := strconv.ParseInt(who.ID.String(), 10, 64); err != nil {
		return Identity{}, &probeError{class: "unreachable"}
	}
	return Identity{ExternalAccountID: who.ID.String(), AccountLabel: who.Login}, nil
}

// probeError carries only a class, never a provider body.
type probeError struct{ class string }

func (e *probeError) Error() string { return "provider probe failed: " + e.class }
