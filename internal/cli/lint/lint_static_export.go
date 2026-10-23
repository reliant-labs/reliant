// File: internal/cli/lint/lint_static_export.go
//
// static-export — `forge lint --static-export`.
//
// A Next.js frontend served by a static runtime (forge.OnHosted for a
// frontend, forge.OnBucket, forge.OnFirebase) must build to a static export,
// and `next dev` happily serves every feature an export cannot: a `[id]`
// route, a server action, cookies(), a rewrite. This lane reports each one
// with its file:line and fix (the rules and their limits are in
// internal/linter/staticexport).
//
// ── Which frontends it judges ─────────────────────────────────────────────
//
// Every Next.js frontend whose EFFECTIVE output is static:
//
//   - its next.config is a static export, whether or not an env ships it
//     yet; or
//   - ANY env binds it to a static runtime. The bindings come from each
//     env's real render, not a text scan: the scaffold declares binder
//     helpers (`_hosted_frontend`, `_on_bucket`) in every env file whether
//     or not they are used, so a mention proves nothing. The render here
//     does NOT bind `frontend_outputs`, so the render-time refusal of a
//     server build on a static runtime (kcl/render.k) does not fire and the
//     binding stays visible to judge. An env that fails to render for some
//     other reason is skipped: `forge env render <env>` owns render errors.
//
// A frontend forge.yaml does not declare (a cross-repo `source` pin) has no
// code in this tree, and is not judged.
//
// ── Relationship to the real build ────────────────────────────────────────
//
// `next build` with output: export is authoritative, and forge already runs
// it for every static frontend: CI's `NODE_ENV=production npm run build`,
// `forge build` for a hosted site, `forge env deploy` for a bucket. This lane
// is the offline early warning — the same failures before a CI round trip,
// each with a file:line — plus what the build cannot report: a frontend
// whose build is not an export at all (it builds fine as a server), and the
// constructs the export accepts and then drops (warnings).
//
// GATES on error findings — each is a build the export refuses, or a build
// with nothing to publish. Warnings never gate. Suppress one finding with
// `// forge:lint-disable-next-line <rule>: <reason>`.

package lint

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/internal/linter/staticexport"
	"github.com/reliant-labs/forge/internal/linter/suppress"
)

// staticBindingsFunc reports, per frontend name, the envs that bind it to a
// static runtime. A seam so tests need not render KCL.
type staticBindingsFunc func(ctx context.Context, projectDir string, cfg *config.ProjectConfig) map[string][]staticexport.Binding

// staticExportTargets selects the frontends this lane judges.
func staticExportTargets(ctx context.Context, projectDir string, cfg *config.ProjectConfig, bindings staticBindingsFunc) []staticexport.Frontend {
	if cfg == nil {
		return nil
	}
	var nextjs []config.FrontendConfig
	for _, fe := range cfg.Frontends {
		if fe.IsNextJS() {
			nextjs = append(nextjs, fe)
		}
	}
	if len(nextjs) == 0 {
		return nil
	}
	bound := bindings(ctx, projectDir, cfg)
	var out []staticexport.Frontend
	for _, fe := range nextjs {
		bs := bound[fe.Name]
		if !fe.StaticExport() && len(bs) == 0 {
			continue
		}
		rel, ok := fe.Dir(projectDir)
		if !ok {
			continue
		}
		out = append(out, staticexport.Frontend{
			Name:     fe.Name,
			Dir:      filepath.Join(projectDir, filepath.FromSlash(rel)),
			RelDir:   rel,
			Bindings: bs,
		})
	}
	return out
}

// renderStaticBindings renders every env under the deploy tree and collects
// its static-runtime frontend bindings.
func renderStaticBindings(_ context.Context, projectDir string, cfg *config.ProjectConfig) map[string][]staticexport.Binding {
	kclDir := filepath.Join(projectDir, deployKCLDirFor(cfg))
	entries, err := os.ReadDir(kclDir)
	if err != nil {
		return nil
	}
	out := map[string][]staticexport.Binding{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		envDir := filepath.Join(kclDir, e.Name())
		if _, err := os.Stat(filepath.Join(envDir, "main.k")); err != nil {
			continue
		}
		raw, err := kclrender.Run(projectDir, envDir, []string{"env=" + strconv.Quote(e.Name())})
		if err != nil {
			continue
		}
		for _, f := range renderedFrontendRuntimes(raw) {
			if staticexport.IsStaticRuntime(f.runtime) {
				out[f.name] = append(out[f.name], staticexport.Binding{Env: e.Name(), Runtime: f.runtime})
			}
		}
	}
	return out
}

type renderedFrontend struct{ name, runtime string }

// renderedFrontendRuntimes reads output.frontends[].{name, runtime.type} out
// of a render's JSON — the two fields this lane needs, without the cli
// package's full entity model (which this package must not import).
func renderedFrontendRuntimes(raw []byte) []renderedFrontend {
	var doc struct {
		Output struct {
			Frontends []struct {
				Name    string `json:"name"`
				Runtime struct {
					Type string `json:"type"`
				} `json:"runtime"`
			} `json:"frontends"`
		} `json:"output"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	out := make([]renderedFrontend, 0, len(doc.Output.Frontends))
	for _, f := range doc.Output.Frontends {
		out = append(out, renderedFrontend{name: f.Name, runtime: f.Runtime.Type})
	}
	return out
}

// staticExportFindings runs the analysis over every target, with the shared
// suppression directives applied.
func staticExportFindings(ctx context.Context, projectDir string, cfg *config.ProjectConfig, bindings staticBindingsFunc) ([]finding.Finding, int, error) {
	targets := staticExportTargets(ctx, projectDir, cfg, bindings)
	var all []finding.Finding
	for _, fe := range targets {
		fs, err := staticexport.Check(fe)
		if err != nil {
			return nil, 0, fmt.Errorf("static-export lint: frontend %s: %w", fe.Name, err)
		}
		all = append(all, fs...)
	}
	res := suppress.ApplyFiles(projectDir, all, os.ReadFile)
	kept := make([]finding.Finding, 0, len(res.Kept)+len(res.Violations))
	kept = append(kept, res.Kept...)
	kept = append(kept, res.Violations...)
	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].File != kept[j].File {
			return kept[i].File < kept[j].File
		}
		return kept[i].Line < kept[j].Line
	})
	return kept, len(targets), nil
}

// collectStaticExportJSON is the JSON arm. Error findings gate.
func collectStaticExportJSON(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
	fs, judged, err := staticExportFindings(rc.ctx, rc.cwd, rc.cfg, renderStaticBindings)
	if err != nil {
		return nil, false, err
	}
	out := findingsToJSON(fs)
	switch {
	case judged == 0:
		out = append(out, ranNote("no Next.js frontend is a static export or bound to a static runtime — nothing to check"))
	case len(out) == 0:
		out = append(out, ranNote(fmt.Sprintf("static-export clean — %d static frontend(s) use nothing a static export cannot serve", judged)))
	}
	return out, anyErrorFinding(out), nil
}

// hasNextJSFrontend reports whether the lane can apply at all.
func hasNextJSFrontend(cfg *config.ProjectConfig) bool {
	if cfg == nil {
		return false
	}
	for _, fe := range cfg.Frontends {
		if fe.IsNextJS() {
			return true
		}
	}
	return false
}
