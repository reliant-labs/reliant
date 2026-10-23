package devidp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
)

// Client talks to a running Zitadel instance.
//
// ONE base URL, unlike a provider that splits its admin API onto a second
// port: Zitadel serves the OIDC endpoints, the resource APIs (`/v2/...`)
// and the management API (`/management/v1/...`) on a single port, and
// authorizes all of them with the same bearer token.
type Client struct {
	// Base is the address this process DIALS. Inside a deploy-time Job
	// that is the in-network address (a compose service name or a
	// cluster-local Service DNS name), which is not necessarily the
	// BROWSER-facing origin — see Host.
	Base string

	// Host overrides the HTTP Host header, for when the address this
	// process dials differs from the address the instance is REGISTERED
	// under (Base above). Empty sends the Host implied by Base.
	//
	// Zitadel is multi-tenant and resolves which instance a request
	// belongs to from the Host header, matched against the instance's
	// ExternalDomain — not from the socket the request arrived on. A Job
	// running inside the compose network dials "idp:8080", but the dev
	// instance is registered under "localhost" (the browser's origin), so
	// every call needs this override or Zitadel answers "Instance not
	// found" with an otherwise perfectly valid, correctly authenticated
	// request.
	Host string

	// Token is the personal access token of the service account declared
	// in idp-steps.yaml, which Zitadel writes to disk on first boot.
	Token string

	// TokenFile, when set, names a file holding the token, read on every use
	// so the credential can rotate without a restart. It takes precedence over
	// Token while it yields one; Token stays the fallback. See token_file.go.
	TokenFile string

	// OrgID scopes management calls to one organization. Resolved by
	// DefaultOrg when empty.
	OrgID string

	http *http.Client
}

// requestTimeout bounds a single API call. Generous for a local container,
// short enough that an unreachable IdP fails rather than hangs — WaitReady
// owns the "not up yet" case and reports it properly.
const requestTimeout = 15 * time.Second

// httpClient returns the HTTP client, building the default on first use.
func (c *Client) httpClient() *http.Client {
	if c.http == nil {
		c.http = observe.NewHTTPClient(observe.HTTPClientOptions{Timeout: requestTimeout})
	}
	return c.http
}

