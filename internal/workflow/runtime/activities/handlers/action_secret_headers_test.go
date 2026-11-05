package handlers

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/log"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/netguard/netguardtest"
	"github.com/reliant-labs/reliant/internal/stepsecrets"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// A declared secret reaches an action in exactly one place: a header slot of
// http/request@1, applied to the outgoing request as a credential. These tests
// pin that the value is on the wire and nowhere a step can print: not in the
// output, the write ledger, the activity input, a log line or an error.

const stagingKey = "stg_live/secret+value&k=1"

type fakeResolver struct {
	mu     sync.Mutex
	values map[stepsecrets.Ref]string
	err    error
	calls  []fakeResolveCall
}

type fakeResolveCall struct {
	runID, nodeID string
	refs          []stepsecrets.Ref
}

func (f *fakeResolver) ResolveSecrets(_ context.Context, runID, nodeID string, refs []stepsecrets.Ref) (map[stepsecrets.Ref]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeResolveCall{runID, nodeID, refs})
	if f.err != nil {
		return nil, f.err
	}
	out := map[stepsecrets.Ref]string{}
	for _, r := range refs {
		v, ok := f.values[r]
		if !ok {
			return nil, errors.New("secret resolution response is incomplete")
		}
		out[r] = v
	}
	return out, nil
}

func traderResolver() *fakeResolver {
	return &fakeResolver{values: map[stepsecrets.Ref]string{
		{Environment: "trader/staging", Name: "TRADER_KEY"}: stagingKey,
		{Environment: "trader/staging", Name: "OTHER"}:      "other-secret-value",
	}}
}

func secretRef(name string) *reliantv1.NodeSecretRef {
	return &reliantv1.NodeSecretRef{Name: name, Environment: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "trader/staging"}}}
}

func traderRef() *reliantv1.NodeSecretRef { return secretRef("TRADER_KEY") }

// capturedLogger records every line the activity logs, with its key/value pairs.
type capturedLogger struct {
	mu    sync.Mutex
	lines []string
}

func (c *capturedLogger) add(level, msg string, kv []interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, fmt.Sprintf("%s %s %v", level, msg, kv))
}
func (c *capturedLogger) Debug(m string, kv ...interface{}) { c.add("DEBUG", m, kv) }
func (c *capturedLogger) Info(m string, kv ...interface{})  { c.add("INFO", m, kv) }
func (c *capturedLogger) Warn(m string, kv ...interface{})  { c.add("WARN", m, kv) }
func (c *capturedLogger) Error(m string, kv ...interface{}) { c.add("ERROR", m, kv) }
func (c *capturedLogger) all() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.lines, "\n")
}

var _ log.Logger = (*capturedLogger)(nil)

type ledgerCapture struct {
	mu      sync.Mutex
	created []core.IntegrationWrite
	done    []core.IntegrationWriteResult
}

func (c *ledgerCapture) CreateIntegrationWrite(_ context.Context, w *core.IntegrationWrite) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.created = append(c.created, *w)
	return nil
}

func (c *ledgerCapture) CompleteIntegrationWrite(_ context.Context, _ string, res core.IntegrationWriteResult) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.done = append(c.done, res)
	return true, nil
}

func (c *ledgerCapture) dump(t *testing.T) string {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, err := json.Marshal(map[string]any{"created": c.created, "done": c.done})
	require.NoError(t, err)
	return string(raw)
}

func actionNode(t *testing.T, uses string, with map[string]any, refs ...*reliantv1.NodeSecretRef) *reliantv1.Node {
	t.Helper()
	pw := map[string]*structpb.Value{}
	for k, v := range with {
		pv, err := structpb.NewValue(v)
		require.NoError(t, err)
		pw[k] = pv
	}
	return &reliantv1.Node{Id: "call", Type: "action", SecretRefs: refs,
		Args: &reliantv1.Node_Action{Action: &reliantv1.ActionArgs{
			Uses: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: uses}},
			With: pw,
		}}}
}

func httpAction(t *testing.T, with map[string]any) *reliantv1.Node {
	return actionNode(t, "http/request@1", with, traderRef())
}

