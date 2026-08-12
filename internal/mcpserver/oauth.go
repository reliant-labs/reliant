// Copyright (c) 2025 Reliant Labs

package mcpserver

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// OAuth support for connector clients.
//
// The MCP authorization spec makes this server an OAuth *protected resource*,
// not an authorization server. Those are different jobs, and the spec is
// explicit that they may live in different places:
//
//	"The implementation details of the authorization server are beyond the
//	 scope of this specification. It may be hosted with the resource server or
//	 a separate entity."
//
// So the parts that must exist here are small and well-defined:
//
//   - RFC 9728 protected-resource metadata, telling a client which
//     authorization server to go to.
//   - A WWW-Authenticate header on 401 pointing at that metadata, so a client
//     discovers the flow from a failed request rather than by guessing.
//   - Bearer-token validation, which reliant already does for Supabase JWTs.
//
// Issuing tokens — /authorize, /token, PKCE, consent, client registration —
// belongs to the identity provider this deployment already uses. Building a
// second authorization server next to one that works would mean owning token
// lifetime, revocation, and consent semantics twice, with the two able to
// disagree.
//
// Connector credentials (rlnt_conn_) remain supported alongside this. They are
// what makes the endpoint usable from Claude Desktop and the API today,
// without an interactive browser flow.

// WellKnownProtectedResourcePath is where RFC 9728 metadata is served.
//
// Clients probe the root path; some also probe a path-scoped variant derived
// from the MCP endpoint. Both are mounted, since a 404 during discovery is
// opaque to debug from a phone.
const WellKnownProtectedResourcePath = "/.well-known/oauth-protected-resource"

// protectedResourceMetadata is the RFC 9728 document.
//
// Hand-rolled rather than taken from the SDK's oauthex type so the JSON shape
// is visible at the point it is served — this is a discovery document that
// third-party clients parse, and a silently-changed field would surface as a
// connector that simply stops working.
type protectedResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers,omitempty"`
	ScopesSupported        []string `json:"scopes_supported,omitempty"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	ResourceName           string   `json:"resource_name,omitempty"`
	ResourceDocumentation  string   `json:"resource_documentation,omitempty"`
}

// OAuthConfig describes how this deployment's connectors authenticate.
type OAuthConfig struct {
	// PublicURL is the externally reachable base URL of this server. Required
	// for the metadata document, whose `resource` field must be the canonical
	// URI a client requests a token for.
	PublicURL string

	// AuthorizationServers lists the OAuth issuers whose tokens this endpoint
	// accepts, e.g. a Supabase project URL. Empty means no OAuth issuer is
	// configured and only connector credentials work.
	AuthorizationServers []string

	// DocumentationURL is an optional human-readable pointer shown to
	// developers wiring up a client.
	DocumentationURL string

	// Scopes are the OAuth scopes advertised to clients. They must be scopes
	// the configured authorization server actually issues — an unrecognized
	// scope fails at the authorize step with "unsupported scope", before the
	// user sees anything. Empty uses the default below.
	Scopes []string
}

// defaultScopes is what a client should request.
//
// `openid` because every OIDC provider supports it and it is all this
// endpoint needs: the token answers "who is calling", and the connector grant
// answers "what may they touch".
var defaultScopes = []string{"openid"}

// scopes returns the advertised scopes.
func (c OAuthConfig) scopes() []string {
	if len(c.Scopes) > 0 {
		return c.Scopes
	}
	return defaultScopes
}

// Enabled reports whether OAuth discovery should be advertised.
func (c OAuthConfig) Enabled() bool {
	return strings.TrimSpace(c.PublicURL) != "" && len(c.AuthorizationServers) > 0
}

// resourceURI is the canonical identifier of this MCP endpoint.
//
// It must match what a client sends as the RFC 8707 `resource` parameter, so
// it is built from one place: base URL plus the mount path, no trailing slash.
func (c OAuthConfig) resourceURI() string {
	return strings.TrimSuffix(strings.TrimSpace(c.PublicURL), "/") + MountPath
}

// metadataURL is where the discovery document lives.
func (c OAuthConfig) metadataURL() string {
	return strings.TrimSuffix(strings.TrimSpace(c.PublicURL), "/") + WellKnownProtectedResourcePath
}

// NewProtectedResourceMetadataHandler serves the RFC 9728 document.
func NewProtectedResourceMetadataHandler(cfg OAuthConfig) http.Handler {
	doc := protectedResourceMetadata{
		Resource:             cfg.resourceURI(),
		AuthorizationServers: cfg.AuthorizationServers,
		// Scopes come from the AUTHORIZATION SERVER's vocabulary, not ours.
		//
		// A resource server may only advertise scopes its AS will actually
		// issue: a client reads this list, asks for those scopes, and the AS
		// rejects the request outright if it does not recognize one
		// ("unsupported scope: mcp"). Advertising a scope we invented would
		// break the flow at the authorize step, before the user ever sees a
		// consent screen.
		//
		// `openid` is the one scope every OIDC provider supports, and it is
		// sufficient here because a connector's real authority is NOT carried
		// in the token. It comes from the grant — which daemon, which tools,
		// which paths — chosen in reliant's own consent UI, where it can be
		// explained in terms a person can act on. The OAuth token establishes
		// WHO is calling; the grant establishes what they may touch.
		ScopesSupported:        cfg.scopes(),
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "Reliant Workspace",
		ResourceDocumentation:  cfg.DocumentationURL,
	}

	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		// The document is built from config, so this cannot fail in practice;
		// serving an error beats serving a malformed discovery document.
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "metadata unavailable", http.StatusInternalServerError)
		})
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Browser-based clients (ChatGPT web, Claude web) fetch this
		// cross-origin. Without CORS the discovery step fails in a way that
		// looks like the server being down.
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD, OPTIONS")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write(body)
	})
}

// challengeHeader builds the WWW-Authenticate value for a 401.
//
// The resource_metadata pointer is what turns a rejection into a usable
// discovery step: a client that receives it knows where to look, instead of
// probing well-known paths and hoping.
func (c OAuthConfig) challengeHeader() string {
	if !c.Enabled() {
		return `Bearer realm="reliant-workspace"`
	}
	return `Bearer realm="reliant-workspace", resource_metadata="` + c.metadataURL() +
		`", scope="` + strings.Join(c.scopes(), " ") + `"`
}

// MountOAuthRoutes mounts the discovery endpoints on mux.
//
// Mounting is skipped when no authorization server is configured — advertising
// a discovery document that names no issuer would send a client into a flow
// that cannot complete, which is worse than not advertising at all. Connector
// credentials keep working either way.
func MountOAuthRoutes(mux *http.ServeMux, cfg OAuthConfig, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	if !cfg.Enabled() {
		logger.Info("connector OAuth discovery not mounted: no authorization server configured; " +
			"connector credentials remain available")
		return
	}

	handler := NewProtectedResourceMetadataHandler(cfg)
	mux.Handle(WellKnownProtectedResourcePath, handler)

	// Path-scoped variant. RFC 9728 allows a resource at a sub-path to publish
	// metadata at /.well-known/oauth-protected-resource<path>, and clients
	// differ on which they probe first.
	mux.Handle(WellKnownProtectedResourcePath+MountPath, handler)

	logger.Info("connector OAuth discovery mounted",
		"metadata", cfg.metadataURL(),
		"resource", cfg.resourceURI(),
		"authorizationServers", cfg.AuthorizationServers)
}
