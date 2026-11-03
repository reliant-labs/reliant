package httpaction

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/netguard/netguardtest"
	"github.com/reliant-labs/reliant/internal/stepsecrets"
)

type scrubNothing struct{}

func (scrubNothing) Mask(s string) string { return s }
func (scrubNothing) MaskAny(v any) any    { return v }

func secretCtx() context.Context { return WithSecretCall(context.Background(), scrubNothing{}) }

func tlsHTTPRunner(t *testing.T, anyOf ...interface{ Client() *http.Client }) *Runner {
	t.Helper()
	r := newRunner()
	// httptest TLS servers share one certificate, so any server's client trusts all.
	r.client.Transport.(*http.Transport).TLSClientConfig = anyOf[0].Client().Transport.(*http.Transport).TLSClientConfig
	return r
}

// A request that carries a step's resolved secrets is never sent over plain
// http, exactly as for a saved credential.
func TestSecretCallRefusesPlainHTTP(t *testing.T) {
	var hits atomic.Int32
	srv := netguardtest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	m, a := builtinHTTPRequest(t)
	params := map[string]any{"url": srv.URL + "/x"}

	_, err := newRunner().Run(context.Background(), m, a, params)
	require.NoError(t, err, "control: an ordinary http request is allowed")
	require.EqualValues(t, 1, hits.Load())

	hits.Store(0)
	res, err := newRunner().Run(secretCtx(), m, a, params)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only sent over https")
	assert.Nil(t, res)
	assert.Zero(t, hits.Load(), "nothing is sent")
}

// A redirect to another host is not followed with the secrets in the request.
func TestSecretCallDoesNotFollowACrossHostRedirect(t *testing.T) {
	var elsewhere atomic.Int32
	other := netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		_, _ = w.Write([]byte(`{"landed":true}`))
	}))
	origin := netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/landing", http.StatusFound)
	}))
	m, a := builtinHTTPRequest(t)
	params := map[string]any{"url": origin.URL + "/r"}

	res, err := tlsHTTPRunner(t, origin).Run(context.Background(), m, a, params)
	require.NoError(t, err, "control: an ordinary request follows the redirect")
	require.False(t, res.IsError, res.Content)
	require.EqualValues(t, 1, elsewhere.Load())

	elsewhere.Store(0)
	res, err = tlsHTTPRunner(t, origin).Run(secretCtx(), m, a, params)
	failed := err != nil || res.IsError
	require.True(t, failed, "the redirect must be refused")
	text := ""
	if err != nil {
		text = err.Error()
	} else {
		text = res.Content
	}
	assert.Contains(t, text, "never sent to a second host")
	assert.Zero(t, elsewhere.Load(), "the other host is never contacted")
}

// A pagination link to another host is not followed either.
func TestSecretCallDoesNotFollowAPaginationLinkToAnotherHost(t *testing.T) {
	var elsewhere atomic.Int32
	other := netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		elsewhere.Add(1)
		_, _ = w.Write([]byte(`{"items":[2]}`))
	}))
	origin := netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[1],"next":"` + other.URL + `/page2"}`))
	}))
	// The other host is a legal host for this integration, so only the pin
	// (not the allow list) can stop the link.
	// Both servers listen on 127.0.0.1 with different ports; the allow list
	// names hosts without ports, the pin compares host and port.
	doc := strings.Replace(manifestFor(origin.URL, `    params: { type: object }
    request:
      method: GET
      path: /list
      pagination: { style: next_url, next_url: "response.?next.orValue('')", max_pages: 3 }
    output: { select: "response.items" }
`), "connection:\n", "connection:\n  allowed_hosts: [127.0.0.1]\n", 1)
	m, a := mustParse(t, doc)

	res, err := tlsHTTPRunner(t, origin).Run(context.Background(), m, a, nil)
	require.NoError(t, err, "control: an ordinary walk follows the link")
	require.False(t, res.IsError, res.Content)
	require.EqualValues(t, 1, elsewhere.Load())

	elsewhere.Store(0)
	res, err = tlsHTTPRunner(t, origin).Run(secretCtx(), m, a, nil)
	failed := err != nil || res.IsError
	require.True(t, failed, "the pagination link must be refused")
	text := ""
	if err != nil {
		text = err.Error()
	} else {
		text = res.Content
	}
	assert.Contains(t, text, "never sent to a second host")
	assert.Zero(t, elsewhere.Load(), "the other host is never contacted")
}

// A nil scrubber marks nothing: the call is an ordinary one.
func TestWithSecretCallNilMarksNothing(t *testing.T) {
	assert.Nil(t, SecretCall(WithSecretCall(context.Background(), nil)))
	assert.NotNil(t, SecretCall(secretCtx()))
}

// A secret the server echoes is masked in the whole response before the runner
// cuts it: an error snippet keeps 300 bytes, so a value echoed across that cut
// once left a prefix no registered form matched, and it survived the action
// activity's mask (reliant #2037 review). Error, non-JSON and JSON responses.
func TestSecretCallMasksTheResponseBeforeItIsCut(t *testing.T) {
	const secret = "sk-live/0123456789abcdefghijklmnopqrstuvwxyz"
	masker := stepsecrets.NewMasker(secret)
	ctx := WithSecretCall(context.Background(), masker)
	pad := strings.Repeat("x", 300-10) // the value starts 10 bytes before the 300-byte cut
	for name, tc := range map[string]struct {
		status int
		ctype  string
		body   string
	}{
		"error response": {http.StatusBadRequest, "text/plain", pad + secret + pad},
		"not JSON":       {http.StatusOK, "text/html", pad + secret + pad},
		"JSON":           {http.StatusOK, "application/json", `{"echo":"` + pad + secret + `"}`},
		// Valid JSON spellings the wire text does not match: json.Unmarshal restores them.
		"JSON unicode escapes": {http.StatusOK, "application/json", `{"echo":"` + pad + `\u0073\u006b-live/0123456789abcdefghijklmnopqrstuvwxyz"}`},
		"JSON escaped slash":   {http.StatusOK, "application/json", `{"echo":"` + pad + `sk-live\/0123456789abcdefghijklmnopqrstuvwxyz"}`},
		"JSON error escaped":   {http.StatusBadRequest, "application/json", `{"error":"` + pad + `sk-live\/0123456789abcdefghijklmnopqrstuvwxyz"}`},
	} {
		t.Run(name, func(t *testing.T) {
			srv := netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.ctype)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			m, a := builtinHTTPRequest(t)
			res, err := tlsHTTPRunner(t, srv).Run(ctx, m, a, map[string]any{"url": srv.URL + "/x"})
			text := ""
			if err != nil {
				text = err.Error()
			} else {
				text = res.Content + fmt.Sprint(res.Data)
			}
			for _, frag := range []string{"sk-live", "0123456789", "opqrstuvwxyz"} {
				assert.NotContains(t, text, frag, "a fragment of the echoed secret survived")
			}
		})
	}
}