// runSecretAction runs the activity the way the worker does and returns its
// output, its input, and every line it logged.
func runSecretAction(t *testing.T, a *ActionActivity, node *reliantv1.Node) (*reliantv1.ActionOutput, types.ActivityInput, string) {
	t.Helper()
	in := types.ActivityInput{Runtime: types.RuntimeContext{WorkflowID: "run-1", StepID: "call"}, Node: node}
	logs := &capturedLogger{}
	suite := &temporaltest.WorkflowTestSuite{}
	suite.SetLogger(logs)
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(a.Execute)
	val, err := env.ExecuteActivity(a.Execute, in)
	require.NoError(t, err)
	var out reliantv1.ActionOutput
	require.NoError(t, val.Get(&out))
	return &out, in, logs.all()
}

func outputText(t *testing.T, out *reliantv1.ActionOutput) string {
	t.Helper()
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	return string(raw)
}

func netguardtestGuard() *netguard.Guard { return netguardtest.Guard() }

func rootPool(srv *httptest.Server) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return pool
}

func httpActivity(srv *httptest.Server, source httpaction.CredentialSource, resolver SecretResolver) *ActionActivity {
	runner := httpaction.NewRunner(netguardtestGuard())
	if srv != nil && srv.TLS != nil {
		runner = runner.WithRootCAs(rootPool(srv))
	}
	return NewActionActivityWith(catalog.MustBuiltin(), runner, source).WithSecretResolver(resolver)
}

