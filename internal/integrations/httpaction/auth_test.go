package httpaction

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/netguard"
)

// fakeCred is a credential that writes a header, carries connection params,
// and scrubs a fixed secret.
type fakeCred struct {
	id, secret string
	params     map[string]string
}

func (c fakeCred) ConnectionID() string      { return c.id }
func (c fakeCred) Params() map[string]string { return c.params }
func (c fakeCred) Apply(r *http.Request) error {
	r.Header.Set("X-Key", c.secret)
	return nil
}
func (c fakeCred) Scrub(s string) string { return strings.ReplaceAll(s, c.secret, "[redacted]") }

type fixedSource struct {
	cred Credential
	got  []CredentialRequest
}

func (s *fixedSource) Credential(_ context.Context, req CredentialRequest) (Credential, error) {
	s.got = append(s.got, req)
	if s.cred == nil {
		return nil, &CredentialError{Code: CodeFailedPrecondition, Message: "no acme connection"}
	}
	return s.cred, nil
}

// tenantRunner sends every connection to the httptest server whatever host the
// request names (the guard still vets the dialled address) and trusts the
// server's certificate, whose SANs include *.example.com. So a templated
// base_url like https://blue.acme.example.com is driven end to end, and the
// Host header shows which tenant the request was built for.
func tenantRunner(t *testing.T, srv *httptest.Server) *Runner {
	t.Helper()
	g := netguard.New()
	g.AllowLoopback = true
	u, _ := url.Parse(srv.URL)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	r := NewRunner(g).WithRootCAs(pool)
	tr := r.client.Transport.(*http.Transport)
	dial := tr.DialContext
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dial(ctx, network, u.Host)
	}
	// The certificate is for *.example.com; one wildcard level only.
	tr.TLSClientConfig.ServerName = "tenant.example.com"
	return r
}

const tenantManifest = `
id: acme
version: 1
display_name: Acme
connection:
  base_url: "https://{{ connection.params.tenant }}.acme.example.com/api"
  connection_params:
    - { name: tenant, pattern: "[a-z0-9-]+" }
  auth:
    - api_key: { in: header, name: X-Key }
actions:
  - id: thing.get
    placement: server
    params: { type: object, properties: { id: { type: string } } }
    request:
      method: GET
      path: "/things/{{ params.id }}"
      query: { site: "{{ connection.params.tenant }}" }
`

// A connection's params pick the tenant host and are visible to request
// templates; the credential is applied there and nowhere else.
func TestConnectionParamsTemplateBaseURL(t *testing.T) {
	var gotHost, gotPath, gotQuery, gotKey string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost, gotPath, gotQuery, gotKey = r.Host, r.URL.Path, r.URL.RawQuery, r.Header.Get("X-Key")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	m, err := manifest.Parse([]byte(tenantManifest), manifest.TrustCurated)
	if err != nil {
		t.Fatal(err)
	}
	src := &fixedSource{cred: fakeCred{id: "conn_1", secret: "k-secret", params: map[string]string{"tenant": "blue"}}}
	r := tenantRunner(t, srv)
	res, err := r.RunAuthenticated(context.Background(), m, m.GetActions()[0], map[string]any{"id": "7"}, src, CallSite{RunID: "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("result: %+v", res)
	}
	if !strings.HasPrefix(gotHost, "blue.acme.example.com") || gotPath != "/api/things/7" || gotQuery != "site=blue" || gotKey != "k-secret" {
		t.Errorf("request: host=%s path=%s query=%s key=%s", gotHost, gotPath, gotQuery, gotKey)
	}
	if len(src.got) != 1 || src.got[0].IntegrationID != "acme" || src.got[0].ConnectionID != "" {
		t.Errorf("an integration that requires auth asks for the owner's default: %+v", src.got)
	}
	if res.ConnectionID != "conn_1" {
		t.Errorf("connection id not recorded: %q", res.ConnectionID)
	}
}

// A param value that tries to add a label or smuggle a host is refused before
// any request is made.
func TestConnectionParamCannotPickTheDomain(t *testing.T) {
	m, err := manifest.Parse([]byte(tenantManifest), manifest.TrustCurated)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"evil.com", "a@b", "x/y", ""} {
		src := &fixedSource{cred: fakeCred{id: "c", secret: "s3cret", params: map[string]string{"tenant": bad}}}
		_, err := NewRunner(netguard.New()).RunAuthenticated(context.Background(), m, m.GetActions()[0], map[string]any{"id": "1"}, src, CallSite{RunID: "r"})
		if err == nil {
			t.Errorf("tenant %q must be refused", bad)
		}
	}
}

