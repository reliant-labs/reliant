// Copyright (c) 2025 Reliant Labs
//
//forge:exclude-contract: a stateless prober/parser for OpenAI-compatible model servers; its only I/O is the *http.Client the caller supplies
package localprobe

// Probing an OpenAI-compatible server: list its models, detect the server
// family (Ollama, LM Studio, llama.cpp, vLLM) and ask the family's own
// metadata endpoints what /v1/models cannot say (context window, vision,
// tools, thinking).
//
// The caller supplies the *http.Client, which is what decides where the
// bytes go: a plain client for a server Reliant dials directly, an
// SSRF-guarded one for a user-supplied URL in a hosted deployment.
//
// The daemon's own prober (internal/toolexec/daemonruntime/localmodels_probe.go)
// predates this package and carries copies of the same parsers plus daemon-only
// work (VRAM estimation). It should delegate its pure parsing here; that was
// left alone because the daemon is outside this change's scope.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

const (
	listTimeout   = 8 * time.Second
	detectTimeout = 2 * time.Second
	enrichTimeout = 5 * time.Second
	maxBodyBytes  = 8 << 20
	// maxEnriched bounds per-model metadata calls so a server hosting
	// hundreds of models cannot turn one probe into hundreds of requests.
	maxEnriched = 64
)

// Target is one server to probe.
type Target struct {
	// Root is the base URL without a trailing /v1 or slash (see NormalizeRoot).
	Root    string
	APIKey  string
	Headers map[string]string
}

// NormalizeRoot strips query, fragment, a trailing slash and a trailing /v1,
// the same convention the daemon uses for its endpoint ids.
func NormalizeRoot(base string) (string, error) {
	base = strings.TrimSpace(base)
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("invalid base URL %q", base)
	}
	u.RawQuery, u.Fragment, u.User = "", "", nil
	p := strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/v1")
	u.Path = strings.TrimRight(p, "/")
	u.RawPath = ""
	return u.String(), nil
}

// EndpointID is the id the daemon gives a user-configured endpoint at root:
// the relay authorizes requests by this id, so a VIA_DAEMON endpoint must be
// addressed by it.
func EndpointID(root string) string {
	sum := sha256.Sum256([]byte(root))
	return "cfg-" + hex.EncodeToString(sum[:])[:8]
}

// Options tunes a probe. The zero value is right for a server reached over the
// network; the daemon sets the fields only it can know.
type Options struct {
	// ListTimeout bounds GET /v1/models; DetectTimeout bounds each kind probe.
	ListTimeout, DetectTimeout time.Duration
	// VRAMBytes reports the GPU-addressable memory of the machine RUNNING the
	// server, 0 = unknown. Only the daemon can answer for its own machine; a
	// server probed over the network leaves it nil, and Ollama's VRAM-tiered
	// default context then resolves to its conservative floor.
	VRAMBytes func() int64
	// ShowCache memoizes /api/show by model name + digest; optional.
	ShowCache *ShowCache
	// UnknownContext, when > 0, is published as the context window of a model
	// whose server did not report one. The proto says 0 = unknown, which is what
	// a server probed over the network publishes; the daemon predates that and
	// publishes its conservative 8192 instead, which its UI warns on.
	UnknownContext int64
	// OnContext observes how an Ollama model's honored context was decided.
	OnContext func(model string, context int64, rule string)
}

// ShowCache memoizes Ollama /api/show results, which are expensive and change
// only when a model's digest does.
type ShowCache struct {
	mu sync.Mutex
	m  map[string]OllamaShow
}

func NewShowCache() *ShowCache { return &ShowCache{m: map[string]OllamaShow{}} }

