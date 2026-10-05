// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/localprobe"
	"google.golang.org/protobuf/encoding/protojson"
)

func readLocalTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "localmodels", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeOllama serves the recorded live Ollama responses.
func fakeOllama(t *testing.T) *httptest.Server {
	return fakeOllamaWith(t, "", "")
}

// fakeOllamaWith overrides the reported version and the /api/ps body.
func fakeOllamaWith(t *testing.T, version, ps string) *httptest.Server {
	t.Helper()
	files := map[string]string{
		"/api/version": "ollama_version.json",
		"/api/tags":    "ollama_tags.json",
		"/v1/models":   "ollama_v1_models.json",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			var req struct{ Model string }
			_ = json.NewDecoder(r.Body).Decode(&req)
			name := strings.TrimSuffix(req.Model, ":latest")
			b, err := os.ReadFile(filepath.Join("testdata", "localmodels", "ollama_show_"+name+".json"))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(b)
			return
		}
		if r.URL.Path == "/api/ps" {
			if ps == "" {
				ps = `{"models":[]}`
			}
			_, _ = io.WriteString(w, ps)
			return
		}
		if r.URL.Path == "/api/version" && version != "" {
			_, _ = io.WriteString(w, `{"version":"`+version+`"}`)
			return
		}
		if f, ok := files[r.URL.Path]; ok {
			_, _ = w.Write(readLocalTestdata(t, f))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func proberFor(root string, cfg localModelsConfig) *localModelProber {
	p := newLocalModelProber()
	p.wellKnown = []wellKnownLocalEndpoint{{id: "ollama", root: root}}
	p.loadCfg = func() localModelsConfig { return cfg }
	return p
}

func TestLocalModelPathAllowList(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"/chat/completions", "/v1/chat/completions", false},
		{"/v1/chat/completions", "/v1/chat/completions", false},
		{"/models?x=1", "/v1/models", false},
		{"/api/show", "/api/show", false},
		{"/api/tags", "/api/tags", false},
		{"/api/v0/models", "/api/v0/models", false},
		{"/props", "/props", false},
		{"/api/pull", "", true},
		{"/api/delete", "", true},
		{"/api/generate", "", true},
		{"/v1/../api/pull", "", true},
		{"/v1/../../etc/passwd", "", true},
		{"/../api/delete", "", true},
		{"/chat/../../api/pull", "", true},
		{"/%2e%2e/api/pull", "", true},
		{"/v1/%2e%2e/api/pull", "", true},
		{"/api/show/../pull", "", true},
		{"chat/completions", "", true},
		{"", "", true},
		{"/props\\..\\x", "", true},
	}
	for _, c := range cases {
		got, _, err := resolveLocalRelayPath(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("%q: err=%v wantErr=%v (got %q)", c.in, err, c.wantErr, got)
			continue
		}
		if !c.wantErr && got != c.want {
			t.Errorf("%q: got %q want %q", c.in, got, c.want)
		}
	}
}

func TestLocalModelKindAndCapabilities_Ollama(t *testing.T) {
	srv := fakeOllama(t)
	snap := proberFor(srv.URL, localModelsConfig{}).probe(context.Background())
	if len(snap.inventory.Endpoints) != 1 {
		t.Fatalf("endpoints = %d", len(snap.inventory.Endpoints))
	}
	ep := snap.inventory.Endpoints[0]
	if ep.Id != "ollama" || ep.Kind != "ollama" || ep.Source != "detected" || ep.Error != "" {
		t.Fatalf("endpoint = %+v", ep)
	}
	byName := map[string]*reliantv1.LocalModelInfo{}
	for _, m := range ep.Models {
		byName[m.Name] = m
	}
	q := byName["qwen3:latest"]
	if q == nil || !q.SupportsChat || !q.SupportsTools || !q.SupportsThinking || q.SupportsVision {
		t.Fatalf("qwen3 = %+v", q)
	}
	if q.ContextWindow != 4096 { // recorded server is 0.12.3: flat 4096 default
		t.Errorf("qwen3 ctx = %d, want 4096", q.ContextWindow)
	}
	if q.ParameterSize != "8.2B" || q.SizeBytes != 5225388164 {
		t.Errorf("qwen3 size info = %q %d", q.ParameterSize, q.SizeBytes)
	}
	e := byName["nomic-embed-text:latest"]
	if e == nil || e.SupportsChat || e.SupportsTools || e.ContextWindow != 2048 {
		t.Fatalf("nomic = %+v", e)
	}
}

