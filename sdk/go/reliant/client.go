// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

// Package reliant is the Go client for Reliant's public API: start and steer
// runs, follow them to a verdict, answer their questions, and read and fire
// automations.
//
//	c, err := reliant.FromEnv() // RELIANT_SERVER_URL, RELIANT_TOKEN
//	run, err := c.Runs.Start(ctx, reliant.StartRun{
//		Workflow: "builtin://agent",
//		Message:  "fix the failing test",
//	})
//	f, err := c.Runs.Follow(ctx, run.GetId(), reliant.FollowOptions{})
//	for ev := range f.Events() { fmt.Println(ev.Event, ev.NodeID) }
//	res, err := f.Wait()
//	if !res.Succeeded { os.Exit(1) }
//
// # Authentication
//
// A caller authenticates with an API token: `reliant auth token create --name
// <app>` prints an rlat_ token with the reliant:api scope, acting as the user
// who created it. Pass it with [WithToken], or rotate it with
// [WithTokenSource]. Tokens last 90 days; the token source is where a
// rotation hooks in.
//
// A webhook trigger needs no token of its own: its URL credential is enough
// (see [NewWebhook]).
//
// # What is not here
//
// Anything that decides who a run executes as, where, or whether an automation
// runs at all (enabling, creating or editing triggers, approving) is not part
// of this API. It is not a gap to be filled by the generated clients either:
// they contain only the rpcs in proto/public-api.txt.
//
// # Stability
//
// The package follows the API's: additive within reliant.v1. Pause, Resume and
// Interrupt are beta; their semantics (positions, interrupted tools) are still
// settling.
//
// # License
//
// Apache-2.0 (the LICENSE file in this module). The rest of the Reliant
// repository is under its own license; this module is the exception.
package reliant

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1/reliantv1connect"
)

// DefaultBaseURL is Reliant's hosted API.
const DefaultBaseURL = "https://api.reliantapi.com"

// Environment variables [FromEnv] reads. They are the CLI's own, so a script
// that works with `reliant` works with this client.
const (
	EnvServerURL = "RELIANT_SERVER_URL"
	EnvToken     = "RELIANT_TOKEN"
)

// Version is the SDK version sent in the User-Agent. It is the module's
// release, set at tag time.
const Version = "0.1.0"

// TokenSource supplies the bearer token for each request. Use it for a token
// that rotates, or to forward a caller's own credential. It is called once per
// request and must be safe for concurrent use; make it cheap, or cache.
type TokenSource func(ctx context.Context) (string, error)

// Client is a Reliant API client. It is safe for concurrent use.
type Client struct {
	// Runs starts, steers, lists and follows runs.
	Runs *RunsClient
	// Gates lists what a run is waiting on and answers questions.
	Gates *GatesClient
	// Triggers reads automations, fires them, and lists their firings.
	Triggers *TriggersClient

	baseURL string
	http    *http.Client
}

// BaseURL is the server this client talks to, without a trailing slash.
func (c *Client) BaseURL() string { return c.baseURL }

// HTTPClient is the authenticated *http.Client every call goes through. Build
// a generated client from it to reach an rpc this package does not wrap:
//
//	reliantv1connect.NewRunServiceClient(c.HTTPClient(), c.BaseURL())
//
// It adds the bearer token only to requests for BaseURL's host.
func (c *Client) HTTPClient() *http.Client { return c.http }

type config struct {
	baseURL    string
	baseURLSet bool
	token      string
	source     TokenSource
	httpClient *http.Client
	loopback   bool
	anyHTTP    bool
}

// Option configures [New].
type Option func(*config)

// WithBaseURL sets the server, e.g. "https://api.reliantapi.com" (the default).
func WithBaseURL(u string) Option {
	return func(c *config) { c.baseURL, c.baseURLSet = u, true }
}

// WithToken authenticates every request with a fixed bearer token: an rlat_
// API token.
func WithToken(token string) Option {
	return func(c *config) { c.token = token }
}

// WithTokenSource authenticates each request with whatever the source returns.
// It wins over [WithToken].
func WithTokenSource(s TokenSource) Option {
	return func(c *config) { c.source = s }
}

// WithHTTPClient sets the client requests are sent with, for proxies, custom
// TLS roots, timeouts or tracing. It is not modified: the client wraps its
// Transport.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *config) { c.httpClient = hc }
}

// WithInsecureLoopback allows a plain http:// base URL when the host is the
// local machine (localhost, *.localhost, 127.0.0.0/8, ::1), for a dev server.
// It changes nothing else: anywhere else a base URL must be https, because a
// bearer token sent over plain http is readable by anyone on the path.
func WithInsecureLoopback() Option {
	return func(c *config) { c.loopback = true }
}

// WithInsecureHTTP allows a plain http:// base URL to any host, for a
// self-hosted or dev server that is not on the local machine. A bearer token
// sent over plain http is readable by anyone on the path, so use it only on a
// network you trust; the default, and WithInsecureLoopback, stay stricter. It
// changes nothing else: redirects off the base URL's origin are still refused.
func WithInsecureHTTP() Option {
	return func(c *config) { c.anyHTTP = true }
}