// Org is a Zitadel organization — the container a project and its users
// live in.
type Org struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// DefaultOrg returns the instance's first organization, which for a
// forge-scaffolded instance is the one idp-steps.yaml declared.
//
// It is looked up rather than pinned because the ID is generated: the NAME
// is declarative (it is in idp-steps.yaml), the snowflake ID it gets is
// not.
func (c *Client) DefaultOrg(ctx context.Context) (Org, error) {
	body, err := c.postJSON(ctx, "/v2/organizations/_search", map[string]any{})
	if err != nil {
		return Org{}, fmt.Errorf("search organizations: %w", err)
	}
	var out struct {
		Result []Org `json:"result"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Org{}, fmt.Errorf("decode organizations: %w", err)
	}
	if len(out.Result) == 0 {
		return Org{}, fmt.Errorf("the instance reported no organizations")
	}
	return out.Result[0], nil
}

// WaitReady blocks until the IdP answers its readiness probe, or ctx
// expires.
//
// /debug/ready is Zitadel's own unauthenticated readiness route, and it
// reports READY rather than merely listening — which matters here because
// the first boot runs the declarative setup (schema, instance, org, admin
// user, PAT) before serving, and a probe that only checked for an open
// socket would hand back a client that 404s every call.
func (c *Client) WaitReady(ctx context.Context) error {
	probe := strings.TrimRight(c.Base, "/") + "/debug/ready"
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, probe, http.NoBody)
		if err != nil {
			return err
		}
		resp, err := c.httpClient().Do(c.withHost(req))
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode < 300 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("identity provider not ready at %s: %w", c.Base, ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func (c *Client) postJSON(ctx context.Context, path string, payload any) ([]byte, error) {
	return c.sendJSON(ctx, http.MethodPost, path, payload)
}

func (c *Client) putJSON(ctx context.Context, path string, payload any) ([]byte, error) {
	return c.sendJSON(ctx, http.MethodPut, path, payload)
}

func (c *Client) getJSON(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(c.Base, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	return c.do(c.withHost(c.authorize(req)))
}

func (c *Client) sendJSON(ctx context.Context, method, path string, payload any) ([]byte, error) {
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method,
		strings.TrimRight(c.Base, "/")+path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(c.withHost(c.authorize(req)))
}

func (c *Client) authorize(req *http.Request) *http.Request {
	if tok := c.bearer(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	// The management API is org-scoped; without this header a call lands
	// on whichever org the credential's own user belongs to.
	if c.OrgID != "" {
		req.Header.Set("x-zitadel-orgid", c.OrgID)
	}
	return req
}

// withHost applies the Host override. Set on req.Host rather than via
// Header.Set("Host", …), which net/http ignores for the request line.
func (c *Client) withHost(req *http.Request) *http.Request {
	if c.Host != "" {
		req.Host = c.Host
	}
	return req
}

// do performs the request and returns the body, converting a non-2xx into
// an error that carries the provider's own message — the API reports
// precisely what it rejected, and paraphrasing it loses the only useful
// detail.
//
// Zitadel answers some errors 200-with-an-error-body over its gRPC-gateway
// (a `code` field and a `message`), so a status check alone is not enough
// to tell success from failure.
func (c *Client) do(req *http.Request) ([]byte, error) {
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, &apiError{
			method: req.Method,
			path:   req.URL.Path,
			status: resp.StatusCode,
			body:   strings.TrimSpace(string(body)),
		}
	}
	if readErr != nil {
		return nil, readErr
	}
	if err := gatewayError(body); err != nil {
		return nil, fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, err)
	}
	return body, nil
}

// apiError is a non-2xx answer from the issuer.
//
// Error() renders the provider's own body verbatim, because the API reports
// precisely what it rejected. The structured fields exist for the few
// callers that must BRANCH on the rejection rather than just report it —
// a password-reset form has to tell "wrong code" from "weak password", and
// the only reliable discriminator is Zitadel's stable error id (see
// errorID), not the localized message.
type apiError struct {
	method string
	path   string
	status int
	body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.method, e.path, e.status, e.body)
}

// errorID returns the stable id and the human message from Zitadel's error
// envelope — `{"code":3,"message":"… (DOMAIN-HuJf6)","details":[{"id":
// "DOMAIN-HuJf6","message":"Password is too short"}]}` — or empty strings
// when the body is not that shape.
//
// The id is what to branch on: it is stable across versions and locales,
// where the message is translated.
func (e *apiError) errorID() (id, message string) {
	var envelope struct {
		Details []struct {
			ID      string `json:"id"`
			Message string `json:"message"`
		} `json:"details"`
	}
	if err := json.Unmarshal([]byte(e.body), &envelope); err != nil || len(envelope.Details) == 0 {
		return "", ""
	}
	return envelope.Details[0].ID, envelope.Details[0].Message
}

// noChangesID is the error Zitadel returns when an update would leave the
// resource exactly as it is. Matched on the stable error ID rather than the
// human-readable message, which is localized.
const noChangesID = "COMMAND-1m88i"

// isNoChangesErr reports whether err is Zitadel's "the resource already has
// these values" refusal — the expected answer when an idempotent command
// runs a second time and everything already agrees.
func isNoChangesErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), noChangesID)
}

// gatewayError reports the error in a 2xx body that is actually a gRPC
// status, and nil for a normal response.
func gatewayError(body []byte) error {
	var probe struct {
		Code    *int   `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil // not a JSON object; the caller's own decode will judge it
	}
	if probe.Code == nil || *probe.Code == 0 || probe.Message == "" {
		return nil
	}
	return fmt.Errorf("%s (code %d)", probe.Message, *probe.Code)
}
