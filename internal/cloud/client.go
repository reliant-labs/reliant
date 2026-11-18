package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/pkg/activity"
)

// Client calls a hosted control plane over Connect's HTTP+JSON binding:
// POST <base>/<package>.<Service>/<Method> with a JSON body and a JSON
// reply.
//
// WHY HTTP+JSON AND NOT A GENERATED CLIENT. A generated client would
// mean vendoring the control plane's protos into forge, which is the one
// coupling that must not ship — it would make forge's deploy story
// specific to our control plane in exactly the way forge's External
// target is deliberately NOT specific to Fly.io. Connect's JSON binding
// is a documented, stable part of the protocol and needs nothing but
// net/http, so the wire contract stays a contract rather than a shared
// build dependency.
//
// The cost is real and worth naming: forge does not get compile-time
// checking of request and response shapes against the server's protos. A
// server-side field rename surfaces as a missing value at runtime rather
// than a build failure. That is the accepted trade for independence, and
// it is why Call decodes into explicitly-declared local structs — the
// shape forge expects is written down in one place instead of inferred.
type Client struct {
	Endpoint   Endpoint
	Credential Credential
	HTTP       *http.Client
	// Via, when set, is what the client names itself as in the Forge-Via
	// header (the activity stream's "via"); "" is "cli", or "ci" inside CI.
	// The hosted MCP connector sends "mcp".
	Via string

	// elevated holds tokens obtained by ExchangeToken, keyed by the scope that
	// was missing. In memory only: they live at most an hour and are never
	// written to the credentials file.
	elevated elevationCache

	// scopes maps a procedure to the scope it is called with an elevated
	// token for: declared by the caller, or learned from a refusal.
	scopes scopeTable

	// credMu guards Credential, which a refresh from the credential helper
	// replaces.
	credMu sync.Mutex
}

// NewClient builds a Client with a bounded default timeout. A deploy CLI
// that hangs forever on an unreachable endpoint is worse than one that
// fails: CI would sit until its own job timeout with no diagnosis.
func NewClient(ep Endpoint, cred Credential) *Client {
	return &Client{
		Endpoint:   ep,
		Credential: cred,
		HTTP:       &http.Client{Timeout: 30 * time.Second},
	}
}

// Call invokes one Connect procedure. procedure is the fully-qualified
// path WITHOUT a leading slash, e.g.
// "controlplane.v1.DeployService/ListReleases".
//
// req is marshalled to JSON and out is unmarshalled from the reply. A
// non-200 becomes a *[Error] (see error.go), which carries the Connect code,
// the domain reason and the server's error details as DATA. Callers branch
// on those fields — never on the message text, which is display copy the
// server is free to improve.
//
// A procedure with a declared scope (DeclareScope), or one this client already
// saw refused for want of one, is called with a credential elevated for that
// scope from the start. Any other is called with the base credential, and a
// refusal naming a missing scope is elevated for and retried once (elevate.go).
func (c *Client) Call(ctx context.Context, procedure string, req, out any) error {
	if scope, ok := c.upfrontScope(procedure); ok {
		return c.callDeclared(ctx, procedure, req, out, scope)
	}
	return c.elevateAndRetry(ctx, procedure, req, out, c.callBase(ctx, procedure, req, out))
}

// callBase calls procedure with the base credential, refreshing it once when
// the control plane refuses it outright.
func (c *Client) callBase(ctx context.Context, procedure string, req, out any) error {
	err := c.call(ctx, procedure, req, out, c.credential().Token)
	if rejectedAsUnauthenticated(err) {
		// The credential itself was refused (expired, revoked, ...), before any
		// handler ran, so asking for a fresh one and repeating the call once
		// cannot double an effect.
		if fresh, ok := c.refreshedCredential(); ok {
			err = c.call(ctx, procedure, req, out, fresh.Token)
		}
	}
	return err
}

// callElevated calls procedure with token, an elevated token for scope, and
// trades for a new one once when the control plane refuses it outright.
func (c *Client) callElevated(ctx context.Context, procedure string, req, out any, scope, token string) error {
	err := c.call(ctx, procedure, req, out, token)
	if rejectedAsUnauthenticated(err) {
		// The elevated token we held was refused (revoked, or expired by a
		// skewed clock). Trade for a new one, once.
		if fresh, ferr := c.reelevatedToken(ctx, scope, token); ferr == nil && fresh != token {
			return c.call(ctx, procedure, req, out, fresh)
		}
	}
	return err
}

// rejectedAsUnauthenticated reports a 401: the bearer was refused, as opposed to
// what it may do (403) or an outage.
func rejectedAsUnauthenticated(err error) bool {
	var apiErr *Error
	return errors.As(err, &apiErr) && apiErr.Unauthenticated()
}

func (c *Client) credential() Credential {
	c.credMu.Lock()
	defer c.credMu.Unlock()
	return c.Credential
}