func (c *ShowCache) get(k string) (OllamaShow, bool) {
	if c == nil {
		return OllamaShow{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[k]
	return v, ok
}

func (c *ShowCache) put(k string, v OllamaShow) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.m[k] = v
	c.mu.Unlock()
}

// Probe asks the server what it serves. It never returns nil: a server that
// does not answer yields an endpoint with Error set and no models.
func Probe(ctx context.Context, client *http.Client, id string, t Target, opt Options) *reliantv1.LocalModelEndpoint {
	ep := &reliantv1.LocalModelEndpoint{Id: id, Kind: "openai_compatible", BaseUrl: t.Root + "/v1", Source: "configured"}
	if _, err := NormalizeRoot(t.Root); err != nil {
		ep.Error = err.Error()
		return ep
	}
	f := fetcher{client: client, t: t, opt: opt}
	listTO := listTimeout
	if opt.ListTimeout > 0 {
		listTO = opt.ListTimeout
	}

	body, status, err := f.get(ctx, "/v1/models", listTO)
	if err != nil {
		ep.Error = ShortError(err)
		return ep
	}
	if status < 200 || status > 299 {
		ep.Error = fmt.Sprintf("GET /v1/models returned HTTP %d", status)
		return ep
	}
	names, err := ParseModelIDs(body)
	if err != nil {
		ep.Error = "GET /v1/models did not return an OpenAI-style model list"
		return ep
	}

	ep.Kind = f.detectKind(ctx)
	switch ep.Kind {
	case "ollama":
		ep.Models = f.ollamaModels(ctx, names)
	case "lmstudio":
		ep.Models = f.lmStudioModels(ctx, names)
	case "llamacpp":
		ep.Models = f.llamaCppModels(ctx, names)
	default:
		ep.Models = UnknownModels(names)
	}
	if opt.UnknownContext > 0 {
		for _, m := range ep.Models {
			if m.ContextWindow <= 0 {
				m.ContextWindow = opt.UnknownContext
			}
		}
	}
	sort.Slice(ep.Models, func(i, j int) bool { return ep.Models[i].Name < ep.Models[j].Name })
	return ep
}

// ShortError trims a Go network error to the part a user can act on.
func ShortError(err error) string {
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 && strings.Contains(msg, "dial tcp") {
		return strings.TrimSpace(msg[i+2:])
	}
	return msg
}

type fetcher struct {
	client *http.Client
	t      Target
	opt    Options
}

func (f fetcher) detectTO() time.Duration {
	if f.opt.DetectTimeout > 0 {
		return f.opt.DetectTimeout
	}
	return detectTimeout
}

func (f fetcher) do(ctx context.Context, method, path string, payload any, timeout time.Duration) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.Reader
	if payload != nil {
		b, _ := json.Marshal(payload)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, f.t.Root+path, body)
	if err != nil {
		return nil, 0, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range f.t.Headers {
		req.Header.Set(k, v)
	}
	if f.t.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+f.t.APIKey)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	return out, resp.StatusCode, err
}

func (f fetcher) get(ctx context.Context, path string, timeout time.Duration) ([]byte, int, error) {
	return f.do(ctx, http.MethodGet, path, nil, timeout)
}

// ParseModelIDs reads an OpenAI-style {"data":[{"id":...}]} list.
func ParseModelIDs(body []byte) ([]string, error) {
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(list.Data))
	for _, m := range list.Data {
		if m.ID != "" {
			names = append(names, m.ID)
		}
	}
	return names, nil
}

// UnknownModels is what a server that reports only ids gets: chat-capable,
// context unknown (0), no capabilities claimed.
func UnknownModels(names []string) []*reliantv1.LocalModelInfo {
	out := make([]*reliantv1.LocalModelInfo, 0, len(names))
	for _, n := range names {
		out = append(out, &reliantv1.LocalModelInfo{Name: n, SupportsChat: true})
	}
	return out
}

