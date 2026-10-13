// Package httpaction runs a manifest action declaratively over HTTP: render the
// request from params, enforce the host rules, call out through the SSRF guard,
// and select/shape the response. It never makes a request the manifest's
// connection did not allow, and never one to a non-public address.
//
// forge:outbound-io
//
// A runner calls the integration's API at the manifest connection's base_url
// (and the hosts it allows), through netguard's dialer, authenticated by the
// Credential its source resolves.
//
//forge:not-a-component: built where it is used (the workflow action activity, the agent's integration tools, the integration pollers) by reliant's hand-wired server and worker, not forge's composition root — until P3c (research/forge-migration/PLAN.md)
package httpaction

import (
	"context"
	"crypto/x509"
	"net/http"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// Service runs manifest actions over HTTP. *Runner implements it.
type Service interface {
	// Run validates params against the action's schema, then performs the
	// request unauthenticated.
	Run(ctx context.Context, integration *reliantv1.IntegrationManifest, action *reliantv1.ActionSpec, params map[string]any) (*Result, error)
	// RunAuthenticated runs the action with the credential its integration
	// needs, resolved from src for the call at site (see
	// Runner.RunAuthenticated for the rules).
	RunAuthenticated(ctx context.Context, integration *reliantv1.IntegrationManifest, action *reliantv1.ActionSpec, params map[string]any, src CredentialSource, site CallSite) (*Result, error)
	// Client is the guarded HTTP client, for executors and pollers that make
	// their own requests.
	Client() *http.Client
	// WithRootCAs returns a runner that trusts pool for TLS, for tests that
	// talk to an httptest TLS server.
	WithRootCAs(pool *x509.CertPool) *Runner
	// WithExecutors returns a copy of the runner that dispatches go: actions
	// to reg.
	WithExecutors(reg *ExecutorRegistry) *Runner
}

// DialGuard builds the transport every request goes through. *netguard.Guard
// satisfies it: its dialer refuses any connection to a non-public address
// after DNS resolution.
type DialGuard interface {
	Transport() *http.Transport
}

// Deps are the runner's collaborators.
type Deps struct {
	// Guard builds the dialing transport. nil is netguard.New().
	Guard DialGuard
}
