// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeLocalDirectory struct{ daemons []local.DaemonInventory }

func (f fakeLocalDirectory) LocalDaemons(context.Context, string) ([]local.DaemonInventory, error) {
	return f.daemons, nil
}

func ollamaInventory(infos ...*reliantv1.LocalModelInfo) *reliantv1.LocalModelInventory {
	return &reliantv1.LocalModelInventory{Endpoints: []*reliantv1.LocalModelEndpoint{{Id: "ollama", Kind: "ollama", Models: infos}}}
}

var testQwen = &reliantv1.LocalModelInfo{Name: "qwen3:latest", ContextWindow: 40960, SupportsTools: true, SupportsThinking: true, SupportsChat: true}

const localSSE = `data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"<think>\nok\n</think>\n\nPONG"},"finish_reason":null}]}

data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}]}

data: {"id":"c","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":22,"total_tokens":33}}

data: [DONE]

`

type relayCall struct{ userID, daemonID, endpointID string }

func localSpecFor(t *testing.T, daemons []local.DaemonInventory, prefer string, calls *[]relayCall, server *httptest.Server) *LocalModelSpec {
	t.Helper()
	return &LocalModelSpec{
		Directory:      fakeLocalDirectory{daemons: daemons},
		PreferDaemonID: prefer,
		Transport: func(userID, daemonID, endpointID string) http.RoundTripper {
			*calls = append(*calls, relayCall{userID, daemonID, endpointID})
			return roundTripFunc(func(req *http.Request) (*http.Response, error) {
				clone := req.Clone(req.Context())
				clone.URL.Scheme, clone.URL.Host = "http", strings.TrimPrefix(server.URL, "http://")
				return http.DefaultTransport.RoundTrip(clone)
			})
		},
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestResolveLLMCallLocalModelThroughRelayTransport(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, localSSE)
	}))
	defer server.Close()

	var calls []relayCall
	daemons := []local.DaemonInventory{
		{DaemonID: "laptop", Machine: "MacBook", Online: false, Inventory: ollamaInventory(testQwen)},
		{DaemonID: "gpu", Machine: "GPU box", Online: true, Inventory: ollamaInventory(testQwen)},
	}
	global := models.MustGetRegistry()
	before := len(global.ListAll())

	res, err := resolveLLMCall(context.Background(), nil, llmCallSpec{
		UserID:   "u1",
		Selector: models.ModelSelector{ID: "qwen3:latest@local"},
		Local:    localSpecFor(t, daemons, "", &calls, server),
	})
	require.NoError(t, err)

	assert.Equal(t, "qwen3:latest@local", res.ModelID)
	assert.Equal(t, "local", res.ProviderDriver)
	assert.Equal(t, int64(40960), res.Model.ContextWindow, "window is the published one, not 200k")
	assert.Equal(t, []relayCall{{"u1", "gpu", "ollama"}}, calls, "the online daemon serves it, offline one is skipped")
	assert.Empty(t, res.ThinkingLevel, "no level requested, none sent")
	assert.Equal(t, before, len(global.ListAll()), "local models must never enter the global registry")
	_, inRegistry := global.GetDefinition("qwen3:latest")
	assert.False(t, inRegistry)

	events := res.Driver.StreamResponse(context.Background(), nil, []message.Message{ProbeUserMessage("hi")}, nil)
	var content, thinking string
	var usage llm.TokenUsage
	for ev := range events {
		switch ev.Type {
		case llm.EventContentDelta:
			content += ev.Content
		case llm.EventThinkingDelta:
			thinking += ev.Thinking
		case llm.EventComplete:
			usage = ev.Response.Usage
		case llm.EventError:
			t.Fatalf("stream error: %v", ev.Error)
		}
	}
	assert.Equal(t, "PONG", content)
	assert.Equal(t, "ok", strings.TrimSpace(thinking))
	assert.Equal(t, int64(22), usage.OutputTokens)
	assert.Contains(t, gotBody, `"model":"qwen3:latest"`)
}

