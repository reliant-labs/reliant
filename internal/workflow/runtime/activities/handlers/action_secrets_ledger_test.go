package handlers

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/netguard/netguardtest"
	"github.com/reliant-labs/reliant/internal/stepsecrets"
)

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

func httpAction(t *testing.T, node map[string]any) *reliantv1.Node {
	t.Helper()
	with := map[string]*structpb.Value{}
	for k, v := range node {
		pv, err := structpb.NewValue(v)
		require.NoError(t, err)
		with[k] = pv
	}
	return &reliantv1.Node{Id: "sign", Type: "action", SecretRefs: []*reliantv1.NodeSecretRef{traderRef()},
		Args: &reliantv1.Node_Action{Action: &reliantv1.ActionArgs{
			Uses: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "http/request@1"}},
			With: with,
		}}}
}

// The real write-ledger decorator sits between the activity and the runner. A
// secret-carrying request goes out intact, the server echoes it back in a failed
// response, and neither the activity's output nor anything the ledger persisted
// (the intent row, the settled error) holds the value.
func TestActionWithSecretsPersistsNothingInTheWriteLedger(t *testing.T) {
	var gotAuth, gotBody, gotQuery string
	srv := netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotAuth, gotBody, gotQuery = r.Header.Get("Authorization"), string(b), r.URL.RawQuery
		w.WriteHeader(http.StatusUnauthorized)
		// An API that echoes what it was sent, including in an error.
		_, _ = w.Write([]byte("rejected token " + gotAuth + " with body " + gotBody + " query " + gotQuery))
	}))

	ledger := &ledgerCapture{}
	runner := httpaction.NewRunner(netguardtestGuard())
	runner = runner.WithRootCAs(rootPool(srv))
	a := NewActionActivityWith(catalog.MustBuiltin(), runner, nil).
		WithLedger(writeledger.New(writeledger.Deps{Store: ledger})).
		WithSecretResolver(traderResolver())

	marker := stepsecrets.Marker("TRADER_KEY")
	node := httpAction(t, map[string]any{
		"url":     srv.URL + "/v1/orders?key=" + marker,
		"method":  "POST",
		"headers": map[string]any{"Authorization": "Bearer " + marker},
		"body":    map[string]any{"token": marker},
	})

	out, _ := runSecretAction(t, a, node)

	assert.Equal(t, "Bearer "+stagingKey, gotAuth, "the request carries the real value")
	assert.Contains(t, gotBody, `stg_live/secret+value\u0026k=1`, "the JSON body carries the value, JSON-escaped")
	assert.Contains(t, gotQuery, "key=", "the query carries it too")
	assert.True(t, out.GetIsError())
	assert.EqualValues(t, 401, out.GetStatusCode())

	for name, text := range map[string]string{"action output": outputText(t, out), "ledger": ledger.dump(t)} {
		assert.NotContains(t, text, stagingKey, name)
		assert.NotContains(t, text, url.QueryEscape(stagingKey), name)
		assert.NotContains(t, text, "Bearer "+stagingKey, name)
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	require.Len(t, ledger.created, 1, "the mutating call was recorded")
	require.Len(t, ledger.done, 1)
	assert.Contains(t, ledger.done[0].Error, "rejected token")
	assert.Contains(t, ledger.done[0].Error, "***")
}

func outputText(t *testing.T, out *reliantv1.ActionOutput) string {
	t.Helper()
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	return string(raw)
}

// A secret-carrying request to a plain http URL is refused before anything is
// sent: it gets the same https-only rule a saved credential does.
func TestActionWithSecretsRefusesPlainHTTP(t *testing.T) {
	var hits int
	srv := netguardtest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte("ok"))
	}))
	a := NewActionActivityWith(catalog.MustBuiltin(), httpaction.NewRunner(netguardtestGuard()), nil).
		WithSecretResolver(traderResolver())
	node := httpAction(t, map[string]any{
		"url":     srv.URL + "/x",
		"headers": map[string]any{"Authorization": "Bearer " + stepsecrets.Marker("TRADER_KEY")},
	})
	out, _ := runSecretAction(t, a, node)

	assert.True(t, out.GetIsError())
	assert.Contains(t, out.GetContent(), "only sent over https")
	assert.NotContains(t, outputText(t, out), stagingKey)
	assert.Zero(t, hits, "nothing was sent")
}

// A redirect to another host is not followed with the secret in the request.
func TestActionWithSecretsDoesNotFollowACrossHostRedirect(t *testing.T) {
	var elsewhere int
	other := netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere++
		_, _ = w.Write([]byte("landed with " + r.Header.Get("Authorization")))
	}))
	origin := netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/landing", http.StatusFound)
	}))
	a := NewActionActivityWith(catalog.MustBuiltin(), httpaction.NewRunner(netguardtestGuard()).WithRootCAs(rootPool(origin)), nil).
		WithSecretResolver(traderResolver())
	node := httpAction(t, map[string]any{
		"url":     origin.URL + "/r",
		"headers": map[string]any{"Authorization": "Bearer " + stepsecrets.Marker("TRADER_KEY")},
	})
	out, _ := runSecretAction(t, a, node)

	assert.True(t, out.GetIsError())
	assert.Contains(t, out.GetContent(), "never sent to a second host")
	assert.Zero(t, elsewhere, "the other host was never contacted")
	assert.NotContains(t, outputText(t, out), stagingKey)
}

func netguardtestGuard() *netguard.Guard { return netguardtest.Guard() }

func rootPool(srv *httptest.Server) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return pool
}
