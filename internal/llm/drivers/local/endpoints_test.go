// Copyright (c) 2025 Reliant Labs
package local

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/netguard"
)

type endpointDirectory struct {
	daemons   []DaemonInventory
	endpoints []EndpointConfig
}

func (d endpointDirectory) LocalDaemons(context.Context, string) ([]DaemonInventory, error) {
	return d.daemons, nil
}
func (d endpointDirectory) ConfiguredEndpoints(context.Context, string) ([]EndpointConfig, error) {
	return d.endpoints, nil
}

func probeWith(models ...*reliantv1.LocalModelInfo) *reliantv1.LocalModelEndpoint {
	return &reliantv1.LocalModelEndpoint{Id: "x", Kind: "vllm", Source: "configured", Models: models}
}

func chat(name string, ctx int64) *reliantv1.LocalModelInfo {
	return &reliantv1.LocalModelInfo{Name: name, SupportsChat: true, ContextWindow: ctx}
}

func ptr[T any](v T) *T { return &v }

func TestSynthesizeEndpointCapabilityPrecedence(t *testing.T) {
	cfg := EndpointConfig{
		ID: "ep-1", Name: "Lab GPU", BaseURL: "https://llm.example.com/v1", Route: db.ModelEndpointRouteDirect,
		Probe: probeWith(
			&reliantv1.LocalModelInfo{Name: "probed", SupportsChat: true, ContextWindow: 65536, SupportsTools: true, SupportsVision: true},
			&reliantv1.LocalModelInfo{Name: "overridden", SupportsChat: true, ContextWindow: 65536, SupportsTools: true},
			&reliantv1.LocalModelInfo{Name: "bare", SupportsChat: true},
			&reliantv1.LocalModelInfo{Name: "embedder", SupportsChat: false},
			&reliantv1.LocalModelInfo{Name: "hidden-one", SupportsChat: true},
		),
		Models: []*reliantv1.ModelEndpointModel{
			{Name: "overridden", ContextWindow: 131072, MaxOutputTokens: 4096, SupportsTools: ptr(false), SupportsVision: ptr(true), SupportsThinking: ptr(true)},
			{Name: "hidden-one", Hidden: true},
			{Name: "configured-only", ContextWindow: 16000},
		},
	}
	got := map[string]Model{}
	for _, m := range SynthesizeEndpoint(cfg) {
		got[m.Definition.ID] = m
	}

	assert.NotContains(t, got, "hidden-one", "hidden models leave the catalog")
	assert.NotContains(t, got, "embedder", "embedding-only models are never chat models")

	probed := got["probed"].Definition.Capabilities
	assert.Equal(t, 65536, probed.MaxContextWindow, "caps come from the probe when there is no override")
	assert.True(t, probed.SupportsTools)
	assert.True(t, probed.SupportsAttachments)

	over := got["overridden"].Definition.Capabilities
	assert.Equal(t, 131072, over.MaxContextWindow, "an override beats the probe")
	assert.Equal(t, 4096, over.MaxOutputTokens, "max output feeds the definition")
	assert.False(t, over.SupportsTools, "an explicit false beats a probed true")
	assert.True(t, over.SupportsAttachments)
	assert.True(t, over.CanReason)

	bare := got["bare"].Definition.Capabilities
	assert.Equal(t, 8192, bare.MaxContextWindow, "conservative default context")
	assert.False(t, bare.SupportsTools, "conservative default: no tools")

	assert.Equal(t, 16000, got["configured-only"].Definition.Capabilities.MaxContextWindow, "a configured model the server did not list is still offered")

	for id, m := range got {
		assert.Equal(t, id+"@local", m.CatalogID())
		assert.Equal(t, "endpoint:ep-1", m.Provider(), "never collides with local:<daemonID>")
		assert.Equal(t, "Lab GPU", m.Machine, "grouped by endpoint name")
		assert.True(t, m.Online, "a direct endpoint is offered; a failed call says why")
	}
}