// The header slot: the value is on the wire, in the header the author named,
// and nowhere else — not the output, the ledger (intent row and settled
// result), the activity input, or any log line.
func TestSecretHeaderReachesTheRequestAndNothingTheStepRecords(t *testing.T) {
	var gotAuth, gotBody, gotQuery string
	srv := netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotAuth, gotBody, gotQuery = r.Header.Get("Authorization"), string(b), r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"query":"` + gotQuery + `"}`))
	}))
	ledger := &ledgerCapture{}
	resolver := traderResolver()
	a := httpActivity(srv, nil, resolver).WithLedger(writeledger.New(writeledger.Deps{Store: ledger}))

	node := httpAction(t, map[string]any{
		"url":     srv.URL + "/v1/orders?status=open",
		"method":  "POST",
		"headers": map[string]any{"Authorization": "Bearer " + stepsecrets.Marker("TRADER_KEY"), "Accept": "application/json"},
		"body":    map[string]any{"qty": 3},
	})
	out, in, logs := runSecretAction(t, a, node)

	require.False(t, out.GetIsError(), out.GetContent())
	assert.Equal(t, "Bearer "+stagingKey, gotAuth, "the request carries the value in its header slot")
	assert.NotContains(t, gotBody, stagingKey)
	assert.NotContains(t, gotQuery, stagingKey)

	require.Len(t, resolver.calls, 1)
	assert.Equal(t, fakeResolveCall{"run-1", "call", []stepsecrets.Ref{{Environment: "trader/staging", Name: "TRADER_KEY"}}}, resolver.calls[0])

	input, err := json.Marshal(in)
	require.NoError(t, err)
	for name, text := range map[string]string{
		"action output": outputText(t, out), "ledger": ledger.dump(t), "activity input": string(input), "logs": logs,
	} {
		assert.NotContains(t, text, stagingKey, name)
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	require.Len(t, ledger.created, 1, "the mutating call was recorded")
	require.Len(t, ledger.done, 1)
}

// The value is not the step's to print: a failed call's error and status are
// returned as they are, and they hold no value because the request's params
// never did.
func TestSecretHeaderFailureCarriesNoValue(t *testing.T) {
	srv := netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("bad token for " + r.URL.Path))
	}))
	ledger := &ledgerCapture{}
	a := httpActivity(srv, nil, traderResolver()).WithLedger(writeledger.New(writeledger.Deps{Store: ledger}))
	out, _, logs := runSecretAction(t, a, httpAction(t, map[string]any{
		"url": srv.URL + "/v1", "method": "POST",
		"headers": map[string]any{"X-Api-Key": stepsecrets.Marker("TRADER_KEY")},
	}))
	assert.True(t, out.GetIsError())
	assert.EqualValues(t, 401, out.GetStatusCode())
	for name, text := range map[string]string{"output": outputText(t, out), "ledger": ledger.dump(t), "logs": logs} {
		assert.NotContains(t, text, stagingKey, name)
	}
}

// A step cannot echo a secret: a marker anywhere but a header slot — even one
// that got past validation — fails the step before anything is resolved or sent.
func TestASecretOutsideAHeaderSlotFailsBeforeResolvingOrSending(t *testing.T) {
	marker := stepsecrets.Marker("TRADER_KEY")
	cases := map[string]map[string]any{
		"url":                  {"url": "https://example.test/x?k=" + marker},
		"query":                {"url": "https://example.test/x", "query": map[string]any{"k": marker}},
		"body":                 {"url": "https://example.test/x", "method": "POST", "body": map[string]any{"token": marker}},
		"text body":            {"url": "https://example.test/x", "method": "POST", "body": "key=" + marker, "body_type": "text"},
		"header with a suffix": {"url": "https://example.test/x", "headers": map[string]any{"Authorization": marker + " trailing"}},
		"two in one header":    {"url": "https://example.test/x", "headers": map[string]any{"Authorization": marker + marker}},
		"a header name":        {"url": "https://example.test/x", "headers": map[string]any{marker: "x"}},
		"a refused header":     {"url": "https://example.test/x", "headers": map[string]any{"Host": marker}},
		"a proxy header":       {"url": "https://example.test/x", "headers": map[string]any{"Proxy-Authorization": marker}},
		"twice, by case":       {"url": "https://example.test/x", "headers": map[string]any{"authorization": "plain", "Authorization": marker}},
		"connection":           {"url": "https://example.test/x", "connection": marker},
	}
	for name, with := range cases {
		t.Run(name, func(t *testing.T) {
			resolver := traderResolver()
			a := httpActivity(nil, nil, resolver)
			out, _, _ := runSecretAction(t, a, httpAction(t, with))
			assert.True(t, out.GetIsError())
			assert.Equal(t, "invalid_argument", out.GetErrorCode(), out.GetContent())
			assert.Empty(t, resolver.calls, "nothing was resolved")
		})
	}

	t.Run("any other action", func(t *testing.T) {
		m, err := manifest.Parse([]byte(signManifest), manifest.TrustCurated)
		require.NoError(t, err)
		reg := httpaction.NewExecutorRegistry()
		require.NoError(t, reg.Register("signer.sign", func(context.Context, httpaction.ExecutorCall) (*httpaction.Result, error) {
			t.Fatal("a secret outside a header slot must not reach an executor")
			return nil, nil
		}))
		resolver := traderResolver()
		a := NewActionActivityWith(catalog.New([]*reliantv1.IntegrationManifest{m}),
			httpaction.NewRunner(netguard.New()).WithExecutors(reg), staticCred{id: "conn_s", secret: "k-secret-9"}).WithSecretResolver(resolver)
		out, _, _ := runSecretAction(t, a, actionNode(t, "signer/sign@1", map[string]any{"text": "Bearer " + marker}, traderRef()))
		assert.Equal(t, "invalid_argument", out.GetErrorCode(), out.GetContent())
		assert.Empty(t, resolver.calls)
	})
}

// Only what the step's own slots name is resolved, even when the workflow
// declares more.
func TestOnlyTheSlotsSecretsAreResolved(t *testing.T) {
	srv := netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	resolver := traderResolver()
	node := actionNode(t, "http/request@1", map[string]any{
		"url": srv.URL, "headers": map[string]any{"Authorization": "Bearer " + stepsecrets.Marker("TRADER_KEY")},
	}, traderRef(), secretRef("OTHER"))
	out, _, _ := runSecretAction(t, httpActivity(srv, nil, resolver), node)
	require.False(t, out.GetIsError(), out.GetContent())
	require.Len(t, resolver.calls, 1)
	assert.Equal(t, []stepsecrets.Ref{{Environment: "trader/staging", Name: "TRADER_KEY"}}, resolver.calls[0].refs)
}

func TestSecretHeaderNamingAnUndeclaredSecretFails(t *testing.T) {
	resolver := traderResolver()
	node := actionNode(t, "http/request@1", map[string]any{
		"url": "https://example.test/x", "headers": map[string]any{"Authorization": stepsecrets.Marker("OTHER")},
	}, traderRef())
	out, _, _ := runSecretAction(t, httpActivity(nil, nil, resolver), node)
	assert.Equal(t, "invalid_argument", out.GetErrorCode())
	assert.Empty(t, resolver.calls)
}

func TestSecretHeaderDeniedShowsTheControlPlaneReason(t *testing.T) {
	reason := "TRADER_KEY in trader/staging is not available to this workflow: add it to the secret's access list"
	var hits atomic.Int32
	srv := netguardtest.NewTLSServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	a := httpActivity(srv, nil, &fakeResolver{err: &stepsecrets.DeniedError{Reason: reason}})
	out, _, _ := runSecretAction(t, a, httpAction(t, map[string]any{
		"url": srv.URL, "headers": map[string]any{"Authorization": stepsecrets.Marker("TRADER_KEY")},
	}))
	assert.Equal(t, "permission_denied", out.GetErrorCode())
	assert.Equal(t, reason, out.GetContent())
	assert.Zero(t, hits.Load(), "nothing was sent")
}

func TestSecretHeaderWithNoResolverFailsInsteadOfSendingThePlaceholder(t *testing.T) {
	var hits atomic.Int32
	srv := netguardtest.NewTLSServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	out, _, _ := runSecretAction(t, httpActivity(srv, nil, nil), httpAction(t, map[string]any{
		"url": srv.URL, "headers": map[string]any{"Authorization": stepsecrets.Marker("TRADER_KEY")},
	}))
	assert.Equal(t, "failed_precondition", out.GetErrorCode())
	assert.Contains(t, out.GetContent(), "no secret resolver")
	assert.Zero(t, hits.Load())
}

func TestAnActionWithoutSecretsNeverCallsTheResolver(t *testing.T) {
	srv := netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	resolver := traderResolver()
	out, _, _ := runSecretAction(t, httpActivity(srv, nil, resolver), httpAction(t, map[string]any{
		"url": srv.URL, "headers": map[string]any{"Authorization": "Bearer public"},
	}))
	assert.False(t, out.GetIsError(), out.GetContent())
	assert.Empty(t, resolver.calls)
}

// A value that cannot be a header value (a line break would split the request)
// fails without sending; the message names the secret, never the value.
func TestSecretHeaderRefusesAValueThatIsNotAHeaderValue(t *testing.T) {
	var hits atomic.Int32
	srv := netguardtest.NewTLSServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	resolver := &fakeResolver{values: map[stepsecrets.Ref]string{
		{Environment: "trader/staging", Name: "TRADER_KEY"}: "line-one\r\nX-Injected: yes",
	}}
	out, _, _ := runSecretAction(t, httpActivity(srv, nil, resolver), httpAction(t, map[string]any{
		"url": srv.URL, "headers": map[string]any{"Authorization": stepsecrets.Marker("TRADER_KEY")},
	}))
	assert.Equal(t, "invalid_argument", out.GetErrorCode())
	assert.Contains(t, out.GetContent(), "TRADER_KEY")
	assert.NotContains(t, out.GetContent(), "line-one")
	assert.Zero(t, hits.Load())
}

// The slot is a credential, so the call gets a saved credential's transport
// rules: https only, and never a second host.
func TestSecretHeaderRefusesPlainHTTP(t *testing.T) {
	var hits atomic.Int32
	srv := netguardtest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	out, _, _ := runSecretAction(t, httpActivity(srv, nil, traderResolver()), httpAction(t, map[string]any{
		"url": srv.URL + "/x", "headers": map[string]any{"Authorization": "Bearer " + stepsecrets.Marker("TRADER_KEY")},
	}))
	assert.True(t, out.GetIsError())
	assert.Contains(t, out.GetContent(), "only sent over https")
	assert.Zero(t, hits.Load(), "nothing was sent")
}

func TestSecretHeaderDoesNotFollowACrossHostRedirect(t *testing.T) {
	var elsewhere atomic.Int32
	other := netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere.Add(1) }))
	origin := netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/landing", http.StatusFound)
	}))
	out, _, _ := runSecretAction(t, httpActivity(origin, nil, traderResolver()), httpAction(t, map[string]any{
		"url": origin.URL + "/r", "headers": map[string]any{"Authorization": "Bearer " + stepsecrets.Marker("TRADER_KEY")},
	}))
	assert.True(t, out.GetIsError())
	assert.Contains(t, out.GetContent(), "never sent to a second host")
	assert.Zero(t, elsewhere.Load(), "the other host was never contacted")

	// Control: the same redirect is followed without a secret header.
	out, _, _ = runSecretAction(t, httpActivity(origin, nil, traderResolver()), httpAction(t, map[string]any{"url": origin.URL + "/r"}))
	assert.EqualValues(t, 1, elsewhere.Load(), "an ordinary call follows it: %s", out.GetContent())
}

// A saved connection and a secret header together: both reach the request, and
// the call is still held to its first host.
func TestSecretHeaderStacksOnASavedConnection(t *testing.T) {
	var gotKey, gotAuth string
	srv := netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotAuth = r.Header.Get("X-Key"), r.Header.Get("X-Trader-Auth")
		_, _ = w.Write([]byte("ok"))
	}))
	conn := staticCred{id: "conn_h", secret: "conn-secret-7"}
	out, _, _ := runSecretAction(t, httpActivity(srv, conn, traderResolver()), httpAction(t, map[string]any{
		"url": srv.URL, "connection": "conn_h", "headers": map[string]any{"X-Trader-Auth": stepsecrets.Marker("TRADER_KEY")},
	}))
	require.False(t, out.GetIsError(), out.GetContent())
	assert.Equal(t, "conn-secret-7", gotKey, "the connection applied its credential")
	assert.Equal(t, stagingKey, gotAuth, "the step applied its own")
	assert.Equal(t, "conn_h", out.GetConnectionId())
	assert.NotContains(t, outputText(t, out), stagingKey)
}

func TestSecretHeaderCannotRideAHostedUse(t *testing.T) {
	resolver := traderResolver()
	node := httpAction(t, map[string]any{"url": "https://example.test", "headers": map[string]any{"Authorization": stepsecrets.Marker("TRADER_KEY")}})
	node.GetAction().HostedUse = &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "grant-1"}}
	out, _, _ := runSecretAction(t, httpActivity(nil, nil, resolver), node)
	assert.Equal(t, "invalid_argument", out.GetErrorCode())
	assert.Empty(t, resolver.calls)
}

// keyedManifest is an integration whose api_key method names its header and
// prefix, served by srv.
func keyedManifest(t *testing.T, srv *httptest.Server) *reliantv1.IntegrationManifest {
	t.Helper()
	m, err := manifest.Parse([]byte(`