func TestLocalModelContextFromServer(t *testing.T) {
	ctxOf := func(srv *httptest.Server, vram int64) int64 {
		p := proberFor(srv.URL, localModelsConfig{})
		p.vramFn = func() int64 { return vram }
		for _, m := range p.probe(context.Background()).inventory.Endpoints[0].Models {
			if m.Name == "qwen3:latest" {
				return m.ContextWindow
			}
		}
		t.Fatal("qwen3 missing")
		return 0
	}
	const gib = int64(1) << 30
	loaded := `{"models":[{"name":"qwen3:latest","model":"qwen3:latest","context_length":12288}]}`
	if got := ctxOf(fakeOllamaWith(t, "0.15.6", loaded), 96*gib); got != 12288 {
		t.Errorf("loaded: %d", got)
	}
	if got := ctxOf(fakeOllamaWith(t, "0.15.6", ""), 96*gib); got != 40960 { // 262144 capped at trained
		t.Errorf("vram default capped at trained: %d", got)
	}
	if got := ctxOf(fakeOllamaWith(t, "0.15.6", ""), 24*gib); got != 32768 {
		t.Errorf("24GiB tier: %d", got)
	}
	if got := ctxOf(fakeOllamaWith(t, "0.15.6", ""), 0); got != 4096 {
		t.Errorf("unknown vram: %d", got)
	}
	if got := ctxOf(fakeOllamaWith(t, "0.12.3", ""), 96*gib); got != 4096 {
		t.Errorf("old server ignores vram tiers: %d", got)
	}
}

func TestResolveOllamaContextPrecedence(t *testing.T) {
	const gib = int64(1) << 30
	newVer := [3]int{0, 20, 0}
	cases := []struct {
		name string
		in   localprobe.OllamaContextInputs
		want int64
		rule string
	}{
		{"loaded beats modelfile and vram", localprobe.OllamaContextInputs{LoadedCtx: 8192, ModelfileCtx: 65536, TrainedCtx: 131072, VersionKnown: true, Version: newVer, VRAMBytes: 96 * gib}, 8192, "loaded"},
		{"loaded is not capped", localprobe.OllamaContextInputs{LoadedCtx: 65536, TrainedCtx: 8192}, 65536, "loaded"},
		{"modelfile beats vram", localprobe.OllamaContextInputs{ModelfileCtx: 16384, TrainedCtx: 131072, VersionKnown: true, Version: newVer, VRAMBytes: 96 * gib}, 16384, "modelfile"},
		{"modelfile capped at trained", localprobe.OllamaContextInputs{ModelfileCtx: 65536, TrainedCtx: 32768}, 32768, "capped"},
		{"vram tier", localprobe.OllamaContextInputs{TrainedCtx: 131072, VersionKnown: true, Version: newVer, VRAMBytes: 30 * gib}, 32768, "vram default"},
		{"vram unknown", localprobe.OllamaContextInputs{TrainedCtx: 131072, VersionKnown: true, Version: newVer}, 4096, "vram unknown"},
		{"old version flat", localprobe.OllamaContextInputs{TrainedCtx: 131072, VersionKnown: true, Version: [3]int{0, 12, 3}, VRAMBytes: 96 * gib}, 4096, "flat"},
		{"unknown version flat", localprobe.OllamaContextInputs{TrainedCtx: 131072, VRAMBytes: 96 * gib}, 4096, "flat"},
		{"trained smaller than default", localprobe.OllamaContextInputs{TrainedCtx: 2048, VersionKnown: true, Version: newVer, VRAMBytes: 8 * gib}, 2048, "capped"},
	}
	for _, c := range cases {
		got, rule := localprobe.ResolveOllamaContext(c.in)
		if got != c.want || !strings.Contains(rule, c.rule) {
			t.Errorf("%s: got %d (%s), want %d (%s)", c.name, got, rule, c.want, c.rule)
		}
	}
}

