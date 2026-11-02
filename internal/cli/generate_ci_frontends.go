// Frontend discovery for the CI workflow generator.
//
// CI workflows need one fact about frontends: which directories in THIS
// repository hold a Node app, so the workflows can point setup-node at a
// package.json and run npm there.
//
// That fact used to be read from forge.yaml's `frontends:` key. The key is
// retired — frontend topology moved to deploy/kcl/<env>/main.k, and
// `forge generate` strips a leftover block rather than honouring it — so the
// read silently returned an empty list on every real project. Empty meant
// HasFrontends=false and FrontendPath="", which the templates render as "no
// frontend jobs at all". Because the CI workflows are write-once scaffolds,
// nothing overwrote the stale blocks a previous forge had emitted while the
// key still existed, and the workflows froze pointing at whatever the
// frontend was called then. A rename (control-plane's admin-web →
// internal-console) left `frontends/admin-web` in e2e.yml, e2e CI broke, and
// no amount of regenerating could fix it: forge was reading a key that is
// always absent. The hand-edit that unbroke CI then failed the Tier-1
// self-certification guard forever.
//
// So discovery reads the KCL, the same source `forge build` and
// `forge env deploy` resolve a --target against.
package cli

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/internal/config"
)

// discoverCIFrontends returns the frontends CI should drive a Node
// toolchain for, unioned over every env declared under deploy/kcl/.
//
// The union is what CI needs: workflows are not per-env (one ci.yml lints
// every frontend in the repo), and a frontend is often declared in only
// some envs — control-plane's internal-console is absent from `e2e`, which
// declares `frontends = []`. Taking any single env would drop it.
//
// Two filters apply, both because a GitHub Actions job checks out THIS
// repository and nothing else:
//
//   - a frontend whose path escapes the repo root is skipped. control-plane
//     declares reliant-web at `../reliant/web`, a sibling checkout that does
//     not exist in CI; emitting `cd ../reliant/web && npm ci` would fail
//     every run.
//   - a frontend declared as a cross-repo `source:` pin has no directory
//     here at all until the source resolver materializes one, so there is no
//     path for setup-node to read.
//
// Results are sorted by name so regenerating twice produces identical bytes.
//
// projectDir is the directory holding forge.yaml. When it declares no envs,
// KCL cannot be rendered, or every env renders no usable frontend, the
// result falls back to cfg.Frontends — which is empty on a current project
// but non-empty on one that predates the key's retirement, and correct for
// the freshly-scaffolded project whose deploy/kcl tree has no frontends yet.
//
// Memoized per project directory: five workflow builders ask the same
// question in one `forge generate`, and each answer costs a KCL render of
// every env. The cache is process-scoped, which is the right lifetime — a
// generate run must render one consistent topology, and the process exits
// before the KCL could change under it.
func discoverCIFrontends(projectDir string, cfg *config.ProjectConfig) []config.FrontendConfig {
	ciFrontendsMu.Lock()
	defer ciFrontendsMu.Unlock()
	if cached, ok := ciFrontendsCache[projectDir]; ok {
		return cached
	}
	out := discoverCIFrontendsUncached(projectDir, cfg)
	ciFrontendsCache[projectDir] = out
	return out
}

var (
	ciFrontendsMu    sync.Mutex
	ciFrontendsCache = map[string][]config.FrontendConfig{}
)

func discoverCIFrontendsUncached(projectDir string, cfg *config.ProjectConfig) []config.FrontendConfig {
	found := map[string]config.FrontendConfig{}

	envs, _ := ListEnvs(projectDir)
	for _, env := range envs {
		// Pure render: this walks EVERY env to collect the frontend set, so a
		// file.write anywhere in any env's module would otherwise fire — and
		// dirty a tracked file — purely because generate enumerated them.
		entities, restored, err := renderKCLPure(context.Background(), projectDir, env)
		reportImpureRender(env, restored)
		if err != nil || entities == nil {
			// A single env that fails to render (a KCL compile error being
			// actively worked on, a toolchain gap) must not blank the CI
			// workflows of a repo whose other envs render fine.
			continue
		}
		for _, fe := range entities.Frontends {
			if fe.Name == "" || fe.Source != nil {
				continue
			}
			path := repoRelativeFrontendPath(fe.Path, fe.Name)
			if path == "" {
				continue
			}
			// First env wins. Envs are walked in ListEnvs' sorted order, so
			// which one that is stays stable across runs.
			if _, seen := found[fe.Name]; !seen {
				found[fe.Name] = config.FrontendConfig{Name: fe.Name, Type: fe.Type}.WithDir(path)
			}
		}
	}

	if len(found) == 0 {
		return cfg.Frontends
	}
	out := make([]config.FrontendConfig, 0, len(found))
	for _, fe := range found {
		out = append(out, fe)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// repoRelativeFrontendPath normalizes a KCL frontend path to a
// repo-relative slash path, or returns "" when the frontend has no usable
// directory in this repository.
//
// An empty path defaults to `frontends/<name>`, matching the same fallback
// the config loader applies to an in-repo frontend.
func repoRelativeFrontendPath(declared, name string) string {
	p := strings.TrimSpace(declared)
	if p == "" {
		return "frontends/" + name
	}
	p = filepath.ToSlash(filepath.Clean(filepath.FromSlash(p)))
	// Absolute paths and any path that climbs above the repo root are
	// machine-specific or sibling-repo locations. CI has neither.
	if filepath.IsAbs(p) || p == ".." || strings.HasPrefix(p, "../") {
		return ""
	}
	return p
}
