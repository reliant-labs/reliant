// Copyright (c) 2025 Reliant Labs
//
//forge:exclude-contract: a tools-daemon command handler; shaped by the daemon command dispatch table
package daemonruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/localprobe"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"gopkg.in/yaml.v3"
)

const maxConfiguredLocalEndpoints = 16

// activeLocalModels is the manager of the running daemon session, so the
// process-global command handler can trigger a re-probe.
var activeLocalModels atomic.Pointer[localModelManager]

func init() {
	RegisterCommand(toolexec.CommandLocalModelsSetEndpoints, handleLocalModelsSetEndpoints)
}

type setLocalEndpointsRequest struct {
	BaseURLs []string `json:"base_urls"`
}

func handleLocalModelsSetEndpoints(_ context.Context, payload []byte) ([]byte, error) {
	var req setLocalEndpointsRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	urls, err := validateLocalEndpointURLs(req.BaseURLs)
	if err != nil {
		return nil, err
	}
	if err := writeLocalEndpointsConfig(localModelsConfigPath(), urls); err != nil {
		return nil, fmt.Errorf("saving local model endpoints: %w", err)
	}
	// The prober re-reads the config file on every probe, so the file is the
	// in-memory list; just wake the publisher.
	if m := activeLocalModels.Load(); m != nil {
		m.requestRefresh()
	}
	return []byte(`{"ok":true}`), nil
}

func localModelsConfigPath() string {
	return filepath.Join(config.GetUserConfigDir(), config.ConfigFileName+".yaml")
}

// validateLocalEndpointURLs checks every URL, trims trailing slashes and
// dedupes (by normalized root, so ".../v1" and "..." collapse). The whole
// request is rejected if any entry is invalid.
func validateLocalEndpointURLs(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		s := strings.TrimSpace(raw)
		u, err := url.Parse(s)
		switch {
		case s == "" || err != nil:
			return nil, fmt.Errorf("invalid base URL %q", raw)
		case u.Scheme != "http" && u.Scheme != "https":
			return nil, fmt.Errorf("base URL %q must use http or https", raw)
		case u.Host == "" || u.Hostname() == "":
			return nil, fmt.Errorf("base URL %q has no host", raw)
		case u.User != nil:
			return nil, fmt.Errorf("base URL %q must not contain credentials", raw)
		}
		s = strings.TrimRight(s, "/")
		key, err := localprobe.NormalizeRoot(s)
		if err != nil {
			return nil, err
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	if len(out) > maxConfiguredLocalEndpoints {
		return nil, fmt.Errorf("too many endpoints: %d (max %d)", len(out), maxConfiguredLocalEndpoints)
	}
	return out, nil
}

func yamlMapGet(m *yaml.Node, key string) (k, v *yaml.Node, idx int) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1], i
		}
	}
	return nil, nil, -1
}

// yamlMapChild returns the mapping under key, creating it when absent. A key
// that exists but is null or a non-mapping is replaced with a mapping.
func yamlMapChild(parent *yaml.Node, key string) *yaml.Node {
	_, v, _ := yamlMapGet(parent, key)
	if v != nil && v.Kind == yaml.MappingNode {
		return v
	}
	child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if v != nil {
		*v = *child
		return v
	}
	parent.Content = append(parent.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, child)
	return child
}

func yamlMapDelete(m *yaml.Node, key string) {
	if _, _, i := yamlMapGet(m, key); i >= 0 {
		m.Content = append(m.Content[:i], m.Content[i+2:]...)
	}
}

// writeLocalEndpointsConfig sets models.providers.local.base_urls to urls in
// the YAML file at path, via a yaml.Node round-trip so every other key and
// comment survives. The legacy single base_url is removed; it stays in the
// list only if urls names it (urls is the complete desired set). Entries that
// keep their URL keep their per-endpoint api_key.
func writeLocalEndpointsConfig(path string, urls []string) error {
	mode := os.FileMode(0o600)
	var doc yaml.Node
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if st, serr := os.Stat(path); serr == nil {
			mode = st.Mode().Perm()
		}
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
	case os.IsNotExist(err):
	default:
		return err
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: top level is not a mapping", path)
	}

	models := yamlMapChild(root, "models")
	providers := yamlMapChild(models, "providers")
	local := yamlMapChild(providers, "local")

	// Existing entries that carry their own settings, keyed by normalized URL.
	existing := map[string]*yaml.Node{}
	if _, seq, _ := yamlMapGet(local, "base_urls"); seq != nil && seq.Kind == yaml.SequenceNode {
		for _, item := range seq.Content {
			raw := item.Value
			if item.Kind == yaml.MappingNode {
				if _, v, _ := yamlMapGet(item, "base_url"); v != nil {
					raw = v.Value
				}
			}
			if key, err := localprobe.NormalizeRoot(raw); err == nil {
				existing[key] = item
			}
		}
	}

	yamlMapDelete(local, "base_url")
	yamlMapDelete(local, "base_urls")
	if len(urls) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, u := range urls {
			key, _ := localprobe.NormalizeRoot(u)
			if prev := existing[key]; prev != nil && prev.Kind == yaml.MappingNode {
				if _, v, _ := yamlMapGet(prev, "base_url"); v != nil {
					v.Value = u
				}
				seq.Content = append(seq.Content, prev)
				continue
			}
			seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: u})
		}
		local.Content = append(local.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "base_urls"}, seq)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return writeFileAtomic(path, buf.Bytes(), mode)
}

// writeFileAtomic writes via a temp file in the same directory, then renames.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