// New builds a client. A token is required: WithToken or WithTokenSource.
func New(opts ...Option) (*Client, error) {
	cfg := config{baseURL: DefaultBaseURL}
	for _, o := range opts {
		o(&cfg)
	}

	base, err := normalizeBaseURL(cfg.baseURL, cfg.loopback, cfg.anyHTTP)
	if err != nil {
		return nil, err
	}
	source := cfg.source
	if source == nil {
		if strings.TrimSpace(cfg.token) == "" {
			return nil, errors.New("reliant: no token: pass WithToken or WithTokenSource (create one with `reliant auth token create`)")
		}
		token := strings.TrimSpace(cfg.token)
		source = func(context.Context) (string, error) { return token, nil }
	}

	hc := cfg.httpClient
	if hc == nil {
		hc = &http.Client{}
	}
	next := hc.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	u, _ := url.Parse(base)
	authed := &http.Client{
		Transport:     &authTransport{next: next, host: u.Host, scheme: u.Scheme, source: source},
		CheckRedirect: sameOrigin(u.Scheme, u.Host, hc.CheckRedirect),
		Jar:           hc.Jar,
		Timeout:       hc.Timeout,
	}

	c := &Client{baseURL: base, http: authed}
	c.Runs = &RunsClient{c: c, runs: reliantv1connect.NewRunServiceClient(authed, base)}
	c.Gates = &GatesClient{c: c, questions: reliantv1connect.NewQuestionServiceClient(authed, base), approvals: reliantv1connect.NewApprovalServiceClient(authed, base)}
	c.Triggers = &TriggersClient{c: c, triggers: reliantv1connect.NewTriggerServiceClient(authed, base)}
	c.Runs.chat = reliantv1connect.NewChatServiceClient(authed, base)
	return c, nil
}

// FromEnv builds a client from RELIANT_SERVER_URL (default
// https://api.reliantapi.com) and RELIANT_TOKEN, and nothing else: no
// credential file, no login. Options override what the environment gave.
func FromEnv(opts ...Option) (*Client, error) {
	token := strings.TrimSpace(os.Getenv(EnvToken)) //nolint:forbidigo // the SDK's documented configuration surface, outside the app's typed config
	if token == "" {
		return nil, fmt.Errorf("reliant: %s is not set (create a token with `reliant auth token create`)", EnvToken)
	}
	base := []Option{WithToken(token)}
	if u := strings.TrimSpace(os.Getenv(EnvServerURL)); u != "" { //nolint:forbidigo // see above
		base = append(base, WithBaseURL(u))
	}
	return New(append(base, opts...)...)
}

// normalizeBaseURL validates the server URL and strips a trailing slash. A
// token only ever travels over https, except to the local machine when the
// caller asked for that.
func normalizeBaseURL(raw string, allowLoopback, allowAnyHTTP bool) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("reliant: invalid base URL %q", raw)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !allowAnyHTTP && (!allowLoopback || !isLoopbackHost(u.Hostname())) {
			return "", fmt.Errorf("reliant: base URL %q is not https; a token must not cross a network in the clear (WithInsecureLoopback permits http to the local machine only; WithInsecureHTTP permits it to any host)", raw)
		}
	default:
		return "", fmt.Errorf("reliant: base URL %q must be https", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("reliant: base URL %q must not carry credentials, a query or a fragment", raw)
	}
	return raw, nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// sameOrigin refuses a redirect off the validated origin (scheme and host)
// before net/http sends it, then defers to the caller's own policy. net/http
// re-sends a request's headers on a same-host redirect even from https to
// http, so an API call must never follow one off the origin it was validated
// for: the redirected request is not sent at all.
func sameOrigin(scheme, host string, next func(*http.Request, []*http.Request) error) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != scheme || req.URL.Host != host {
			return fmt.Errorf("reliant: refusing a redirect off %s://%s: a token never leaves the server it was issued for", scheme, host)
		}
		if next != nil {
			return next(req, via)
		}
		if len(via) >= 10 {
			return errors.New("reliant: stopped after 10 redirects")
		}
		return nil
	}
}

// authTransport adds the bearer token and the User-Agent.
type authTransport struct {
	next   http.RoundTripper
	scheme string
	host   string
	source TokenSource
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// The token goes to the server it was issued for and nowhere else. A
	// request for another origin (sameOrigin refuses redirects, so only a
	// caller's own use of the HTTP client gets here) carries no Authorization,
	// whoever set it.
	if req.URL.Host != t.host || req.URL.Scheme != t.scheme {
		if req.Header.Get("Authorization") != "" {
			req = req.Clone(req.Context())
			req.Header.Del("Authorization")
		}
		return t.next.RoundTrip(req)
	}
	token, err := t.source(req.Context())
	if err != nil {
		return nil, fmt.Errorf("reliant: token source: %w", err)
	}
	if token == "" {
		return nil, errors.New("reliant: token source returned an empty token")
	}
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+token)
	if r.Header.Get("User-Agent") == "" {
		r.Header.Set("User-Agent", "reliant-go-sdk/"+Version)
	}
	return t.next.RoundTrip(r)
}

// asConnectError reports the Connect error in err's chain, if any.
func asConnectError(err error) (*connect.Error, bool) {
	var ce *connect.Error
	return ce, errors.As(err, &ce)
}
