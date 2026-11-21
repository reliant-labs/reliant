// Package cli — standalone k3d registry lifecycle for declared clusters.
//
// A k3d "Simple" config can reference a registry in two ways:
//
//   - registries.create  — k3d creates a registry container OWNED BY the
//     cluster. `k3d cluster delete` destroys it. Every cold recreate then
//     rebuilds + re-pushes every image (the fat ones especially).
//   - registries.use      — k3d references an EXISTING registry container.
//     A standalone registry (`k3d registry create`) is owned by NO cluster,
//     so it survives `cluster delete` and its pushed images persist across
//     cold recreates.
//
// This file makes the persistent path zero-glue: when a declared cluster's
// k3d config references a registry via `registries.use`, forge ensures that
// standalone registry exists BEFORE `k3d cluster create` runs. Idempotent —
// an already-present registry is a no-op; an absent one is created with the
// declared host port so host pushes keep resolving to the same `localhost:
// <port>` ref.
package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// k3dRegistryRef is a standalone registry a cluster's k3d config references
// via `registries.use`. Name is the k3d registry name (which equals the
// container name); HostPort is the host-side port to bind so host pushes to
// `localhost:<HostPort>` reach it. HostPort is 0 when the config doesn't
// pin one — forge then lets k3d pick a free port (rare; the host ref would
// be non-deterministic, so projects that bake `localhost:<port>` image refs
// always pin it).
type k3dRegistryRef struct {
	Name     string
	HostPort int
}

// registryExistsFn / registryCreateFn are indirection seams so the
// ensure-registry decision logic is unit-testable without shelling out to
// k3d. Production wires them to the real `k3d registry` subcommands.
var (
	registryExistsFn = k3dRegistryExists
	registryCreateFn = k3dRegistryCreate
)

// ensureConfigRegistries parses the cluster's k3d config for
// `registries.use` references and ensures each named standalone registry
// exists (create-if-absent, no-op if present). A cluster whose config uses
// `registries.create` (cluster-owned registry) or declares no registry is a
// no-op here — k3d handles those itself. configPath is the on-disk k3d
// Simple-config (deploy/k3d.yaml); an empty/missing path is a no-op.
//
// This runs BEFORE `k3d cluster create`: a `registries.use` reference fails
// the create if the registry container isn't already up, so forge stands it
// up first. The standalone registry is owned by no cluster, so a later
// `cluster delete` leaves it (and its pushed images) intact.
func ensureConfigRegistries(ctx context.Context, configPath string) error {
	if configPath == "" {
		return nil
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read k3d config %s: %w", configPath, err)
	}
	refs, err := parseUseRegistries(data)
	if err != nil {
		return fmt.Errorf("parse registries from %s: %w", configPath, err)
	}
	for _, ref := range refs {
		if err := ensureStandaloneRegistry(ctx, ref); err != nil {
			return err
		}
	}
	return nil
}

// ensureStandaloneRegistry creates the standalone k3d registry if absent;
// no-op when it already exists. Idempotent so warm runs (and cold recreates
// against a surviving registry) cost a single `k3d registry list`.
func ensureStandaloneRegistry(ctx context.Context, ref k3dRegistryRef) error {
	exists, err := registryExistsFn(ctx, ref.Name)
	if err != nil {
		return err
	}
	if exists {
		fmt.Printf("  standalone registry %q already exists — no-op (persists across cluster delete)\n", ref.Name)
		return nil
	}
	fmt.Printf("  creating standalone registry %q (host port %d) — survives cluster delete so images persist...\n", ref.Name, ref.HostPort)
	return registryCreateFn(ctx, ref)
}

// parseUseRegistries pulls the `registries.use` entries out of a k3d
// Simple-config. Each entry is the `<name>:<internalPort>` form k3d uses
// (e.g. `k3d-control-plane-registry:5000`); we keep the name and discard the
// container-internal port. The host port forge must bind comes from the
// inline `registries.config` containerd mirror — the `localhost:<hostPort>`
// (or `<name-without-k3d->.localhost:<hostPort>`) mirror key that maps the
// host push name to the registry. Reading the host port from the config the
// project already authors keeps the registry spec single-sourced (no
// duplicate forge field that could drift from the mirror).
func parseUseRegistries(configYAML []byte) ([]k3dRegistryRef, error) {
	var doc struct {
		Registries struct {
			Use    []string `yaml:"use"`
			Config string   `yaml:"config"`
		} `yaml:"registries"`
	}
	if err := yaml.Unmarshal(configYAML, &doc); err != nil {
		return nil, err
	}
	if len(doc.Registries.Use) == 0 {
		return nil, nil
	}
	refs := make([]k3dRegistryRef, 0, len(doc.Registries.Use))
	for _, entry := range doc.Registries.Use {
		name := strings.TrimSpace(entry)
		// `use` entries are `<name>:<internalPort>`. Strip the internal port
		// (the registry's in-container port, always 5000 for k3d) to get the
		// registry name `k3d registry create` expects.
		if i := strings.LastIndex(name, ":"); i > 0 {
			name = name[:i]
		}
		if name == "" {
			continue
		}
		refs = append(refs, k3dRegistryRef{Name: name, HostPort: hostPortFromMirrorConfig(doc.Registries.Config, name)})
	}
	return refs, nil
}