func TestOllamaVRAMTiers(t *testing.T) {
	const gib = int64(1) << 30
	for _, c := range []struct {
		vram int64
		want int64
	}{{0, 4096}, {8 * gib, 4096}, {22 * gib, 4096}, {23 * gib, 32768}, {47*gib - 1, 32768}, {47 * gib, 262144}, {192 * gib, 262144}} {
		if got := localprobe.OllamaVRAMTierContext(c.vram); got != c.want {
			t.Errorf("%d GiB: %d want %d", c.vram/gib, got, c.want)
		}
	}
	for _, c := range []struct{ mem, want int64 }{{16 * gib, 16 * gib * 2 / 3}, {36 * gib, 36 * gib * 2 / 3}, {128 * gib, 96 * gib}} {
		if got := metalWorkingSetBytes(c.mem); got != c.want {
			t.Errorf("metal %d: %d want %d", c.mem/gib, got, c.want)
		}
	}
	if got := parseNvidiaSMIMiB("24576\n24576\n"); got != 49152 {
		t.Errorf("nvidia-smi sum: %d", got)
	}
}

func TestParseOllamaShowModelfileCtxAndVersion(t *testing.T) {
	info, err := localprobe.ParseOllamaShow([]byte(`{"parameters":"stop \"x\"\nnum_ctx                        16384\ntemperature 0.6","model_info":{"general.architecture":"llama","llama.context_length":131072}}`))
	if err != nil || info.ModelfileCtx != 16384 || info.TrainedCtx != 131072 {
		t.Errorf("%+v %v", info, err)
	}
	for in, want := range map[string][3]int{`{"version":"0.12.3"}`: {0, 12, 3}, `{"version":"v0.15.6-rc1"}`: {0, 15, 6}} {
		if v, ok := localprobe.ParseOllamaVersion([]byte(in)); !ok || v != want {
			t.Errorf("%s: %v %v", in, v, ok)
		}
	}
	if _, ok := localprobe.ParseOllamaVersion([]byte(`{}`)); ok {
		t.Error("empty version must be unknown")
	}
}

func TestLocalModelParsers(t *testing.T) {
	lm := `{"data":[{"id":"llama-3","type":"llm","max_context_length":131072,"capabilities":["tool_use"]},
	{"id":"embed","type":"embeddings","max_context_length":2048},{"id":"vis","type":"vlm","max_context_length":8192,"loaded_context_length":4096}]}`
	models, err := localprobe.ParseLMStudioModels([]byte(lm))
	if err != nil || len(models) != 3 {
		t.Fatalf("%v %v", err, models)
	}
	if !models[0].SupportsChat || !models[0].SupportsTools || models[0].ContextWindow != 131072 {
		t.Errorf("llm = %+v", models[0])
	}
	if models[1].SupportsChat {
		t.Errorf("embeddings model must not support chat: %+v", models[1])
	}
	if !models[2].SupportsVision || models[2].ContextWindow != 4096 {
		t.Errorf("vlm = %+v", models[2])
	}

	n, vis, tools, err := localprobe.ParseLlamaCppProps([]byte(`{"default_generation_settings":{"n_ctx":16384},"modalities":{"vision":true}}`))
	if err != nil || n != 16384 || !vis || tools {
		t.Errorf("props: %d %v %v %v", n, vis, tools, err)
	}
	info := localprobe.OllamaModelInfo("x", localprobe.OllamaShow{}, false, 0, "", 4096)
	if !info.SupportsChat || info.SupportsTools || info.ContextWindow != 4096 {
		t.Errorf("unknown ollama model must be conservative: %+v", info)
	}
}

func TestLocalModelKindDetection(t *testing.T) {
	mk := func(extra map[string]string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				_, _ = io.WriteString(w, `{"data":[{"id":"m"}]}`)
				return
			}
			if b, ok := extra[r.URL.Path]; ok {
				_, _ = io.WriteString(w, b)
				return
			}
			http.NotFound(w, r)
		}))
	}
	cases := []struct {
		name  string
		extra map[string]string
		kind  string
	}{
		{"ollama", map[string]string{"/api/tags": `{"models":[]}`}, "ollama"},
		{"lmstudio", map[string]string{"/api/v0/models": `{"data":[]}`}, "lmstudio"},
		{"llamacpp", map[string]string{"/props": `{"default_generation_settings":{"n_ctx":4096}}`}, "llamacpp"},
		{"vllm", map[string]string{"/version": `{"version":"0.6"}`}, "vllm"},
		{"other", nil, "openai_compatible"},
	}
	for _, c := range cases {
		srv := mk(c.extra)
		snap := proberFor(srv.URL, localModelsConfig{}).probe(context.Background())
		srv.Close()
		if got := snap.inventory.Endpoints[0].Kind; got != c.kind {
			t.Errorf("%s: kind = %s, want %s", c.name, got, c.kind)
		}
	}
}

