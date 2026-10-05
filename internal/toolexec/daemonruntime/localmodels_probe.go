// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/localprobe"
	"github.com/reliant-labs/reliant/internal/logging"
	"gopkg.in/yaml.v3"
)

const (
	localModelDetectTimeout    = time.Second
	localModelConfiguredTO     = 3 * time.Second
	localModelEnrichTimeout    = 5 * time.Second
	localModelUnknownCtx       = 8192
	localModelMaxProbeBodySize = 8 << 20
)

// wellKnownLocalEndpoint is a port probed on the daemon's machine.
type wellKnownLocalEndpoint struct {
	id   string
	root string // scheme://host:port, no /v1
}

func defaultWellKnownLocalEndpoints() []wellKnownLocalEndpoint {
	return []wellKnownLocalEndpoint{
		{id: "ollama", root: "http://localhost:11434"},
		{id: "lmstudio", root: "http://localhost:1234"},
		{id: "llamacpp", root: "http://localhost:8080"},
		{id: "vllm", root: "http://localhost:8000"},
	}
}

// localEndpointConfig is one user-configured endpoint.
type localEndpointConfig struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
}

// UnmarshalYAML accepts either a bare URL string or a {base_url, api_key} map.
func (e *localEndpointConfig) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		return node.Decode(&e.BaseURL)
	}
	type plain localEndpointConfig
	return node.Decode((*plain)(e))
}

// localModelsConfig is the `models.providers.local` block of the user config.
// The single `base_url` key keeps working; `base_urls` adds more endpoints.
type localModelsConfig struct {
	BaseURL  string                `yaml:"base_url"`
	BaseURLs []localEndpointConfig `yaml:"base_urls"`
	APIKey   string                `yaml:"api_key"`
}

func (c localModelsConfig) endpoints() []localEndpointConfig {
	var out []localEndpointConfig
	if strings.TrimSpace(c.BaseURL) != "" {
		out = append(out, localEndpointConfig{BaseURL: c.BaseURL, APIKey: c.APIKey})
	}
	for _, e := range c.BaseURLs {
		if strings.TrimSpace(e.BaseURL) == "" {
			continue
		}
		if e.APIKey == "" {
			e.APIKey = c.APIKey
		}
		out = append(out, e)
	}
	return out
}

// loadLocalModelsConfig reads models.providers.local from the user-scope
// config (~/.reliant/config.yaml). A missing or unparsable file is "no config".
func loadLocalModelsConfig() localModelsConfig {
	data, err := os.ReadFile(localModelsConfigPath())
	if err != nil {
		return localModelsConfig{}
	}
	return parseLocalModelsConfig(data)
}