id: keyed
version: 1
display_name: Keyed
connection:
  base_url: `+srv.URL+`
  auth:
    - api_key: { in: header, name: X-Api-Key, prefix: "Key " }
actions:
  - id: whoami
    placement: server
    mutates: false
    request: { method: GET, path: /me }
`), manifest.TrustCurated)
	require.NoError(t, err)
	return m
}

func keyedActivity(t *testing.T, srv *httptest.Server, source httpaction.CredentialSource, resolver SecretResolver) *ActionActivity {
	runner := httpaction.NewRunner(netguardtestGuard()).WithRootCAs(rootPool(srv))
	return NewActionActivityWith(catalog.New([]*reliantv1.IntegrationManifest{keyedManifest(t, srv)}), runner, source).WithSecretResolver(resolver)
}

func credentialNode(t *testing.T, secret string, with map[string]any) *reliantv1.Node {
	node := actionNode(t, "keyed/whoami@1", with, traderRef(), secretRef("OTHER"))
	node.GetAction().Credential = &reliantv1.ActionCredential{Secret: secret}
	return node
}

// The credential slot: the secret is written where the integration's api_key
// method puts a key, with its prefix, and nowhere the step records.
func TestActionCredentialIsAppliedThroughTheAPIKeyMethod(t *testing.T) {
	var gotKey string
	srv := netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Api-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login":"bot"}`))
	}))
	resolver := traderResolver()
	ledger := &ledgerCapture{}
	a := keyedActivity(t, srv, staticCred{id: "conn_never", secret: "must-not-be-used"}, resolver).WithLedger(writeledger.New(writeledger.Deps{Store: ledger}))
	out, in, logs := runSecretAction(t, a, credentialNode(t, "TRADER_KEY", nil))

	require.False(t, out.GetIsError(), out.GetContent())
	assert.Equal(t, "Key "+stagingKey, gotKey, "the manifest's header and prefix carry the value")
	assert.Empty(t, out.GetConnectionId(), "no saved connection was used")
	require.Len(t, resolver.calls, 1)
	assert.Equal(t, []stepsecrets.Ref{{Environment: "trader/staging", Name: "TRADER_KEY"}}, resolver.calls[0].refs, "only the credential's secret is resolved")
	input, err := json.Marshal(in)
	require.NoError(t, err)
	for name, text := range map[string]string{"output": outputText(t, out), "ledger": ledger.dump(t), "input": string(input), "logs": logs} {
		assert.NotContains(t, text, stagingKey, name)
	}
}

