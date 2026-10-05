// Copyright (c) 2025 Reliant Labs
package localprobe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeRootAndEndpointID(t *testing.T) {
	for in, want := range map[string]string{
		"http://h:8000/v1":       "http://h:8000",
		"http://h:8000/v1/":      "http://h:8000",
		"http://h:8000":          "http://h:8000",
		"https://h/proxy/v1?x=1": "https://h/proxy",
	} {
		got, err := NormalizeRoot(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	_, err := NormalizeRoot("ftp://h")
	assert.Error(t, err)

	// Parity pin with the daemon's configuredLocalEndpointID
	// (daemonruntime/localmodels_probe.go): "cfg-" + sha256(root)[:8]. If this
	// drifts, a VIA_DAEMON endpoint is addressed by an id its daemon rejects.
	assert.Equal(t, "cfg-"+shaPrefix("http://10.0.0.5:8000"), EndpointID("http://10.0.0.5:8000"))
	assert.Equal(t, EndpointID("http://h:1"), EndpointID("http://h:1"))
	assert.NotEqual(t, EndpointID("http://h:1"), EndpointID("http://h:2"))
}

func shaPrefix(s string) string {
	// duplicated on purpose: the test must not call the code under test
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}

func TestParseOllamaShow(t *testing.T) {
	info, err := ParseOllamaShow([]byte(`{"capabilities":["completion","tools","thinking"],"parameters":"stop \"x\"\nnum_ctx                        16384\ntemperature 0.6","model_info":{"general.architecture":"llama","llama.context_length":131072}}`))
	require.NoError(t, err)
	assert.EqualValues(t, 16384, info.ModelfileCtx)
	assert.EqualValues(t, 131072, info.TrainedCtx)

	m := OllamaModelInfo("x", info, true, 0, "", 16384)
	assert.True(t, m.SupportsChat)
	assert.True(t, m.SupportsTools)
	assert.True(t, m.SupportsThinking)
	assert.False(t, m.SupportsVision)

	embed := OllamaModelInfo("e", OllamaShow{Capabilities: []string{"embedding"}}, true, 0, "", 0)
	assert.False(t, embed.SupportsChat)

	unknown := OllamaModelInfo("u", OllamaShow{}, false, 0, "", 0)
	assert.True(t, unknown.SupportsChat, "unknown capabilities default to chat-capable, claiming nothing")
	assert.False(t, unknown.SupportsTools)

	_, err = ParseOllamaShow([]byte(`nope`))
	assert.Error(t, err)
}

func TestParseLMStudioModels(t *testing.T) {
	got, err := ParseLMStudioModels([]byte(`{"data":[
		{"id":"qwen","type":"llm","max_context_length":32768,"loaded_context_length":16384,"capabilities":["tool_use"]},
		{"id":"vis","type":"vlm","max_context_length":8192},
		{"id":"emb","type":"embeddings"},
		{"id":""}
	]}`))
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.EqualValues(t, 16384, got[0].ContextWindow, "the loaded context is the one honored")
	assert.True(t, got[0].SupportsTools)
	assert.True(t, got[1].SupportsVision)
	assert.False(t, got[2].SupportsChat)
}

func TestParseLlamaCppProps(t *testing.T) {
	n, vision, tools, err := ParseLlamaCppProps([]byte(`{"default_generation_settings":{"n_ctx":16384},"modalities":{"vision":true}}`))
	require.NoError(t, err)
	assert.EqualValues(t, 16384, n)
	assert.True(t, vision)
	assert.False(t, tools)

	n, _, tools, err = ParseLlamaCppProps([]byte(`{"default_generation_settings":{"params":{"n_ctx":4096}},"chat_template_caps":{"supports_tools":true}}`))
	require.NoError(t, err)
	assert.EqualValues(t, 4096, n)
	assert.True(t, tools)
}

func TestParseModelIDs(t *testing.T) {
	ids, err := ParseModelIDs([]byte(`{"data":[{"id":"a"},{"id":""},{"id":"b"}]}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, ids)
	_, err = ParseModelIDs([]byte(`<html>`))
	assert.Error(t, err)
}

// fakeServer answers the way each server family really does.
func fakeServer(t *testing.T, routes map[string]string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if body, ok := routes[r.Method+" "+r.URL.Path]; ok {
			_, _ = w.Write([]byte(body))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const twoModels = `{"data":[{"id":"alpha"},{"id":"beta"}]}`

func TestProbeDetectsEachServerFamily(t *testing.T) {
	cases := []struct {
		name     string
		routes   map[string]string
		kind     string
		check    func(t *testing.T, models []string)
		wantCtx  int64
		wantTool bool
	}{
		{
			name:   "plain openai-compatible",
			routes: map[string]string{"GET /v1/models": twoModels},
			kind:   "openai_compatible",
		},
		{
			name:   "vllm (answers /version)",
			routes: map[string]string{"GET /v1/models": twoModels, "GET /version": `{"version":"0.6.1"}`},
			kind:   "vllm",
		},
		{
			name: "ollama",
			routes: map[string]string{
				"GET /v1/models": twoModels,
				"GET /api/tags":  `{"models":[]}`,
				"GET /api/ps":    `{"models":[{"name":"alpha","model":"alpha","context_length":12345}]}`,
				"POST /api/show": `{"capabilities":["completion","tools"],"model_info":{"general.architecture":"llama","llama.context_length":8192}}`,
			},
			kind: "ollama", wantCtx: 12345, wantTool: true,
		},
		{
			name: "lm studio",
			routes: map[string]string{
				"GET /v1/models":     twoModels,
				"GET /api/v0/models": `{"data":[{"id":"alpha","type":"llm","max_context_length":32768,"capabilities":["tool_use"]},{"id":"beta","type":"llm","max_context_length":4096}]}`,
			},
			kind: "lmstudio", wantCtx: 32768, wantTool: true,
		},
		{
			name: "llama.cpp",
			routes: map[string]string{
				"GET /v1/models": twoModels,
				"GET /props":     `{"default_generation_settings":{"n_ctx":16384},"chat_template_caps":{"supports_tools":true}}`,
			},
			kind: "llamacpp", wantCtx: 16384, wantTool: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := fakeServer(t, tc.routes, nil)
			ep := Probe(context.Background(), srv.Client(), "id-1", Target{Root: srv.URL}, Options{})
			require.Empty(t, ep.Error)
			assert.Equal(t, tc.kind, ep.Kind)
			assert.Equal(t, srv.URL+"/v1", ep.BaseUrl)
			require.Len(t, ep.Models, 2)
			assert.Equal(t, "alpha", ep.Models[0].Name)
			assert.Equal(t, "beta", ep.Models[1].Name)
			assert.EqualValues(t, tc.wantCtx, ep.Models[0].ContextWindow)
			assert.Equal(t, tc.wantTool, ep.Models[0].SupportsTools)
			assert.True(t, ep.Models[0].SupportsChat)
		})
	}
}

func TestProbeSendsCredentialsAndReportsFailures(t *testing.T) {
	var gotAuth, gotOrg atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		gotOrg.Store(r.Header.Get("X-Org"))
		if r.Header.Get("Authorization") != "Bearer sk-ok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(twoModels))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	ep := Probe(context.Background(), srv.Client(), "x", Target{Root: srv.URL}, Options{})
	assert.Contains(t, ep.Error, "401")
	assert.Empty(t, ep.Models)

	ep = Probe(context.Background(), srv.Client(), "x", Target{Root: srv.URL, APIKey: "sk-ok", Headers: map[string]string{"X-Org": "acme"}}, Options{})
	require.Empty(t, ep.Error)
	assert.Equal(t, "Bearer sk-ok", gotAuth.Load())
	assert.Equal(t, "acme", gotOrg.Load())

	dead := httptest.NewServer(http.NotFoundHandler())
	root := dead.URL
	dead.Close()
	ep = Probe(context.Background(), http.DefaultClient, "x", Target{Root: root}, Options{})
	assert.NotEmpty(t, ep.Error)
	assert.NotNil(t, ep, "a dead server still yields an endpoint")
	assert.False(t, strings.Contains(ep.Error, "dial tcp 127"), "the error is trimmed to the actionable part: %q", ep.Error)
}

func TestProbeBoundsPerModelEnrichment(t *testing.T) {
	var shows atomic.Int32
	names := make([]string, 0, 200)
	items := make([]string, 0, 200)
	for i := range 200 {
		n := "m" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + string(rune('A'+i/26))
		names, items = append(names, n), append(items, `{"id":"`+n+`"}`)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[` + strings.Join(items, ",") + `]}`))
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[]}`))
		case "/api/show":
			shows.Add(1)
			_, _ = w.Write([]byte(`{"capabilities":["completion"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ep := Probe(context.Background(), srv.Client(), "x", Target{Root: srv.URL}, Options{})
	require.Empty(t, ep.Error)
	assert.Len(t, ep.Models, 200, "every model is listed")
	assert.LessOrEqual(t, int(shows.Load()), maxEnriched, "but metadata calls are bounded")
	_ = names
}

// A DIRECT Ollama must publish the context Ollama will HONOR, exactly as the
// daemon does, not the trained one and not 0. Before this was shared a direct
// probe reported 0 (which the catalog turned into an 8K guess) for any model
// without a loaded context or Modelfile num_ctx.
func TestProbeOllamaPublishesHonoredContextNotTrained(t *testing.T) {
	const gib = int64(1) << 30
	mk := func(version, ps string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1/models":
				_, _ = w.Write([]byte(`{"data":[{"id":"qwen3:latest"}]}`))
			case "/api/tags":
				_, _ = w.Write([]byte(`{"models":[{"name":"qwen3:latest","size":5,"digest":"d1","details":{"parameter_size":"8.2B"}}]}`))
			case "/api/version":
				_, _ = w.Write([]byte(`{"version":"` + version + `"}`))
			case "/api/ps":
				_, _ = w.Write([]byte(ps))
			case "/api/show":
				_, _ = w.Write([]byte(`{"capabilities":["completion","tools"],"model_info":{"general.architecture":"qwen3","qwen3.context_length":40960}}`))
			default:
				http.NotFound(w, r)
			}
		}))
	}
	ctxOf := func(srv *httptest.Server, vram func() int64) int64 {
		defer srv.Close()
		ep := Probe(context.Background(), srv.Client(), "x", Target{Root: srv.URL}, Options{VRAMBytes: vram})
		require.Empty(t, ep.Error)
		require.Equal(t, "ollama", ep.Kind)
		require.Len(t, ep.Models, 1)
		assert.Equal(t, "8.2B", ep.Models[0].ParameterSize)
		return ep.Models[0].ContextWindow
	}
	none := `{"models":[]}`
	assert.EqualValues(t, 4096, ctxOf(mk("0.12.3", none), nil), "old server: flat 4096, not trained 40960")
	assert.EqualValues(t, 4096, ctxOf(mk("0.15.6", none), nil), "new server, VRAM unknown to a remote prober: conservative 4096")
	assert.EqualValues(t, 32768, ctxOf(mk("0.15.6", none), func() int64 { return 24 * gib }), "VRAM known (daemon) picks the tier")
	assert.EqualValues(t, 40960, ctxOf(mk("0.15.6", none), func() int64 { return 96 * gib }), "tier capped at trained")
	assert.EqualValues(t, 12288, ctxOf(mk("0.12.3", `{"models":[{"name":"qwen3:latest","model":"qwen3:latest","context_length":12288}]}`), nil), "a loaded model's context is authoritative")
}