func TestLocalModelConfiguredEndpoints(t *testing.T) {
	srv := fakeOllama(t)
	cfg := parseLocalModelsConfig([]byte("models:\n  providers:\n    local:\n      base_url: " + srv.URL + "/v1\n      base_urls:\n        - http://127.0.0.1:1/v1\n        - {base_url: " + srv.URL + "}\n"))
	if got := len(cfg.endpoints()); got != 3 {
		t.Fatalf("endpoints = %d", got)
	}
	p := proberFor("http://127.0.0.1:2", cfg) // dead well-known one is dropped
	snap := p.probe(context.Background())
	if len(snap.inventory.Endpoints) != 2 { // duplicate of srv collapses; dead configured kept with error
		t.Fatalf("endpoints = %v", snap.inventory.Endpoints)
	}
	var dead, live *reliantv1.LocalModelEndpoint
	for _, ep := range snap.inventory.Endpoints {
		if ep.Error != "" {
			dead = ep
		} else {
			live = ep
		}
	}
	if dead == nil || live == nil || !strings.HasPrefix(live.Id, "cfg-") || live.Source != "configured" || live.Kind != "ollama" {
		t.Fatalf("live=%+v dead=%+v", live, dead)
	}
	if id2 := localprobe.EndpointID(srv.URL); id2 != live.Id {
		t.Errorf("id not stable: %s vs %s", id2, live.Id)
	}

	// Legacy single key only.
	single := parseLocalModelsConfig([]byte("models:\n  providers:\n    local:\n      base_url: http://localhost:9/v1\n"))
	if len(single.endpoints()) != 1 {
		t.Errorf("single key broken")
	}
}

func TestLocalModelChangeOnlyPublication(t *testing.T) {
	srv := fakeOllama(t)
	m := newLocalModelManager()
	m.prober = proberFor(srv.URL, localModelsConfig{})
	var mu sync.Mutex
	var sent []*reliantv1.DaemonMessage
	send := func(msg *reliantv1.DaemonMessage) error {
		mu.Lock()
		sent = append(sent, msg)
		mu.Unlock()
		return nil
	}
	ctx := context.Background()
	m.probeAndPublish(ctx, send, true)
	time.Sleep(1100 * time.Millisecond) // probed_at changes; must not count
	m.probeAndPublish(ctx, send, false)
	if len(sent) != 1 {
		t.Fatalf("unchanged inventory re-sent: %d messages", len(sent))
	}
	m.prober.wellKnown = nil // endpoint disappears
	m.probeAndPublish(ctx, send, false)
	if len(sent) != 2 || len(sent[1].GetLocalModelInventory().Endpoints) != 0 {
		t.Fatalf("change not published: %d", len(sent))
	}
	if m.current().inventory == nil {
		t.Fatal("snapshot not kept")
	}
}

type chunkSink struct {
	mu     sync.Mutex
	chunks []*reliantv1.LocalModelHTTPChunk
	done   chan struct{}
}

func newChunkSink() *chunkSink { return &chunkSink{done: make(chan struct{})} }

func (s *chunkSink) send(msg *reliantv1.DaemonMessage) error {
	c := msg.GetLocalModelHttpChunk()
	s.mu.Lock()
	s.chunks = append(s.chunks, c)
	s.mu.Unlock()
	if c.Done {
		close(s.done)
	}
	return nil
}

func (s *chunkSink) wait(t *testing.T) []*reliantv1.LocalModelHTTPChunk {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		t.Fatal("relay did not finish")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*reliantv1.LocalModelHTTPChunk(nil), s.chunks...)
}

func relayManagerFor(t *testing.T, root, kind string, models ...*reliantv1.LocalModelInfo) *localModelManager {
	t.Helper()
	m := newLocalModelManager()
	m.snap = &localInventorySnapshot{
		inventory: &reliantv1.LocalModelInventory{Endpoints: []*reliantv1.LocalModelEndpoint{{Id: "ollama", Kind: kind, Models: models}}},
		targets:   map[string]localEndpointTarget{"ollama": {root: root, kind: kind, apiKey: "sekret"}},
	}
	return m
}