func TestResolveLLMCallLocalPinnedProviderAndOfflineError(t *testing.T) {
	var calls []relayCall
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	daemons := []local.DaemonInventory{
		{DaemonID: "laptop", Machine: "Sean's MacBook", Online: false, Inventory: ollamaInventory(testQwen)},
		{DaemonID: "gpu", Machine: "GPU box", Online: true, Inventory: ollamaInventory(testQwen)},
	}

	_, err := resolveLLMCall(context.Background(), nil, llmCallSpec{
		UserID:   "u1",
		Selector: models.ModelSelector{ID: "qwen3:latest@local", Providers: []string{"local:laptop"}},
		Local:    localSpecFor(t, daemons, "", &calls, server),
	})
	var unavailable *local.UnavailableError
	require.ErrorAs(t, err, &unavailable)
	assert.Equal(t, "Local model qwen3:latest on Sean's MacBook is unavailable: that machine is offline", err.Error())
	assert.NotContains(t, strings.ToLower(err.Error()), "api key", "an offline machine is not a credentials problem")
	assert.Empty(t, calls)

	res, err := resolveLLMCall(context.Background(), nil, llmCallSpec{
		UserID:   "u1",
		Selector: models.ModelSelector{ID: "qwen3:latest@local", Providers: []string{"local:gpu"}},
		Local:    localSpecFor(t, daemons, "laptop", &calls, server),
	})
	require.NoError(t, err)
	assert.Equal(t, "qwen3:latest@local", res.ModelID)
}

func TestResolveLLMCallLocalUnreachableWithoutRelay(t *testing.T) {
	_, err := resolveLLMCall(context.Background(), nil, llmCallSpec{
		UserID:   "u1",
		Selector: models.ModelSelector{ID: "qwen3:latest@local"},
	})
	var unavailable *local.UnavailableError
	require.ErrorAs(t, err, &unavailable)
}

func TestResolveLLMCallThinkingLevelReconciledForLocal(t *testing.T) {
	var calls []relayCall
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	gptOSS := &reliantv1.LocalModelInfo{Name: "gpt-oss:20b", ContextWindow: 131072, SupportsTools: true, SupportsThinking: true, SupportsChat: true}
	daemons := []local.DaemonInventory{{DaemonID: "gpu", Machine: "GPU box", Online: true, Inventory: ollamaInventory(testQwen, gptOSS)}}

	// qwen3 on Ollama takes no level: dropped, never sent (it would 400).
	res, err := resolveLLMCall(context.Background(), nil, llmCallSpec{UserID: "u", ThinkingLevel: "high",
		Selector: models.ModelSelector{ID: "qwen3:latest@local"}, Local: localSpecFor(t, daemons, "", &calls, server)})
	require.NoError(t, err)
	assert.Empty(t, res.ThinkingLevel)

	res, err = resolveLLMCall(context.Background(), nil, llmCallSpec{UserID: "u", ThinkingLevel: "high",
		Selector: models.ModelSelector{ID: "gpt-oss:20b@local"}, Local: localSpecFor(t, daemons, "", &calls, server)})
	require.NoError(t, err)
	assert.Equal(t, "high", res.ThinkingLevel)
}

// A tag selector must never land on a local model, even when the user has one.
func TestTagSelectorNeverResolvesToLocalModel(t *testing.T) {
	spec := llmCallSpec{UserID: "u", Selector: models.ModelSelector{Tags: []string{"local"}}}
	_, err := resolveLLMCall(context.Background(), nil, spec)
	if err == nil {
		return
	}
	var unavailable *local.UnavailableError
	assert.False(t, errors.As(err, &unavailable), "tags are the registry's business, not the local resolver's")
}