// An integration whose auth is required refuses to run without a credential
// source rather than sending an unauthenticated request.
func TestRequiredAuthWithoutSourceRefuses(t *testing.T) {
	m, err := manifest.Parse([]byte(tenantManifest), manifest.TrustCurated)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewRunner(netguard.New()).RunAuthenticated(context.Background(), m, m.GetActions()[0], map[string]any{"id": "1"}, nil, CallSite{RunID: "r"})
	var ce *CredentialError
	if !errors.As(err, &ce) || ce.Code != CodeFailedPrecondition {
		t.Fatalf("want FailedPrecondition, got %v", err)
	}
	// And a source that has no connection says so, typed.
	_, err = NewRunner(netguard.New()).RunAuthenticated(context.Background(), m, m.GetActions()[0], map[string]any{"id": "1"}, &fixedSource{}, CallSite{RunID: "r"})
	if !errors.As(err, &ce) || ce.Message != "no acme connection" {
		t.Fatalf("want the source's error, got %v", err)
	}
}

const executorManifest = `
id: acme
version: 1
display_name: Acme
connection:
  base_url: https://api.acme.example.com
  auth:
    - api_key: { in: header, name: X-Key }
actions:
  - id: sign
    placement: server
    executor: go:acme.test_sign
    params:
      type: object
      required: [text]
      properties:
        text: { type: string }
        upper: { type: boolean, default: false }
    output: { schema: { type: object, properties: { signed: { type: string } } } }
`

// A go: executor gets validated params (defaults applied), the resolved
// credential, and returns the same Result contract; anything the credential
// would scrub is scrubbed from its output.
func TestGoExecutorDispatch(t *testing.T) {
	m, err := manifest.Parse([]byte(executorManifest), manifest.TrustCurated)
	if err != nil {
		t.Fatal(err)
	}
	var gotParams map[string]any
	var gotCred Credential
	reg := NewExecutorRegistry()
	if err := reg.Register("acme.test_sign", func(_ context.Context, call ExecutorCall) (*Result, error) {
		gotParams, gotCred = call.Params, call.Credential
		return &Result{Data: map[string]any{"signed": call.Params["text"].(string) + "|k-secret"}, StatusCode: 200}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register("acme.test_sign", nil); err == nil {
		t.Error("registering a name twice must fail")
	}
	r := NewRunner(netguard.New()).WithExecutors(reg)
	src := &fixedSource{cred: fakeCred{id: "conn_9", secret: "k-secret"}}
	res, err := r.RunAuthenticated(context.Background(), m, m.GetActions()[0], map[string]any{"text": "hi"}, src, CallSite{RunID: "r"})
	if err != nil {
		t.Fatal(err)
	}
	if gotParams["upper"] != false || gotCred == nil || gotCred.ConnectionID() != "conn_9" {
		t.Errorf("executor got params=%v cred=%v", gotParams, gotCred)
	}
	if res.Data["signed"] != "hi|[redacted]" || strings.Contains(res.Content, "k-secret") || res.ConnectionID != "conn_9" {
		t.Errorf("result not scrubbed or not recorded: %+v", res)
	}

	// Schema validation still guards an executor.
	if _, err := r.RunAuthenticated(context.Background(), m, m.GetActions()[0], map[string]any{}, src, CallSite{RunID: "r"}); err == nil {
		t.Error("missing required param must fail before the executor runs")
	}

	// An unregistered executor is a clear error, not a panic or an HTTP call.
	_, err = NewRunner(netguard.New()).RunAuthenticated(context.Background(), m, m.GetActions()[0], map[string]any{"text": "hi"}, src, CallSite{RunID: "r"})
	if err == nil || !strings.Contains(err.Error(), `executor "acme.test_sign" is not registered`) {
		t.Errorf("unregistered executor: %v", err)
	}
}

func TestExecutorRegistryValidatesManifests(t *testing.T) {
	m, err := manifest.Parse([]byte(executorManifest), manifest.TrustCurated)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewExecutorRegistry()
	if err := reg.CheckManifests([]*reliantv1.IntegrationManifest{m}); err == nil || !strings.Contains(err.Error(), "acme.test_sign") {
		t.Errorf("a manifest naming an unregistered executor must be reported: %v", err)
	}
	_ = reg.Register("acme.test_sign", func(context.Context, ExecutorCall) (*Result, error) { return &Result{}, nil })
	if err := reg.CheckManifests([]*reliantv1.IntegrationManifest{m}); err != nil {
		t.Error(err)
	}
}