func TestActionCredentialIsRefusedWhereItCannotStandInForAConnection(t *testing.T) {
	srv := netguardtest.NewTLSServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a refused credential must not send anything")
	}))
	cases := map[string]func() (*ActionActivity, *reliantv1.Node, *fakeResolver){
		"with a connection": func() (*ActionActivity, *reliantv1.Node, *fakeResolver) {
			r := traderResolver()
			n := credentialNode(t, "TRADER_KEY", nil)
			n.GetAction().Connection = &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "conn_1"}}
			return keyedActivity(t, srv, staticCred{id: "conn_1", secret: "s"}, r), n, r
		},
		"a header slot too": func() (*ActionActivity, *reliantv1.Node, *fakeResolver) {
			r := traderResolver()
			n := actionNode(t, "http/request@1", map[string]any{"url": srv.URL, "headers": map[string]any{"X-A": stepsecrets.Marker("TRADER_KEY")}}, traderRef())
			n.GetAction().Credential = &reliantv1.ActionCredential{Secret: "TRADER_KEY"}
			return httpActivity(srv, nil, r), n, r
		},
		"an integration whose header is open": func() (*ActionActivity, *reliantv1.Node, *fakeResolver) {
			r := traderResolver()
			n := actionNode(t, "http/request@1", map[string]any{"url": srv.URL}, traderRef())
			n.GetAction().Credential = &reliantv1.ActionCredential{Secret: "TRADER_KEY"}
			return httpActivity(srv, nil, r), n, r
		},
		"an undeclared secret": func() (*ActionActivity, *reliantv1.Node, *fakeResolver) {
			r := traderResolver()
			return keyedActivity(t, srv, nil, r), credentialNode(t, "NOT_DECLARED", nil), r
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			a, node, resolver := setup()
			out, _, _ := runSecretAction(t, a, node)
			assert.Equal(t, "invalid_argument", out.GetErrorCode(), out.GetContent())
			assert.Empty(t, resolver.calls, "nothing was resolved")
		})
	}

	t.Run("an automation acting as the app", func(t *testing.T) {
		resolver := traderResolver()
		a := keyedActivity(t, srv, nil, resolver)
		in := types.ActivityInput{Runtime: types.RuntimeContext{WorkflowID: "run-1", StepID: "call", ActingIdentity: httpaction.IdentityApp}, Node: credentialNode(t, "TRADER_KEY", nil)}
		var suite temporaltest.WorkflowTestSuite
		env := suite.NewTestActivityEnvironment()
		env.RegisterActivity(a.Execute)
		val, err := env.ExecuteActivity(a.Execute, in)
		require.NoError(t, err)
		var out reliantv1.ActionOutput
		require.NoError(t, val.Get(&out))
		assert.Equal(t, "invalid_argument", out.GetErrorCode(), out.GetContent())
		assert.Empty(t, resolver.calls)
	})
}

// A credential holds the call to the integration's host, as a connection does.
func TestActionCredentialDoesNotFollowACrossHostRedirect(t *testing.T) {
	var elsewhere atomic.Int32
	other := netguardtest.NewTLSServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	origin := netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/landing", http.StatusFound)
	}))
	out, _, _ := runSecretAction(t, keyedActivity(t, origin, nil, traderResolver()), credentialNode(t, "TRADER_KEY", nil))
	assert.True(t, out.GetIsError())
	assert.Zero(t, elsewhere.Load(), "the other host was never contacted")
	assert.NotContains(t, outputText(t, out), stagingKey)
}