func parseLocalModelsConfig(data []byte) localModelsConfig {
	var doc struct {
		Models struct {
			Providers struct {
				Local *localModelsConfig `yaml:"local"`
			} `yaml:"providers"`
		} `yaml:"models"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil || doc.Models.Providers.Local == nil {
		return localModelsConfig{}
	}
	return *doc.Models.Providers.Local
}

// localEndpointTarget is what the relay needs beyond the published inventory.
type localEndpointTarget struct {
	root   string // base URL without trailing /v1 or slash
	apiKey string
	kind   string
}

// localInventorySnapshot is the daemon's current view: the published proto
// plus the private data used to relay.
type localInventorySnapshot struct {
	inventory *reliantv1.LocalModelInventory
	targets   map[string]localEndpointTarget
}

type localModelProber struct {
	client    *http.Client
	wellKnown []wellKnownLocalEndpoint
	loadCfg   func() localModelsConfig

	showCache *localprobe.ShowCache // /api/show by name|digest
	vramFn    func() int64          // total GPU-addressable memory; 0 = unknown
}

func newLocalModelProber() *localModelProber {
	return &localModelProber{
		client:    &http.Client{},
		wellKnown: defaultWellKnownLocalEndpoints(),
		loadCfg:   loadLocalModelsConfig,
		showCache: localprobe.NewShowCache(),
		vramFn:    estimateLocalVRAMBytes,
	}
}

// probe builds a complete snapshot: well-known ports (kept only if they
// answer) plus configured endpoints (kept even when down, with error set).
func (p *localModelProber) probe(ctx context.Context) *localInventorySnapshot {
	cfg := p.loadCfg()

	type job struct {
		id         string
		root       string
		source     string
		apiKey     string
		configured bool
	}
	var jobs []job
	seen := map[string]bool{}
	for _, e := range cfg.endpoints() {
		root, err := localprobe.NormalizeRoot(e.BaseURL)
		if err != nil {
			jobs = append(jobs, job{id: localprobe.EndpointID(strings.TrimSpace(e.BaseURL)), root: strings.TrimSpace(e.BaseURL), source: "configured", configured: true})
			continue
		}
		if seen[root] {
			continue
		}
		seen[root] = true
		jobs = append(jobs, job{id: localprobe.EndpointID(root), root: root, source: "configured", apiKey: e.APIKey, configured: true})
	}
	for _, w := range p.wellKnown {
		if seen[w.root] {
			continue
		}
		seen[w.root] = true
		jobs = append(jobs, job{id: w.id, root: w.root, source: "detected"})
	}

	results := make([]*reliantv1.LocalModelEndpoint, len(jobs))
	targets := make([]localEndpointTarget, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			to := localModelDetectTimeout
			if j.configured {
				to = localModelConfiguredTO
			}
			ep, tgt := p.probeEndpoint(ctx, j.id, j.root, j.source, j.apiKey, to)
			if ep == nil {
				return
			}
			results[i], targets[i] = ep, tgt
		}()
	}
	wg.Wait()

	snap := &localInventorySnapshot{
		inventory: &reliantv1.LocalModelInventory{ProbedAt: time.Now().UTC().Format(time.RFC3339)},
		targets:   map[string]localEndpointTarget{},
	}
	for i, ep := range results {
		if ep == nil {
			continue
		}
		snap.inventory.Endpoints = append(snap.inventory.Endpoints, ep)
		snap.targets[ep.Id] = targets[i]
	}
	sortLocalInventory(snap.inventory)
	return snap
}

func sortLocalInventory(inv *reliantv1.LocalModelInventory) {
	sort.Slice(inv.Endpoints, func(i, j int) bool { return inv.Endpoints[i].Id < inv.Endpoints[j].Id })
	for _, ep := range inv.Endpoints {
		sort.Slice(ep.Models, func(i, j int) bool { return ep.Models[i].Name < ep.Models[j].Name })
	}
}

// probeEndpoint returns nil for a detected endpoint that did not answer.
//
// The probing itself (model list, server-kind detection, per-family metadata,
// the honored-context rules) is internal/localprobe, shared with the server's
// DIRECT endpoint probe so the two cannot drift. What stays here is what only a
// daemon can know: this machine's VRAM (Ollama's default context depends on
// it) and which endpoints are well-known ports.
func (p *localModelProber) probeEndpoint(ctx context.Context, id, root, source, apiKey string, timeout time.Duration) (*reliantv1.LocalModelEndpoint, localEndpointTarget) {
	tgt := localEndpointTarget{root: root, apiKey: apiKey, kind: "openai_compatible"}
	ep := localprobe.Probe(ctx, p.client, id, localprobe.Target{Root: root, APIKey: apiKey}, localprobe.Options{
		ListTimeout:    timeout,
		DetectTimeout:  localModelDetectTimeout,
		VRAMBytes:      p.vramFn,
		ShowCache:      p.showCache,
		UnknownContext: localModelUnknownCtx,
		OnContext: func(model string, context int64, rule string) {
			logging.Debug(logPrefix+" Local model context", "endpoint", id, "model", model, "context", context, "rule", rule)
		},
	})
	ep.Source = source
	tgt.kind = ep.Kind
	if ep.Error != "" && source == "detected" {
		return nil, tgt
	}
	return ep, tgt
}

// estimateLocalVRAMBytes estimates memory Ollama can use for GPU work: an
// estimate, since the daemon cannot ask the server. macOS (Apple Silicon):
// the Metal working-set limit (~2/3 of unified memory up to 36 GiB, ~3/4
// above). Linux: summed nvidia-smi totals. Otherwise 0 (unknown).
func estimateLocalVRAMBytes() int64 {
	switch runtime.GOOS {
	case "darwin":
		if runtime.GOARCH != "arm64" {
			return 0
		}
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return 0
		}
		mem, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		if err != nil || mem <= 0 {
			return 0
		}
		return metalWorkingSetBytes(mem)
	case "linux":
		out, err := exec.Command("nvidia-smi", "--query-gpu=memory.total", "--format=csv,noheader,nounits").Output()
		if err != nil {
			return 0
		}
		return parseNvidiaSMIMiB(string(out)) << 20
	}
	return 0
}

func metalWorkingSetBytes(memsize int64) int64 {
	const gib = int64(1) << 30
	if memsize <= 36*gib {
		return memsize * 2 / 3
	}
	return memsize * 3 / 4
}

func parseNvidiaSMIMiB(out string) int64 {
	var total int64
	for _, line := range strings.Split(out, "\n") {
		if n, err := strconv.ParseInt(strings.TrimSpace(line), 10, 64); err == nil {
			total += n
		}
	}
	return total
}