func TestLocalModelRelayStreamsVerbatim(t *testing.T) {
	var gotBody, gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody, gotAuth, gotPath = string(b), r.Header.Get("Authorization"), r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for i := 0; i < 3; i++ {
			_, _ = io.WriteString(w, "data: chunk\n\n")
			fl.Flush()
			time.Sleep(80 * time.Millisecond)
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	m := relayManagerFor(t, srv.URL, "ollama", &reliantv1.LocalModelInfo{Name: "qwen3:latest", ContextWindow: 24000})
	sink := newChunkSink()
	go m.relay(context.Background(), sink.send, &reliantv1.LocalModelHTTPRequest{
		RequestId: "r1", EndpointId: "ollama", Method: "POST", Path: "/chat/completions",
		Headers: map[string]string{"Authorization": "Bearer server-supplied", "Connection": "close", "Content-Type": "application/json"},
		Body:    []byte(`{"model":"qwen3:latest","stream":true}`),
	})
	chunks := sink.wait(t)
	if chunks[0].Status != 200 || chunks[0].Headers["Content-Type"] != "text/event-stream" {
		t.Fatalf("head = %+v", chunks[0])
	}
	var body strings.Builder
	for i, c := range chunks {
		if c.Sequence != uint64(i) {
			t.Errorf("sequence %d at index %d", c.Sequence, i)
		}
		body.Write(c.Data)
	}
	if last := chunks[len(chunks)-1]; !last.Done || last.Error != "" {
		t.Fatalf("last = %+v", last)
	}
	if strings.Count(body.String(), "data: chunk") != 3 || !strings.Contains(body.String(), "[DONE]") {
		t.Errorf("body = %q", body.String())
	}
	if len(chunks) < 4 {
		t.Errorf("expected body to arrive in several chunks, got %d total", len(chunks))
	}
	if gotAuth != "Bearer sekret" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody != `{"model":"qwen3:latest","stream":true}` {
		t.Errorf("body must be relayed byte-identical: %s", gotBody)
	}
}

func TestLocalModelRelayRejects(t *testing.T) {
	m := relayManagerFor(t, "http://127.0.0.1:1", "ollama")
	for name, req := range map[string]*reliantv1.LocalModelHTTPRequest{
		"unknown endpoint": {RequestId: "a", EndpointId: "nope", Method: "GET", Path: "/models"},
		"bad path":         {RequestId: "b", EndpointId: "ollama", Method: "POST", Path: "/api/pull"},
		"bad method":       {RequestId: "c", EndpointId: "ollama", Method: "DELETE", Path: "/models"},
		"dial failure":     {RequestId: "d", EndpointId: "ollama", Method: "GET", Path: "/models"},
	} {
		sink := newChunkSink()
		go m.relay(context.Background(), sink.send, req)
		chunks := sink.wait(t)
		if len(chunks) != 1 || !chunks[0].Done || chunks[0].Error == "" || chunks[0].Status != 0 {
			t.Errorf("%s: chunks = %+v", name, chunks)
		}
	}
}

func TestLocalModelRelayCancel(t *testing.T) {
	started := make(chan struct{})
	upstreamGone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(upstreamGone)
	}))
	defer srv.Close()

	m := relayManagerFor(t, srv.URL, "openai_compatible")
	sink := newChunkSink()
	go m.relay(context.Background(), sink.send, &reliantv1.LocalModelHTTPRequest{RequestId: "c1", EndpointId: "ollama", Method: "GET", Path: "/models"})
	<-started
	m.cancelRelay("c1")
	chunks := sink.wait(t)
	if last := chunks[len(chunks)-1]; !last.Done || last.Error != "canceled" {
		t.Fatalf("last = %+v", last)
	}
	select {
	case <-upstreamGone:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream request was not canceled")
	}
}

func TestLocalModelRelayIdleTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	m := relayManagerFor(t, srv.URL, "openai_compatible")
	sink := newChunkSink()
	go m.relay(context.Background(), sink.send, &reliantv1.LocalModelHTTPRequest{RequestId: "i1", EndpointId: "ollama", Method: "GET", Path: "/models", IdleTimeoutMs: 200})
	chunks := sink.wait(t)
	if last := chunks[len(chunks)-1]; !last.Done || !strings.Contains(last.Error, "no data") {
		t.Fatalf("last = %+v", last)
	}
}