func TestShowCacheAndContextObserver(t *testing.T) {
	var shows atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"m","digest":"d1"}]}`))
		case "/api/show":
			shows.Add(1)
			_, _ = w.Write([]byte(`{"capabilities":["completion"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	var rules []string
	opt := Options{ShowCache: NewShowCache(), OnContext: func(_ string, _ int64, rule string) { rules = append(rules, rule) }}
	for range 3 {
		Probe(context.Background(), srv.Client(), "x", Target{Root: srv.URL}, opt)
	}
	assert.EqualValues(t, 1, shows.Load(), "/api/show is cached by name+digest")
	assert.Len(t, rules, 3)
}

func TestOllamaContextPrecedence(t *testing.T) {
	const gib = int64(1) << 30
	nv := [3]int{0, 20, 0}
	cases := []struct {
		name string
		in   OllamaContextInputs
		want int64
		rule string
	}{
		{"loaded beats all", OllamaContextInputs{LoadedCtx: 8192, ModelfileCtx: 65536, TrainedCtx: 131072, VersionKnown: true, Version: nv, VRAMBytes: 96 * gib}, 8192, "loaded"},
		{"loaded is not capped", OllamaContextInputs{LoadedCtx: 65536, TrainedCtx: 8192}, 65536, "loaded"},
		{"modelfile beats vram", OllamaContextInputs{ModelfileCtx: 16384, TrainedCtx: 131072, VersionKnown: true, Version: nv, VRAMBytes: 96 * gib}, 16384, "modelfile"},
		{"modelfile capped", OllamaContextInputs{ModelfileCtx: 65536, TrainedCtx: 32768}, 32768, "capped"},
		{"vram tier", OllamaContextInputs{TrainedCtx: 131072, VersionKnown: true, Version: nv, VRAMBytes: 30 * gib}, 32768, "vram default"},
		{"vram unknown", OllamaContextInputs{TrainedCtx: 131072, VersionKnown: true, Version: nv}, 4096, "vram unknown"},
		{"old version flat", OllamaContextInputs{TrainedCtx: 131072, VersionKnown: true, Version: [3]int{0, 12, 3}, VRAMBytes: 96 * gib}, 4096, "flat"},
		{"unknown version flat", OllamaContextInputs{TrainedCtx: 131072, VRAMBytes: 96 * gib}, 4096, "flat"},
		{"trained smaller than default", OllamaContextInputs{TrainedCtx: 2048, VersionKnown: true, Version: nv, VRAMBytes: 8 * gib}, 2048, "capped"},
	}
	for _, c := range cases {
		got, rule := ResolveOllamaContext(c.in)
		assert.Equal(t, c.want, got, c.name)
		assert.Contains(t, rule, c.rule, c.name)
	}
	for in, want := range map[string][3]int{`{"version":"0.12.3"}`: {0, 12, 3}, `{"version":"v0.15.6-rc1"}`: {0, 15, 6}} {
		v, ok := ParseOllamaVersion([]byte(in))
		assert.True(t, ok)
		assert.Equal(t, want, v)
	}
	_, ok := ParseOllamaVersion([]byte(`{}`))
	assert.False(t, ok)
	for vram, want := range map[int64]int64{0: 4096, 22 * gib: 4096, 23 * gib: 32768, 47*gib - 1: 32768, 47 * gib: 262144} {
		assert.Equal(t, want, OllamaVRAMTierContext(vram))
	}
}