func TestSynthesizeViaDaemonUsesRelayIDAndLiveness(t *testing.T) {
	cfg := EndpointConfig{
		ID: "ep-2", Name: "Home vLLM", BaseURL: "http://10.0.0.5:8000/v1", Route: db.ModelEndpointRouteViaDaemon,
		DaemonID: "d-1", Online: false, Probe: probeWith(chat("m", 8192)),
	}
	ms := SynthesizeEndpoint(cfg)
	require.Len(t, ms, 1)
	assert.False(t, ms[0].Online, "offline with its machine")
	assert.Equal(t, "d-1", ms[0].DaemonID)
	assert.Equal(t, RelayEndpointID("http://10.0.0.5:8000/v1"), ms[0].EndpointID)
	assert.Equal(t, RelayEndpointID("http://10.0.0.5:8000"), ms[0].EndpointID, "the /v1 suffix does not change the id")
	assert.True(t, strings.HasPrefix(ms[0].EndpointID, "cfg-"))
}

func TestListModelsMergesDetectedAndConfiguredAndResolveKeepsThemApart(t *testing.T) {
	dir := endpointDirectory{
		daemons: []DaemonInventory{{
			DaemonID: "d-1", Machine: "laptop", Online: true,
			Inventory: &reliantv1.LocalModelInventory{Endpoints: []*reliantv1.LocalModelEndpoint{
				{Id: "ollama", Kind: "ollama", Models: []*reliantv1.LocalModelInfo{chat("qwen3", 8192)}},
			}},
		}},
		endpoints: []EndpointConfig{{
			ID: "ep-1", Name: "Lab GPU", BaseURL: "https://llm.example.com/v1", Route: db.ModelEndpointRouteDirect,
			Probe: probeWith(chat("qwen3", 32768), chat("llama", 8192)),
		}},
	}
	all, err := ListModels(context.Background(), dir, "u1")
	require.NoError(t, err)
	require.Len(t, all, 3)

	t.Run("a daemon pin only ever resolves a detected model", func(t *testing.T) {
		got, err := Resolve(all, models.ModelSelector{ID: "qwen3@local", Providers: []string{"local:d-1"}}, "")
		require.NoError(t, err)
		assert.Nil(t, got.Custom)
		assert.Equal(t, 8192, got.Definition.Capabilities.MaxContextWindow)
	})
	t.Run("an endpoint pin resolves that endpoint's model", func(t *testing.T) {
		got, err := Resolve(all, models.ModelSelector{ID: "qwen3@local", Providers: []string{"endpoint:ep-1"}}, "")
		require.NoError(t, err)
		require.NotNil(t, got.Custom)
		assert.Equal(t, 32768, got.Definition.Capabilities.MaxContextWindow)
	})
	t.Run("an endpoint pin never falls through to a detected model", func(t *testing.T) {
		_, err := Resolve(all, models.ModelSelector{ID: "qwen3@local", Providers: []string{"endpoint:gone"}}, "")
		var unavailable *UnavailableError
		require.ErrorAs(t, err, &unavailable)
		assert.Contains(t, err.Error(), "no longer exists")
	})
	t.Run("a missing model on a live endpoint says so", func(t *testing.T) {
		_, err := Resolve(all, models.ModelSelector{ID: "nope@local", Providers: []string{"endpoint:ep-1"}}, "")
		require.ErrorContains(t, err, "does not serve this model")
	})
	t.Run("both are local selectors", func(t *testing.T) {
		assert.True(t, IsSelector(models.ModelSelector{ID: "x", Providers: []string{"endpoint:ep-1"}}))
		assert.True(t, IsSelector(models.ModelSelector{ID: "x", Providers: []string{"local:d-1"}}))
		assert.False(t, IsSelector(models.ModelSelector{ID: "x", Providers: []string{"endpoint:"}}))
	})
	t.Run("an offline machine reads as offline, naming the endpoint", func(t *testing.T) {
		off := []Model{SynthesizeEndpoint(EndpointConfig{ID: "ep-9", Name: "Home", BaseURL: "http://10.0.0.5:8000/v1", Route: db.ModelEndpointRouteViaDaemon, DaemonID: "d-9", Online: false, Probe: probeWith(chat("m", 0))})[0]}
		_, err := Resolve(off, models.ModelSelector{ID: "m@local", Providers: []string{"endpoint:ep-9"}}, "")
		require.ErrorContains(t, err, "offline")
		assert.Contains(t, err.Error(), "Home")
	})
}