func TestResolveLLMCall_PinnedLocalTagPref(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	moderate := models.ModelSelector{Tags: []string{models.TagModerate}}
	reader := fakeSettingsReader{rows: map[string]string{
		"model.tag_config.moderate": `{"model_id":"qwen3:latest@local","providers":["local:gpu"]}`,
	}}

	t.Run("online machine serves the pinned model", func(t *testing.T) {
		var calls []relayCall
		daemons := []local.DaemonInventory{{DaemonID: "gpu", Machine: "GPU box", Online: true, Inventory: ollamaInventory(testQwen)}}
		res, err := resolveLLMCall(context.Background(), nil, llmCallSpec{
			UserID: "u1", Selector: moderate, TagPrefsReader: reader,
			Local: localSpecFor(t, daemons, "", &calls, server),
		})
		require.NoError(t, err)
		assert.Equal(t, "qwen3:latest@local", res.ModelID)
		assert.Equal(t, "local", res.ProviderDriver)
		assert.Equal(t, []relayCall{{"u1", "gpu", "ollama"}}, calls)
	})

	t.Run("offline machine falls back to the tier", func(t *testing.T) {
		userID := "user-" + uuid.NewString()
		ctx := context.Background()
		provisionProviderKeys(t, ctx, userID, []string{"anthropic"})
		captured := &capturedDriverOptions{}
		original := drivers.GetDriver
		drivers.GetDriver = captureDriverOptionsResolver(captured)
		t.Cleanup(func() { drivers.GetDriver = original })

		var calls []relayCall
		daemons := []local.DaemonInventory{{DaemonID: "gpu", Machine: "GPU box", Online: false, Inventory: ollamaInventory(testQwen)}}
		res, err := resolveLLMCall(ctx, nil, llmCallSpec{
			UserID: userID, SessionID: "s", Selector: moderate, TagPrefsReader: reader,
			Local: localSpecFor(t, daemons, "", &calls, server),
		})
		require.NoError(t, err)
		assert.Equal(t, "claude-5.5-sonnet@anthropic", res.ModelID)
		assert.Empty(t, calls)
	})
}

type endpointDirectory struct {
	daemons   []local.DaemonInventory
	endpoints []local.EndpointConfig
}

func (d endpointDirectory) LocalDaemons(context.Context, string) ([]local.DaemonInventory, error) {
	return d.daemons, nil
}
func (d endpointDirectory) ConfiguredEndpoints(context.Context, string) ([]local.EndpointConfig, error) {
	return d.endpoints, nil
}

