package oauth2

// OIDC discovery lives in its own file, and nothing else in this package
// imports it: the PKCE mechanism works entirely from literal endpoints, and
// Discover is a convenience for callers who would otherwise hand-copy three
// URLs out of a provider's console. Deleting this file leaves the rest of the
// package compiling.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/reliant-labs/forge/pkg/observe"
)

// maxMetadataBytes caps a discovery document read. Real documents are a few
// kilobytes; the JWKS is fetched separately by a validator, not inlined here.
const maxMetadataBytes = 1 << 20

// ProviderMetadata is the subset of an OpenID Provider configuration
// (OpenID Connect Discovery 1.0 §3, RFC 8414) that a PKCE client needs.
//
// Raw carries the whole document for callers who need a field this struct does
// not name.
type ProviderMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	UserinfoEndpoint                  string   `json:"userinfo_endpoint,omitempty"`
	JWKSURI                           string   `json:"jwks_uri,omitempty"`
	EndSessionEndpoint                string   `json:"end_session_endpoint,omitempty"`
	ScopesSupported                   []string `json:"scopes_supported,omitempty"`
	ResponseTypesSupported            []string `json:"response_types_supported,omitempty"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported,omitempty"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported,omitempty"`

	Raw map[string]any `json:"-"`
}

// SupportsS256 reports whether the provider advertises the S256 code challenge
// method.
//
// A provider that advertises no methods at all returns false rather than a
// hopeful true: the answer is "it did not say", and a caller checking this
// should treat silence as unknown instead of being told yes by a default.
// Note that many providers support S256 without advertising it, so a false
// here is a reason to check documentation, not to fall back to plain.
func (m ProviderMetadata) SupportsS256() bool {
	for _, v := range m.CodeChallengeMethodsSupported {
		if v == string(MethodS256) {
			return true
		}
	}
	return false
}

// ErrMetadataIssuerMismatch reports that a discovery document declared an
// issuer other than the one it was fetched from. OpenID Connect Discovery §4.3
// requires these to match; a mismatch means the document cannot be trusted to
// describe the issuer the caller asked about.
var ErrMetadataIssuerMismatch = errors.New("oauth2: discovery document issuer does not match the requested issuer")