// ---- the wire: DIRECT through the real driver ----

type captured struct {
	mu      sync.Mutex
	path    string
	auth    string
	headers http.Header
	body    map[string]any
	remote  string
}

func chatServer(t *testing.T, c *captured) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.path, c.auth, c.headers, c.remote = r.URL.Path, r.Header.Get("Authorization"), r.Header.Clone(), r.RemoteAddr
		_ = json.Unmarshal(raw, &c.body)
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sendOnce(t *testing.T, rt http.RoundTripper, temperature *float64) {
	t.Helper()
	opts := llm.DriverOptions{BaseURL: PlaceholderBaseURL, Transport: rt, Model: models.Model{ID: "m", APIModel: "m"}, Temperature: temperature}
	c := NewClient(opts)
	_, err := c.SendMessages(context.Background(), nil, []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}}}, nil)
	require.NoError(t, err)
}

func TestDirectEndpointOnTheWire(t *testing.T) {
	var c captured
	srv := chatServer(t, &c)

	model := Model{
		Definition: models.ModelDefinition{ID: "m"},
		Custom: &CustomEndpoint{
			ID: "ep-1", Name: "Lab", BaseURL: srv.URL + "/v1", Route: db.ModelEndpointRouteDirect,
			Params: ModelParams{
				Temperature: ptr(0.3), TopP: ptr(0.9),
				ExtraBody: map[string]json.RawMessage{
					"min_p":                json.RawMessage(`0.05`),
					"chat_template_kwargs": json.RawMessage(`{"enable_thinking":false}`),
					"repetition_penalty":   json.RawMessage(`1.1`),
					"model":                json.RawMessage(`"evil"`), // reserved: must be ignored even if stored
				},
			},
		},
	}
	rt, err := TransportFor(context.Background(), "u1", &model, nil, &CustomRoutes{Policy: netguard.Policy{AllowPrivate: true}})
	require.NoError(t, err)

	t.Run("extra body is merged, defaults fill in, reserved keys are never overridden", func(t *testing.T) {
		sendOnce(t, rt, nil)
		assert.Equal(t, "/v1/chat/completions", c.path)
		assert.Equal(t, 0.05, c.body["min_p"])
		assert.Equal(t, 1.1, c.body["repetition_penalty"])
		assert.Equal(t, map[string]any{"enable_thinking": false}, c.body["chat_template_kwargs"])
		assert.Equal(t, 0.3, c.body["temperature"], "the model's default temperature applies when the chat sets none")
		assert.Equal(t, 0.9, c.body["top_p"])
		assert.Equal(t, "m", c.body["model"], "reserved keys cannot be overridden")
		assert.NotEmpty(t, c.body["messages"])
	})
	t.Run("a chat-level temperature wins over the model default", func(t *testing.T) {
		sendOnce(t, rt, ptr(0.8))
		assert.Equal(t, 0.8, c.body["temperature"])
		assert.Equal(t, 0.9, c.body["top_p"], "top_p default still applies")
	})
	t.Run("no credential is sent when the endpoint has none", func(t *testing.T) {
		assert.NotContains(t, c.auth, "Bearer sk")
	})
}