func TestLocalModelRelayConcurrencyLimit(t *testing.T) {
	var mu sync.Mutex
	cur, peak := 0, 0
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cur++
		peak = max(peak, cur)
		mu.Unlock()
		<-release
		mu.Lock()
		cur--
		mu.Unlock()
		_, _ = io.WriteString(w, "{}")
	}))
	defer srv.Close()
	m := relayManagerFor(t, srv.URL, "openai_compatible")
	var sinks []*chunkSink
	for i := 0; i < 8; i++ {
		s := newChunkSink()
		sinks = append(sinks, s)
		go m.relay(context.Background(), s.send, &reliantv1.LocalModelHTTPRequest{RequestId: string(rune('a' + i)), EndpointId: "ollama", Method: "GET", Path: "/models"})
	}
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	if cur != localRelayMaxConcurrent {
		t.Errorf("in flight = %d, want %d", cur, localRelayMaxConcurrent)
	}
	mu.Unlock()
	close(release)
	for _, s := range sinks {
		s.wait(t)
	}
	if peak > localRelayMaxConcurrent {
		t.Errorf("peak = %d", peak)
	}
}

func TestLocalModelsRealOllama(t *testing.T) {
	if os.Getenv("RELIANT_TEST_OLLAMA") != "1" {
		t.Skip("set RELIANT_TEST_OLLAMA=1 to probe a real local Ollama")
	}
	p := newLocalModelProber()
	p.loadCfg = func() localModelsConfig { return localModelsConfig{} }
	snap := p.probe(context.Background())
	out, _ := protojson.MarshalOptions{Multiline: true, Indent: "  ", EmitUnpopulated: true}.Marshal(snap.inventory)
	t.Logf("inventory:\n%s", out)
	var ollama *reliantv1.LocalModelEndpoint
	for _, ep := range snap.inventory.Endpoints {
		if ep.Id == "ollama" {
			ollama = ep
		}
	}
	if ollama == nil || ollama.Kind != "ollama" || len(ollama.Models) == 0 {
		t.Fatalf("ollama not detected: %+v", snap.inventory)
	}
	for _, m := range ollama.Models {
		if strings.Contains(m.Name, "embed") && m.SupportsChat {
			t.Errorf("embedding model offered for chat: %+v", m)
		}
		if m.ContextWindow <= 0 {
			t.Errorf("no context window: %+v", m)
		}
	}

	// Context honored by the real server: unloaded (rule-derived), then
	// loaded (authoritative /api/ps), compared with what Ollama really did.
	qwenCtx := func(s *localInventorySnapshot) int64 {
		for _, ep := range s.inventory.Endpoints {
			for _, m := range ep.Models {
				if m.Name == "qwen3:latest" {
					return m.ContextWindow
				}
			}
		}
		return -1
	}
	unload := func() {
		r, _ := http.Post("http://localhost:11434/api/generate", "application/json", strings.NewReader(`{"model":"qwen3:latest","keep_alive":0}`))
		if r != nil {
			r.Body.Close()
		}
		time.Sleep(2 * time.Second)
	}
	unload()
	unloadedCtx := qwenCtx(p.probe(context.Background()))
	r, err := http.Post("http://localhost:11434/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"qwen3:latest","messages":[{"role":"user","content":"hi"}],"max_tokens":2}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, r.Body)
	r.Body.Close()
	loadedCtx := qwenCtx(p.probe(context.Background()))
	t.Logf("qwen3 context_window: unloaded=%d loaded=%d vram_estimate=%d", unloadedCtx, loadedCtx, estimateLocalVRAMBytes())
	if unloadedCtx != loadedCtx {
		t.Errorf("published context before load (%d) differs from what Ollama actually loaded (%d)", unloadedCtx, loadedCtx)
	}

	// Relay end-to-end through the real server.
	m := newLocalModelManager()
	m.snap = snap
	sink := newChunkSink()
	go m.relay(context.Background(), sink.send, &reliantv1.LocalModelHTTPRequest{RequestId: "real", EndpointId: "ollama", Method: "GET", Path: "/models"})
	chunks := sink.wait(t)
	if chunks[0].Status != 200 || !chunks[len(chunks)-1].Done || chunks[len(chunks)-1].Error != "" {
		t.Fatalf("relay /models: %+v", chunks)
	}
}