func (f fetcher) detectKind(ctx context.Context) string {
	if body, st, err := f.get(ctx, "/api/tags", f.detectTO()); err == nil && st == 200 {
		var t struct {
			Models *json.RawMessage `json:"models"`
		}
		if json.Unmarshal(body, &t) == nil && t.Models != nil {
			return "ollama"
		}
	}
	if body, st, err := f.get(ctx, "/api/v0/models", f.detectTO()); err == nil && st == 200 {
		var t struct {
			Data *json.RawMessage `json:"data"`
		}
		if json.Unmarshal(body, &t) == nil && t.Data != nil {
			return "lmstudio"
		}
	}
	if body, st, err := f.get(ctx, "/props", f.detectTO()); err == nil && st == 200 && json.Valid(body) {
		return "llamacpp"
	}
	if body, st, err := f.get(ctx, "/version", f.detectTO()); err == nil && st == 200 {
		var v struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(body, &v) == nil && v.Version != "" {
			return "vllm"
		}
	}
	return "openai_compatible"
}

// ---- Ollama ----

// OllamaTieredDefaultSince is the first release verified (from source) to pick
// the default context from VRAM. Older servers use a flat 4096.
var OllamaTieredDefaultSince = [3]int{0, 15, 6}

// OllamaShow is what /api/show tells us that /v1/models does not.
type OllamaShow struct {
	Capabilities []string
	TrainedCtx   int64
	ModelfileCtx int64 // PARAMETER num_ctx in the Modelfile, 0 = unset
}

// ParseOllamaShow reads an /api/show response.
func ParseOllamaShow(body []byte) (OllamaShow, error) {
	var s struct {
		Capabilities []string       `json:"capabilities"`
		Parameters   string         `json:"parameters"`
		ModelInfo    map[string]any `json:"model_info"`
	}
	if err := json.Unmarshal(body, &s); err != nil {
		return OllamaShow{}, err
	}
	info := OllamaShow{Capabilities: s.Capabilities}
	for _, line := range strings.Split(s.Parameters, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "num_ctx" {
			if n, err := strconv.ParseInt(fields[1], 10, 64); err == nil && n > 0 {
				info.ModelfileCtx = n
			}
		}
	}
	if arch, _ := s.ModelInfo["general.architecture"].(string); arch != "" {
		if v, ok := number(s.ModelInfo[arch+".context_length"]); ok {
			info.TrainedCtx = v
		}
	}
	if info.TrainedCtx == 0 {
		for k, v := range s.ModelInfo {
			if strings.HasSuffix(k, ".context_length") {
				if n, ok := number(v); ok && n > info.TrainedCtx {
					info.TrainedCtx = n
				}
			}
		}
	}
	return info, nil
}

func number(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	}
	return 0, false
}