func TestDirectEndpointSendsStoredCredentials(t *testing.T) {
	var c captured
	srv := chatServer(t, &c)
	model := Model{Definition: models.ModelDefinition{ID: "m"}, Custom: &CustomEndpoint{
		ID: "ep-1", BaseURL: srv.URL + "/v1", Route: db.ModelEndpointRouteDirect, CredentialConnectionID: "conn-1",
	}}
	rt, err := TransportFor(context.Background(), "u1", &model, nil, &CustomRoutes{
		Policy:      netguard.Policy{AllowPrivate: true},
		Credentials: fakeCreds{key: "sk-test", headers: map[string]string{"X-Org": "acme"}},
	})
	require.NoError(t, err)
	sendOnce(t, rt, nil)
	assert.Equal(t, "Bearer sk-test", c.auth)
	assert.Equal(t, "acme", c.headers.Get("X-Org"))

	t.Run("an unreadable credential makes the model unavailable, not unauthenticated", func(t *testing.T) {
		_, err := TransportFor(context.Background(), "u1", &model, nil, &CustomRoutes{
			Policy: netguard.Policy{AllowPrivate: true}, Credentials: fakeCreds{err: errors.New("vault down")},
		})
		var unavailable *UnavailableError
		require.ErrorAs(t, err, &unavailable)
		assert.NotContains(t, err.Error(), "vault down", "internals are not shown to the user")
	})
	t.Run("no credential source at all is also unavailable", func(t *testing.T) {
		_, err := TransportFor(context.Background(), "u1", &model, nil, &CustomRoutes{Policy: netguard.Policy{AllowPrivate: true}})
		var unavailable *UnavailableError
		require.ErrorAs(t, err, &unavailable)
	})
}

type fakeCreds struct {
	key     string
	headers map[string]string
	err     error
}

func (f fakeCreds) Get(context.Context, string, string) (string, map[string]string, error) {
	return f.key, f.headers, f.err
}

// A hosted worker must refuse at CONNECT time, whatever the name resolved to
// when it was validated: this is the DNS-rebinding defence, not the save-time
// check. The endpoint's host is "localhost", which the save-time check also
// refuses; here the request path is exercised with the check bypassed by an
// IP-literal-free name that the SYSTEM resolver sends to loopback.
func TestDirectEndpointRefusesPrivateDestinationAtRequestTime(t *testing.T) {
	var c captured
	srv := chatServer(t, &c) // listens on 127.0.0.1
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))

	// The pre-flight resolver says "public"; the dialer's real lookup of
	// 127.0.0.1.nip.io-style names is not available offline, so use the literal
	// loopback address behind a pre-flight that was fooled.
	fooled := netguard.Policy{LookupIP: func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}}
	model := Model{Definition: models.ModelDefinition{ID: "m"}, Custom: &CustomEndpoint{
		ID: "ep-1", BaseURL: "http://rebind.example.com:" + port + "/v1", Route: db.ModelEndpointRouteDirect,
	}}
	// Route the name to loopback exactly where DNS would: below the policy's
	// own dial guard, which sees only the final IP.
	guarded := fooled.Transport()
	realDial := guarded.DialContext
	guarded.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return realDial(ctx, network, "127.0.0.1:"+port)
	}
	rt, err := TransportFor(context.Background(), "u1", &model, nil, &CustomRoutes{Policy: fooled, Dial: guarded})
	require.NoError(t, err)

	cl := NewClient(llm.DriverOptions{BaseURL: PlaceholderBaseURL, Transport: rt, Model: models.Model{ID: "m", APIModel: "m"}})
	_, err = cl.SendMessages(context.Background(), nil, []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}}}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not allowed")
	assert.Empty(t, c.remote, "the loopback server must never have been reached")
}

func TestShapeChatBody(t *testing.T) {
	out, err := ShapeChatBody([]byte(`{"model":"m","temperature":0.1,"messages":[]}`), ModelParams{
		Temperature: ptr(0.9), TopP: ptr(0.5),
		ExtraBody: map[string]json.RawMessage{"min_p": json.RawMessage(`0.1`), "stream": json.RawMessage(`false`), "tools": json.RawMessage(`[]`)},
	})
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, 0.1, got["temperature"], "an existing temperature is kept")
	assert.Equal(t, 0.5, got["top_p"])
	assert.Equal(t, 0.1, got["min_p"])
	assert.NotContains(t, got, "stream")
	assert.NotContains(t, got, "tools")

	_, err = ShapeChatBody([]byte(`[1]`), ModelParams{})
	assert.Error(t, err)
}