// hostPortFromMirrorConfig extracts the host push port from the inline
// containerd mirror config: the port on a `<host>:<port>` mirror key. A
// config may carry mirrors for several registries (e.g. the declared `use`
// registry plus a hosted one), so the choice is made deterministically:
//
//  1. mirrors whose endpoint points at registryName (the declared registry)
//     beat mirrors that do not;
//  2. within a tier, a `localhost:<port>` key beats any other host;
//  3. remaining ties break on the sorted key.
//
// Returns 0 when no `<host>:<port>` mirror key is present (forge then lets
// k3d pick a port).
func hostPortFromMirrorConfig(mirrorYAML, registryName string) int {
	if strings.TrimSpace(mirrorYAML) == "" {
		return 0
	}
	var cfg struct {
		Mirrors map[string]struct {
			Endpoint []string `yaml:"endpoint"`
		} `yaml:"mirrors"`
	}
	if err := yaml.Unmarshal([]byte(mirrorYAML), &cfg); err != nil {
		return 0
	}
	keys := make([]string, 0, len(cfg.Mirrors))
	for key := range cfg.Mirrors {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	best, bestScore := 0, -1
	for _, key := range keys {
		i := strings.LastIndex(key, ":")
		if i <= 0 {
			continue
		}
		port, err := strconv.Atoi(key[i+1:])
		if err != nil {
			continue
		}
		score := 0
		if key[:i] == "localhost" {
			score++
		}
		if registryName != "" {
			for _, ep := range cfg.Mirrors[key].Endpoint {
				if strings.Contains(ep, "://"+registryName+":") || strings.HasSuffix(ep, "://"+registryName) {
					score += 2
					break
				}
			}
		}
		if score > bestScore {
			best, bestScore = port, score
		}
	}
	return best
}

// k3dRegistryExists reports whether a standalone k3d registry of the given
// name is listed by `k3d registry list`. The name column carries the k3d
// registry name (which equals the container name, `k3d registry create`
// prefixes `k3d-`).
func k3dRegistryExists(ctx context.Context, name string) (bool, error) {
	listCmd := exec.CommandContext(ctx, "k3d", "registry", "list", "--no-headers")
	scrubSubprocessLogEnv(listCmd)
	out, err := listCmd.Output()
	if err != nil {
		return false, fmt.Errorf("k3d registry list: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == name {
			return true, nil
		}
	}
	return false, nil
}

// k3dRegistryCreate creates a standalone registry. k3d prefixes the supplied
// name with `k3d-`, so we strip a leading `k3d-` from the desired final name
// before passing it (passing `k3d-foo` would yield `k3d-k3d-foo`). The
// registry binds 0.0.0.0:<HostPort> so host pushes to `localhost:<HostPort>`
// reach it; when HostPort is 0 we omit --port and k3d picks a free one.
// A new registry is created with --delete-enabled (k3d v5.9): distribution
// refuses DELETE by default, so without it retention cannot reclaim a manifest
// through the registry API and has to restart the container behind a
// delete-enabled config instead — a write outage during every GC pass. The
// flag only enables the API verb; nothing deletes without forge asking.
// Existing registries keep the restart path, since the flag is create-time only.
func k3dRegistryCreate(ctx context.Context, ref k3dRegistryRef) error {
	args := k3dRegistryCreateArgs(ref)
	cmd := exec.CommandContext(ctx, "k3d", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("k3d registry create %q: %w", ref.Name, err)
	}
	return nil
}

// k3dRegistryCreateArgs is the argv, split out so the flags are assertable
// without a k3d binary.
func k3dRegistryCreateArgs(ref k3dRegistryRef) []string {
	// k3d prefixes the supplied name with `k3d-`, so strip a leading `k3d-`
	// from the desired final name (passing `k3d-foo` would yield `k3d-k3d-foo`).
	args := []string{"registry", "create", strings.TrimPrefix(ref.Name, "k3d-"), "--delete-enabled"}
	if ref.HostPort > 0 {
		args = append(args, "--port", fmt.Sprintf("0.0.0.0:%d", ref.HostPort))
	}
	return args
}

// registryFactsFromConfig extracts the storage facts a cluster's k3d config
// carries about its registry: the container name from `registries.use`, and
// every host name the inline containerd mirror maps to it.
//
// The mirror keys ARE the alias set — they are exactly the names a host push
// or an in-cluster pull resolves through, which is what retention must match a
// repository's host against. Reading them from the config the project already
// authors keeps the alias list single-sourced; a separate forge field would
// drift from the mirror and silently make pushed images look unowned.
//
// Returns an empty name when the config declares no `registries.use` registry
// (a cluster-owned `registries.create`, or none at all) — forge converges
// nothing for a registry it does not own the lifecycle of.
func registryFactsFromConfig(configYAML []byte) (container string, aliases []string, err error) {
	refs, err := parseUseRegistries(configYAML)
	if err != nil || len(refs) == 0 {
		return "", nil, err
	}
	ref := refs[0]
	return ref.Name, append(mirrorHostAliases(configYAML), ref.Name+":5000"), nil
}

// mirrorHostAliases returns every `<host>:<port>` key in the config's inline
// containerd mirror block.
func mirrorHostAliases(configYAML []byte) []string {
	var doc struct {
		Registries struct {
			Config string `yaml:"config"`
		} `yaml:"registries"`
	}
	if err := yaml.Unmarshal(configYAML, &doc); err != nil {
		return nil
	}
	var cfg struct {
		Mirrors map[string]any `yaml:"mirrors"`
	}
	if err := yaml.Unmarshal([]byte(doc.Registries.Config), &cfg); err != nil {
		return nil
	}
	aliases := make([]string, 0, len(cfg.Mirrors))
	for key := range cfg.Mirrors {
		if i := strings.LastIndex(key, ":"); i > 0 {
			if _, convErr := strconv.Atoi(key[i+1:]); convErr == nil {
				aliases = append(aliases, key)
			}
		}
	}
	// Map iteration is unordered; sort so a converge is byte-identical run to
	// run and the idempotency property holds.
	sort.Strings(aliases)
	return aliases
}