// Discover fetches and validates a provider's configuration from
// issuer + "/.well-known/openid-configuration".
//
// client may be nil, in which case a client with [DefaultExchangeTimeout] is
// used. The returned metadata is checked for the issuer match required by the
// spec and for the presence of the endpoints this package needs, so a caller
// can use the endpoints without nil-checking strings.
func Discover(ctx context.Context, client HTTPDoer, issuer string) (*ProviderMetadata, error) {
	if issuer == "" {
		return nil, errors.New("oauth2: Discover requires an issuer URL")
	}
	u, err := url.Parse(issuer)
	if err != nil {
		return nil, fmt.Errorf("oauth2: parse issuer %q: %w", issuer, err)
	}
	if !u.IsAbs() {
		return nil, fmt.Errorf("oauth2: issuer %q is not an absolute URL", issuer)
	}

	metaURL := strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metaURL, nil)
	if err != nil {
		return nil, fmt.Errorf("oauth2: build discovery request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	if client == nil {
		client = observe.NewHTTPClient(observe.HTTPClientOptions{Timeout: DefaultExchangeTimeout})
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth2: fetch %s: %w", metaURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataBytes))
	if err != nil {
		return nil, fmt.Errorf("oauth2: read discovery document from %s: %w", metaURL, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("oauth2: fetch %s: HTTP %d: %s", metaURL, resp.StatusCode, summarizeBody(body))
	}

	var meta ProviderMetadata
	if err := json.Unmarshal(body, &meta); err != nil {
		return nil, fmt.Errorf("oauth2: decode discovery document from %s: %w", metaURL, err)
	}
	if err := json.Unmarshal(body, &meta.Raw); err != nil {
		return nil, fmt.Errorf("oauth2: decode discovery document from %s: %w", metaURL, err)
	}

	// Issuer equality is exact per OIDC Discovery §4.3, modulo the trailing
	// slash that providers are inconsistent about.
	if strings.TrimSuffix(meta.Issuer, "/") != strings.TrimSuffix(issuer, "/") {
		return nil, fmt.Errorf("%w: requested %q, document declared %q", ErrMetadataIssuerMismatch, issuer, meta.Issuer)
	}

	var missing []string
	if meta.AuthorizationEndpoint == "" {
		missing = append(missing, "authorization_endpoint")
	}
	if meta.TokenEndpoint == "" {
		missing = append(missing, "token_endpoint")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("oauth2: discovery document from %s omits required field(s): %s", metaURL, strings.Join(missing, ", "))
	}
	return &meta, nil
}

// AuthorizationServerMetadataPath is RFC 8414's well-known path.
const AuthorizationServerMetadataPath = "/.well-known/oauth-authorization-server"

// DiscoverAuthorizationServer fetches RFC 8414 authorization-server metadata
// from server + AuthorizationServerMetadataPath.
//
// Unlike [Discover], the document may be published by a RESOURCE server on
// behalf of the authorization server that protects it (an API naming the
// control plane that issues its tokens), so the declared issuer need not equal
// server. What IS required: the issuer and both endpoints are absolute, the
// endpoints live on the issuer's origin, and every one is https or loopback.
// A document naming a token endpoint on some other origin is refused, since
// that is where the verifier and the code are about to be sent.
func DiscoverAuthorizationServer(ctx context.Context, client HTTPDoer, server string) (*ProviderMetadata, error) {
	base, err := url.Parse(strings.TrimSpace(server))
	if err != nil || !base.IsAbs() {
		return nil, fmt.Errorf("oauth2: server %q is not an absolute URL", server)
	}
	metaURL := strings.TrimSuffix(base.String(), "/") + AuthorizationServerMetadataPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metaURL, nil)
	if err != nil {
		return nil, fmt.Errorf("oauth2: build discovery request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if client == nil {
		client = observe.NewHTTPClient(observe.HTTPClientOptions{Timeout: DefaultExchangeTimeout})
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth2: fetch %s: %w", metaURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataBytes))
	if err != nil {
		return nil, fmt.Errorf("oauth2: read %s: %w", metaURL, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %s", ErrNoAuthorizationServer, metaURL)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("oauth2: fetch %s: HTTP %d: %s", metaURL, resp.StatusCode, summarizeBody(body))
	}
	var meta ProviderMetadata
	if err := json.Unmarshal(body, &meta); err != nil {
		return nil, fmt.Errorf("oauth2: decode %s: %w", metaURL, err)
	}
	_ = json.Unmarshal(body, &meta.Raw)

	issuer, err := url.Parse(meta.Issuer)
	if err != nil || !issuer.IsAbs() {
		return nil, fmt.Errorf("oauth2: %s declares no absolute issuer", metaURL)
	}
	for name, raw := range map[string]string{
		"authorization_endpoint": meta.AuthorizationEndpoint,
		"token_endpoint":         meta.TokenEndpoint,
	} {
		u, err := url.Parse(raw)
		if raw == "" || err != nil || !u.IsAbs() {
			return nil, fmt.Errorf("oauth2: %s has no absolute %s", metaURL, name)
		}
		if u.Scheme != issuer.Scheme || u.Host != issuer.Host {
			return nil, fmt.Errorf("oauth2: %s's %s %q is not on the issuer's origin %s", metaURL, name, raw, meta.Issuer)
		}
		if u.Scheme != "https" && !isLoopbackHost(u.Hostname()) {
			return nil, fmt.Errorf("oauth2: %s's %s must use https (loopback excepted)", metaURL, name)
		}
	}
	return &meta, nil
}

// ErrNoAuthorizationServer reports a server that publishes no authorization
// server metadata: it issues no tokens through a browser login.
var ErrNoAuthorizationServer = errors.New("oauth2: the server publishes no authorization server metadata")

func isLoopbackHost(h string) bool {
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}