func TestValidateExtraBodyMatrix(t *testing.T) {
	for _, ok := range []string{"", "  ", `{}`, `{"min_p":0.05}`, `{"nested":{"model":"fine"}}`} {
		_, err := ValidateExtraBody(ok)
		assert.NoError(t, err, ok)
	}
	for _, bad := range []string{`null`, `[]`, `1`, `"s"`, `{`, `{"model":"x"}`, `{"messages":1}`, `{"tools":null}`, `{"stream":true}`, `{} {}`} {
		_, err := ValidateExtraBody(bad)
		assert.Error(t, err, bad)
	}
}

// ---- the wire: VIA_DAEMON through a fake relay ----

type recordedRelay struct {
	mu        sync.Mutex
	userID    string
	daemonID  string
	endpoint  string
	bodies    []map[string]any
	paths     []string
	relayHits int
}

func (r *recordedRelay) factory(userID, daemonID, endpointID string) http.RoundTripper {
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(req.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		r.mu.Lock()
		r.userID, r.daemonID, r.endpoint = userID, daemonID, endpointID
		r.bodies = append(r.bodies, body)
		r.paths = append(r.paths, req.URL.Path)
		r.relayHits++
		r.mu.Unlock()
		return &http.Response{
			StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Request: req,
			Body: io.NopCloser(strings.NewReader(`{"id":"1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)),
		}, nil
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestViaDaemonEndpointUsesRelayWithEndpointIDAndShapesTheBody(t *testing.T) {
	relay := &recordedRelay{}
	cfg := EndpointConfig{
		ID: "ep-2", Name: "Home", BaseURL: "http://10.0.0.5:8000/v1", Route: db.ModelEndpointRouteViaDaemon,
		DaemonID: "d-1", Online: true, Probe: probeWith(chat("m", 8192)),
		Models: []*reliantv1.ModelEndpointModel{{Name: "m", Temperature: ptr(0.2), ExtraBodyJson: `{"min_p":0.05}`}},
	}
	all, err := ListModels(context.Background(), endpointDirectory{endpoints: []EndpointConfig{cfg}}, "u1")
	require.NoError(t, err)
	picked, err := Resolve(all, models.ModelSelector{ID: "m@local", Providers: []string{"endpoint:ep-2"}}, "")
	require.NoError(t, err)

	rt, err := TransportFor(context.Background(), "u1", picked, relay.factory, &CustomRoutes{Relay: relay.factory, Policy: netguard.Policy{}})
	require.NoError(t, err)
	sendOnce(t, rt, nil)

	assert.Equal(t, "u1", relay.userID)
	assert.Equal(t, "d-1", relay.daemonID)
	assert.Equal(t, RelayEndpointID("http://10.0.0.5:8000/v1"), relay.endpoint, "the relay is addressed by the id the daemon authorized")
	assert.Equal(t, "/v1/chat/completions", relay.paths[0], "same path shape detected models send; the daemon accepts /v1/* as-is")
	assert.Equal(t, 0.05, relay.bodies[0]["min_p"], "extra body is merged on the relay route too")
	assert.Equal(t, 0.2, relay.bodies[0]["temperature"])

	t.Run("no relay on this server means unavailable, not a dial", func(t *testing.T) {
		_, err := TransportFor(context.Background(), "u1", picked, nil, &CustomRoutes{Policy: netguard.Policy{}})
		var unavailable *UnavailableError
		require.ErrorAs(t, err, &unavailable)
	})
}

func TestDetectedModelsKeepTheirPlainRelay(t *testing.T) {
	relay := &recordedRelay{}
	m := &Model{Definition: models.ModelDefinition{ID: "qwen3"}, DaemonID: "d-1", EndpointID: "ollama"}
	rt, err := TransportFor(context.Background(), "u1", m, relay.factory, nil)
	require.NoError(t, err)
	sendOnce(t, rt, nil)
	assert.Equal(t, "ollama", relay.endpoint)

	_, err = TransportFor(context.Background(), "u1", m, nil, nil)
	var unavailable *UnavailableError
	require.ErrorAs(t, err, &unavailable)
}