func TestResolveLLMCall_PinnedCustomEndpointTagPref(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, localSSE)
	}))
	defer server.Close()

	moderate := models.ModelSelector{Tags: []string{models.TagModerate}}
	pin := func(endpointID string) fakeSettingsReader {
		return fakeSettingsReader{rows: map[string]string{
			"model.tag_config.moderate": `{"model_id":"llama@local","providers":["endpoint:` + endpointID + `"],"temperature":0.4}`,
		}}
	}
	probe := &reliantv1.LocalModelEndpoint{Models: []*reliantv1.LocalModelInfo{{Name: "llama", SupportsChat: true, ContextWindow: 65536, SupportsTools: true}}}
	direct := local.EndpointConfig{ID: "ep-direct", Name: "Lab", BaseURL: server.URL + "/v1", Route: "direct", Probe: probe,
		Models: []*reliantv1.ModelEndpointModel{{Name: "llama", ExtraBodyJson: `{"min_p":0.05}`}}}
	via := local.EndpointConfig{ID: "ep-via", Name: "Home", BaseURL: "http://10.0.0.5:8000/v1", Route: "via_daemon", DaemonID: "gpu", Online: true, Probe: probe}

	spec := func(t *testing.T, eps ...local.EndpointConfig) (*LocalModelSpec, *[]relayCall) {
		var calls []relayCall
		relay := func(userID, daemonID, endpointID string) http.RoundTripper {
			calls = append(calls, relayCall{userID, daemonID, endpointID})
			return roundTripFunc(func(req *http.Request) (*http.Response, error) {
				clone := req.Clone(req.Context())
				clone.URL.Scheme, clone.URL.Host = "http", strings.TrimPrefix(server.URL, "http://")
				return http.DefaultTransport.RoundTrip(clone)
			})
		}
		return &LocalModelSpec{
			Directory: endpointDirectory{endpoints: eps},
			Transport: relay,
			Custom:    &local.CustomRoutes{Relay: relay, Policy: netguard.Policy{AllowPrivate: true}},
		}, &calls
	}

	t.Run("a direct endpoint pin resolves, carries the tag temperature, and reaches the server with its extra body", func(t *testing.T) {
		ls, calls := spec(t, direct)
		res, err := resolveLLMCall(context.Background(), nil, llmCallSpec{UserID: "u1", SessionID: "s", Selector: moderate, TagPrefsReader: pin("ep-direct"), Local: ls})
		require.NoError(t, err)
		assert.Equal(t, "llama@local", res.ModelID)
		assert.Equal(t, "local", res.ProviderDriver)
		assert.Equal(t, 65536, res.Definition.Capabilities.MaxContextWindow)
		require.NotNil(t, res.Temperature)
		assert.Equal(t, 0.4, *res.Temperature)
		assert.Empty(t, *calls, "direct never touches the daemon relay")

		for range res.Driver.StreamResponse(context.Background(), nil, []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}}, nil) {
		}
		assert.Contains(t, gotBody, `"min_p":0.05`)
	})

	t.Run("a via-machine endpoint pin goes through that machine's relay by its relay id", func(t *testing.T) {
		ls, calls := spec(t, via)
		res, err := resolveLLMCall(context.Background(), nil, llmCallSpec{UserID: "u1", SessionID: "s", Selector: moderate, TagPrefsReader: pin("ep-via"), Local: ls})
		require.NoError(t, err)
		assert.Equal(t, "llama@local", res.ModelID)
		require.Len(t, *calls, 1)
		assert.Equal(t, "gpu", (*calls)[0].daemonID)
		assert.Equal(t, local.RelayEndpointID("http://10.0.0.5:8000/v1"), (*calls)[0].endpointID)
	})

	fallsBackToTier := func(t *testing.T, ls *LocalModelSpec, reader fakeSettingsReader) {
		userID := "user-" + uuid.NewString()
		ctx := context.Background()
		provisionProviderKeys(t, ctx, userID, []string{"anthropic"})
		captured := &capturedDriverOptions{}
		original := drivers.GetDriver
		drivers.GetDriver = captureDriverOptionsResolver(captured)
		t.Cleanup(func() { drivers.GetDriver = original })
		res, err := resolveLLMCall(ctx, nil, llmCallSpec{UserID: userID, SessionID: "s", Selector: moderate, TagPrefsReader: reader, Local: ls})
		require.NoError(t, err)
		assert.Equal(t, "claude-5.5-sonnet@anthropic", res.ModelID, "the tier runs instead")
	}

	t.Run("a deleted endpoint falls back to the tier", func(t *testing.T) {
		ls, _ := spec(t) // endpoint no longer exists
		fallsBackToTier(t, ls, pin("ep-gone"))
	})
	t.Run("an offline machine falls back to the tier", func(t *testing.T) {
		off := via
		off.Online = false
		ls, calls := spec(t, off)
		fallsBackToTier(t, ls, pin("ep-via"))
		assert.Empty(t, *calls)
	})
	t.Run("a hidden model falls back to the tier", func(t *testing.T) {
		hidden := direct
		hidden.Models = []*reliantv1.ModelEndpointModel{{Name: "llama", Hidden: true}}
		ls, _ := spec(t, hidden)
		fallsBackToTier(t, ls, pin("ep-direct"))
	})
	t.Run("an endpoint pin never resolves to another endpoint's model of the same name", func(t *testing.T) {
		other := via
		other.ID = "ep-other"
		ls, calls := spec(t, other)
		fallsBackToTier(t, ls, pin("ep-direct"))
		assert.Empty(t, *calls)
	})
}
