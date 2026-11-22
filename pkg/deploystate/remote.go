package deploystate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
)

// Remote implements [Store] against an HTTP+JSON endpoint. Reliant is
// the first implementor of the server side; the protocol is written down
// here so it is not.
//
// # Why this exists in forge rather than only in Reliant
//
// An interface with one implementation is not a seam, it is a type
// declaration with extra steps. Shipping the second implementation is
// what proves the boundary holds — and it did its job: writing this is
// what established that the interface needed no transaction, no lease
// and no cursor, because nothing here wanted one.
//
// # Why HTTP+JSON and not a generated client
//
// The same reason forge chose REST over a vendored SDK twice already.
// A generated client drags a protocol runtime into forge/pkg, which
// every forge APP inherits — and the shapes on the wire here are five
// verbs over two flat structs. `net/http` covers it with no new module.
//
// # The protocol
//
//	GET    {base}/v1/state/{env}/{provider}/{service}   -> 200 Record | 404
//	PUT    {base}/v1/state/{env}/{provider}/{service}   <- Record, 204
//	GET    {base}/v1/state/{env}                        -> 200 {"records":[...]}
//	GET    {base}/v1/policy/{env}                       -> 200 {"policy":"..."} | 404
//	PUT    {base}/v1/policy/{env}                       <- {"policy":"..."}, 204
//
// Every path segment is URL-escaped, so a service name with a slash
// cannot reach a sibling route.
type Remote struct {
	base  string
	http  *http.Client
	token string
}

var _ Store = (*Remote)(nil)

// RemoteOption configures a Remote.
type RemoteOption func(*Remote)

// WithHTTPClient supplies the client to use. Callers with their own
// retry, tracing or auth transport pass it here rather than having forge
// invent a second policy alongside theirs.
func WithHTTPClient(c *http.Client) RemoteOption {
	return func(r *Remote) {
		if c != nil {
			r.http = c
		}
	}
}

// WithBearerToken sets an Authorization header on every request.
func WithBearerToken(token string) RemoteOption {
	return func(r *Remote) { r.token = token }
}

// NewRemote builds a Remote against a base URL ("https://host/api").
//
// The default client carries a 30s timeout. A Store call sits inside a
// reconcile pass that holds a lease with its own TTL, and a request with
// no timeout outlives the lease that authorized it — at which point the
// write it eventually makes is a stale write wearing a fresh timestamp,
// which is the failure the whole fence design upstream exists to catch.
// Better to fail the pass and let the next one hold a real lease.
func NewRemote(baseURL string, opts ...RemoteOption) *Remote {
	r := &Remote{
		base: strings.TrimSuffix(baseURL, "/"),
		http: observe.NewHTTPClient(observe.HTTPClientOptions{Timeout: 30 * time.Second}),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func (r *Remote) do(ctx context.Context, method, path string, body, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("deploystate: marshal request: %w", err)
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, r.base+path, reader)
	if err != nil {
		return 0, fmt.Errorf("deploystate: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}

	resp, err := r.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("deploystate: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return resp.StatusCode, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Bounded read: an error body is for a human, and an endpoint
		// that answers an error with a gigabyte should not be able to
		// exhaust the client that asked.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return resp.StatusCode, fmt.Errorf("deploystate: %s %s: %s: %s",
			method, path, resp.Status, strings.TrimSpace(string(snippet)))
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("deploystate: decode %s %s: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

func (r *Remote) statePath(k Key) string {
	return "/v1/state/" + url.PathEscape(k.Env) +
		"/" + url.PathEscape(k.Provider) +
		"/" + url.PathEscape(k.Service)
}

// Get fetches one record. A 404 becomes ErrNotFound so callers branch on
// the same sentinel regardless of which backend they hold.
func (r *Remote) Get(ctx context.Context, key Key) (Record, error) {
	if !key.Valid() {
		return Record{}, fmt.Errorf("deploystate: incomplete key %q", key)
	}
	var rec Record
	status, err := r.do(ctx, http.MethodGet, r.statePath(key), nil, &rec)
	if err != nil {
		return Record{}, err
	}
	if status == http.StatusNotFound {
		return Record{}, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	rec.Key = key
	return rec, nil
}

// Put writes one record.
func (r *Remote) Put(ctx context.Context, rec Record) error {
	if !rec.Key.Valid() {
		return fmt.Errorf("deploystate: incomplete key %q", rec.Key)
	}
	_, err := r.do(ctx, http.MethodPut, r.statePath(rec.Key), rec, nil)
	return err
}

// listResponse wraps the list so the endpoint can grow a sibling field
// later without changing a bare-array body into an object, which is a
// breaking change for every existing client.
type listResponse struct {
	Records []Record `json:"records"`
}

// List returns every record for one environment.
func (r *Remote) List(ctx context.Context, env string) ([]Record, error) {
	if env == "" {
		return nil, fmt.Errorf("deploystate: List requires an environment")
	}
	var body listResponse
	status, err := r.do(ctx, http.MethodGet, "/v1/state/"+url.PathEscape(env), nil, &body)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, nil
	}
	return body.Records, nil
}

// Policy reads the environment's policy.
//
// A 404 yields PolicyObserve, not an error — matching [Local] exactly.
// The direction is what matters: a backend that has never heard of this
// environment must not be able to produce a converge verdict.
func (r *Remote) Policy(ctx context.Context, env string) (Policy, error) {
	if env == "" {
		return PolicyObserve, fmt.Errorf("deploystate: Policy requires an environment")
	}
	var pf policyFile
	status, err := r.do(ctx, http.MethodGet, "/v1/policy/"+url.PathEscape(env), nil, &pf)
	if err != nil {
		return PolicyObserve, err
	}
	if status == http.StatusNotFound {
		return PolicyObserve, nil
	}
	return pf.Policy, nil
}

// SetPolicy writes the environment's policy.
func (r *Remote) SetPolicy(ctx context.Context, env string, p Policy) error {
	if env == "" {
		return fmt.Errorf("deploystate: SetPolicy requires an environment")
	}
	_, err := r.do(ctx, http.MethodPut, "/v1/policy/"+url.PathEscape(env), policyFile{Policy: p}, nil)
	return err
}