// ParseOllamaVersion extracts major.minor.patch from "0.12.3" / "v0.15.6-rc1".
func ParseOllamaVersion(body []byte) ([3]int, bool) {
	var v struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(body, &v) != nil || v.Version == "" {
		return [3]int{}, false
	}
	s := strings.TrimPrefix(v.Version, "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	var out [3]int
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return out, false
	}
	for i := 0; i < len(parts) && i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// VersionAtLeast reports v >= min.
func VersionAtLeast(v, min [3]int) bool {
	for i := range v {
		if v[i] != min[i] {
			return v[i] > min[i]
		}
	}
	return true
}

// OllamaVRAMTierContext is Ollama's documented VRAM-based default context.
// Ollama's own thresholds are 47/23 GiB to absorb rounding.
func OllamaVRAMTierContext(vramBytes int64) int64 {
	const gib = int64(1) << 30
	switch {
	case vramBytes >= 47*gib:
		return 262144
	case vramBytes >= 23*gib:
		return 32768
	default:
		return 4096
	}
}

// OllamaContextInputs is everything the honored-context decision needs.
type OllamaContextInputs struct {
	LoadedCtx    int64 // /api/ps context_length, 0 = not loaded
	ModelfileCtx int64
	TrainedCtx   int64
	VersionKnown bool
	Version      [3]int
	VRAMBytes    int64 // 0 = unknown
}

// ResolveOllamaContext returns the context Ollama will honor on /v1 and the
// rule that produced it. Precedence: loaded > Modelfile > server default, then
// capped at the trained context (a loaded value is authoritative and never
// capped).
func ResolveOllamaContext(in OllamaContextInputs) (int64, string) {
	var ctx int64
	var rule string
	switch {
	case in.LoadedCtx > 0:
		return in.LoadedCtx, "loaded (/api/ps)"
	case in.ModelfileCtx > 0:
		ctx, rule = in.ModelfileCtx, "modelfile num_ctx"
	case in.VersionKnown && VersionAtLeast(in.Version, OllamaTieredDefaultSince):
		if in.VRAMBytes > 0 {
			ctx, rule = OllamaVRAMTierContext(in.VRAMBytes), "vram default"
		} else {
			ctx, rule = 4096, "vram unknown, assumed 4096"
		}
	default:
		ctx, rule = 4096, "flat default (pre-vram-tier or unknown version)"
	}
	if in.TrainedCtx > 0 && ctx > in.TrainedCtx {
		ctx, rule = in.TrainedCtx, rule+", capped at trained"
	}
	return ctx, rule
}

// OllamaModelInfo maps /api/show onto the inventory shape. ctx is the context
// the server will honor.
func OllamaModelInfo(name string, show OllamaShow, known bool, sizeBytes int64, paramSize string, ctx int64) *reliantv1.LocalModelInfo {
	m := &reliantv1.LocalModelInfo{Name: name, SizeBytes: sizeBytes, ParameterSize: paramSize, ContextWindow: ctx}
	if !known {
		m.SupportsChat = true
		return m
	}
	has := func(c string) bool {
		for _, x := range show.Capabilities {
			if x == c {
				return true
			}
		}
		return false
	}
	if len(show.Capabilities) == 0 {
		m.SupportsChat = true
		return m
	}
	m.SupportsChat = has("completion") || !has("embedding")
	m.SupportsTools = has("tools")
	m.SupportsVision = has("vision")
	m.SupportsThinking = has("thinking")
	return m
}

func (f fetcher) ollamaModels(ctx context.Context, names []string) []*reliantv1.LocalModelInfo {
	type tagInfo struct {
		size   int64
		digest string
		param  string
	}
	tags := map[string]tagInfo{}
	if body, st, err := f.get(ctx, "/api/tags", enrichTimeout); err == nil && st == 200 {
		var t struct {
			Models []struct {
				Name    string `json:"name"`
				Size    int64  `json:"size"`
				Digest  string `json:"digest"`
				Details struct {
					ParameterSize string `json:"parameter_size"`
				} `json:"details"`
			} `json:"models"`
		}
		if json.Unmarshal(body, &t) == nil {
			for _, m := range t.Models {
				tags[m.Name] = tagInfo{size: m.Size, digest: m.Digest, param: m.Details.ParameterSize}
			}
		}
	}

	base := OllamaContextInputs{}
	if body, st, err := f.get(ctx, "/api/version", enrichTimeout); err == nil && st == 200 {
		base.Version, base.VersionKnown = ParseOllamaVersion(body)
	}
	if base.VersionKnown && VersionAtLeast(base.Version, OllamaTieredDefaultSince) && f.opt.VRAMBytes != nil {
		base.VRAMBytes = f.opt.VRAMBytes()
	}

	loaded := map[string]int64{}
	if body, st, err := f.get(ctx, "/api/ps", enrichTimeout); err == nil && st == 200 {
		var r struct {
			Models []struct {
				Name          string `json:"name"`
				Model         string `json:"model"`
				ContextLength int64  `json:"context_length"`
			} `json:"models"`
		}
		if json.Unmarshal(body, &r) == nil {
			for _, m := range r.Models {
				if m.ContextLength > 0 {
					loaded[m.Name] = m.ContextLength
					if m.Model != "" {
						loaded[m.Model] = m.ContextLength
					}
				}
			}
		}
	}

	out := make([]*reliantv1.LocalModelInfo, len(names))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, name := range names {
		if i >= maxEnriched {
			out[i] = &reliantv1.LocalModelInfo{Name: name, SupportsChat: true}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			tag := tags[name]
			key := name + "|" + tag.digest
			show, known := f.opt.ShowCache.get(key)
			if !known {
				if body, st, err := f.do(ctx, http.MethodPost, "/api/show", map[string]string{"model": name}, enrichTimeout); err == nil && st == 200 {
					if s, perr := ParseOllamaShow(body); perr == nil {
						show, known = s, true
						if tag.digest != "" {
							f.opt.ShowCache.put(key, s)
						}
					}
				}
			}
			in := base
			in.LoadedCtx, in.ModelfileCtx, in.TrainedCtx = loaded[name], show.ModelfileCtx, show.TrainedCtx
			honored, rule := ResolveOllamaContext(in)
			if f.opt.OnContext != nil {
				f.opt.OnContext(name, honored, rule)
			}
			out[i] = OllamaModelInfo(name, show, known, tag.size, tag.param, honored)
		}()
	}
	wg.Wait()
	return out
}

// ---- LM Studio ----

// ParseLMStudioModels reads /api/v0/models.
func ParseLMStudioModels(body []byte) ([]*reliantv1.LocalModelInfo, error) {
	var r struct {
		Data []struct {
			ID                  string   `json:"id"`
			Type                string   `json:"type"`
			MaxContextLength    int64    `json:"max_context_length"`
			LoadedContextLength int64    `json:"loaded_context_length"`
			Capabilities        []string `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	out := make([]*reliantv1.LocalModelInfo, 0, len(r.Data))
	for _, d := range r.Data {
		if d.ID == "" {
			continue
		}
		m := &reliantv1.LocalModelInfo{
			Name:           d.ID,
			ContextWindow:  d.MaxContextLength,
			SupportsChat:   d.Type != "embeddings",
			SupportsVision: d.Type == "vlm",
		}
		if d.LoadedContextLength > 0 {
			m.ContextWindow = d.LoadedContextLength
		}
		for _, c := range d.Capabilities {
			if c == "tool_use" {
				m.SupportsTools = true
			}
		}
		out = append(out, m)
	}
	return out, nil
}

func (f fetcher) lmStudioModels(ctx context.Context, names []string) []*reliantv1.LocalModelInfo {
	if body, st, err := f.get(ctx, "/api/v0/models", enrichTimeout); err == nil && st == 200 {
		if models, perr := ParseLMStudioModels(body); perr == nil && len(models) > 0 {
			return models
		}
	}
	return UnknownModels(names)
}

// ---- llama.cpp ----

// ParseLlamaCppProps reads /props.
func ParseLlamaCppProps(body []byte) (nCtx int64, vision, tools bool, err error) {
	var r struct {
		DefaultGenerationSettings struct {
			NCtx   int64 `json:"n_ctx"`
			Params struct {
				NCtx int64 `json:"n_ctx"`
			} `json:"params"`
		} `json:"default_generation_settings"`
		Modalities struct {
			Vision bool `json:"vision"`
		} `json:"modalities"`
		ChatTemplateCaps struct {
			SupportsTools bool `json:"supports_tools"`
		} `json:"chat_template_caps"`
	}
	if err = json.Unmarshal(body, &r); err != nil {
		return
	}
	nCtx = r.DefaultGenerationSettings.NCtx
	if nCtx == 0 {
		nCtx = r.DefaultGenerationSettings.Params.NCtx
	}
	return nCtx, r.Modalities.Vision, r.ChatTemplateCaps.SupportsTools, nil
}

func (f fetcher) llamaCppModels(ctx context.Context, names []string) []*reliantv1.LocalModelInfo {
	models := UnknownModels(names)
	body, st, err := f.get(ctx, "/props", enrichTimeout)
	if err != nil || st != 200 {
		return models
	}
	nCtx, vision, tools, err := ParseLlamaCppProps(body)
	if err != nil {
		return models
	}
	for _, m := range models {
		if nCtx > 0 {
			m.ContextWindow = nCtx
		}
		m.SupportsVision, m.SupportsTools = vision, tools
	}
	return models
}