// call is one HTTP round trip with an explicit bearer.
func (c *Client) call(ctx context.Context, procedure string, req, out any, token string) error {
	// Resolve the server target before any human operation, including reads.
	// The session has no org default that can stand in for project ownership.
	projectID := c.Endpoint.ProjectID
	if token != "" && c.credential().Kind == "human" && projectID == "" && c.Endpoint.ProjectName != "" && !strings.HasSuffix(procedure, "/ResolveHostedProject") {
		var target struct {
			ProjectID string `json:"projectId"`
			OrgID     string `json:"orgId"`
		}
		if err := c.call(ctx, "controlplane.v1.DeployService/ResolveHostedProject", map[string]any{"name": c.Endpoint.ProjectName}, &target, token); err != nil {
			return err
		}
		if target.ProjectID == "" || target.OrgID == "" {
			return fmt.Errorf("control plane returned incomplete project identity")
		}
		projectID = target.ProjectID
	}
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encode %s request: %w", procedure, err)
	}

	resp, err := c.send(ctx, procedure, body, projectID, token)
	if err != nil {
		return err
	}
	// Nothing was written, so a close error carries no data loss — it can
	// only report a connection this call is already finished with.
	defer func() { _ = resp.Body.Close() }()

	// Bounded read: a misconfigured endpoint that returns a huge HTML
	// error page should not be streamed into memory in full.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("read %s response: %w", procedure, err)
	}
	if resp.StatusCode != http.StatusOK {
		return c.connectError(procedure, resp, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode %s response: %w", procedure, err)
	}
	return nil
}

// connectAttempts is how many times send tries a request whose connection
// could not be set up (dial, DNS, TLS handshake), and connectBackoff the wait
// before each retry. Package vars so a test needs no real waits.
var (
	connectAttempts = 3
	connectBackoff  = func(attempt int) time.Duration { return time.Duration(attempt) * 500 * time.Millisecond }
)

// send is one request, retried only while it never left this machine: a
// failure before the request's headers were written (a refused dial, a DNS
// failure, a TLS handshake timeout on a loaded host) reached no handler, so a
// second attempt cannot double an effect, whatever the procedure. A failure
// after the headers went out is returned as it is: the server may have acted.
func (c *Client) send(ctx context.Context, procedure string, body []byte, projectID, token string) (*http.Response, error) {
	var err error
	for attempt := 1; ; attempt++ {
		// The transport reports the write from its own goroutine.
		var wrote atomic.Bool
		trace := &httptrace.ClientTrace{WroteHeaders: func() { wrote.Store(true) }}
		var resp *http.Response
		resp, err = c.sendOnce(httptrace.WithClientTrace(ctx, trace), procedure, body, projectID, token)
		if err == nil || wrote.Load() || ctx.Err() != nil || attempt >= connectAttempts {
			return resp, err
		}
		t := time.NewTimer(connectBackoff(attempt))
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, err
		case <-t.C:
		}
	}
}

func (c *Client) sendOnce(ctx context.Context, procedure string, body []byte, projectID, token string) (*http.Response, error) {
	url := c.Endpoint.URL + "/" + strings.TrimPrefix(procedure, "/")
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build %s request: %w", procedure, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if token != "" && projectID != "" && !strings.HasSuffix(procedure, "/ResolveHostedProject") {
		httpReq.Header.Set("Forge-Project-ID", projectID)
	}
	// The credential is an OPAQUE bearer string. forge does not inspect
	// it, does not validate a prefix, and does not decode it — the issuer
	// owns its format, and a forge-side assumption about it would break
	// the day that format changed.
	if token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}
	// Forge-Via names the client to the server's activity stream
	// (forge/pkg/activity): the same command reads "via CLI" from a
	// terminal and "via CI" from a pipeline.
	if c.Via != "" {
		httpReq.Header.Set(activity.ViaHeader, c.Via)
	} else {
		httpReq.Header.Set(activity.ViaHeader, via())
	}
	// Forge-Version names this release, so a control plane that no longer
	// serves it refuses it with an upgrade instruction
	// (ReasonClientUpgradeRequired) instead of running a request whose
	// meaning has changed since this forge was built.
	httpReq.Header.Set(VersionHeader, buildinfo.Version())
	// User-Agent and X-Forge-Client name the release, platform and driving
	// agent harness, for the control plane's product analytics.
	setClientIdentity(httpReq.Header)

	resp, err := c.httpClientFor(procedure).Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call %s at %s: %w", procedure, c.Endpoint.URL, err)
	}
	return resp, nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// ContentScannedTimeout is the request timeout for the procedures in
// contentScannedProcedures. The control plane scans an unclaimed org's
// uploaded artifacts (ClamAV, Web Risk) inside these requests, within a 10m
// budget (control-plane contentscan.ScanBudget), and its gateway gives them
// 11m (CONTENT_SCAN_ROUTE_TIMEOUT). A container image's scan routinely
// exceeds the client's 30s default. A shorter client timeout only abandons a
// scan the server is still running and turns its verdict into a bare
// "context deadline exceeded".
const ContentScannedTimeout = 12 * time.Minute

// contentScannedProcedures are the control-plane procedures that may run the
// content scan before they answer.
var contentScannedProcedures = map[string]bool{
	"controlplane.v1.DeployService/RecordBundle":     true,
	"controlplane.v1.DeployService/Promote":          true,
	"controlplane.v1.DeployService/CreateDeployment": true,
	"controlplane.v1.DeployService/UpdateDeployment": true,
}

// httpClientFor is httpClient with the timeout procedure needs: a
// content-scanned procedure gets at least ContentScannedTimeout, and a
// configured transport is kept.
func (c *Client) httpClientFor(procedure string) *http.Client {
	base := c.httpClient()
	if !contentScannedProcedures[strings.TrimPrefix(procedure, "/")] ||
		base.Timeout == 0 || base.Timeout >= ContentScannedTimeout {
		return base
	}
	longer := *base
	longer.Timeout = ContentScannedTimeout
	return &longer
}

// VersionHeader is the request header this client names its forge release
// in, on every control-plane call.
const VersionHeader = "Forge-Version"

// via is the Forge-Via value for this process: "ci" under a CI runner, "cli"
// otherwise.
func via() string {
	if buildinfo.IsCI() {
		return "ci"
	}
	return "cli"
}
