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
// Every Vite frontend that ANY env binds to a static runtime is checked too
// (internal/linter/staticexport/vite.go): a Vite build is static by
// construction, so what breaks it on a static host is the seam with the
// deploy — build.outDir against the env's public_dir, and base against the
// env's base_path. Those bindings carry each env's rendered public_dir and
// base_path.
//
// Every other frontend — a Next.js server nobody ships statically, an unbound
// Vite app, an Expo app, a frontend whose code is elsewhere — gets an
// explicit "<name> (<type>): not applicable — <reason>" note, so a run never
// reads as "checked, fine" for a frontend it did not look at.
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
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/internal/linter/staticexport"
	"github.com/reliant-labs/forge/internal/linter/suppress"
)

// staticBindingsFunc reports, per frontend name, the envs that bind it to a
// static runtime. A seam so tests need not render KCL.
type staticBindingsFunc func(ctx context.Context, projectDir string, cfg *config.ProjectConfig) map[string][]staticexport.Binding

// staticExportPlan is what the lane does with each frontend in the
// inventory: judge it as a Next.js export, check it as a Vite build, or say
// why neither applies. Every frontend lands in exactly one of the three, so
// a run never reads as "checked, fine" for a frontend it did not look at.
type staticExportPlan struct {
	next []staticexport.Frontend
	vite []staticexport.Frontend
	// skipped is one "<name> (<type>): not applicable — <reason>" per
	// frontend the lane does not check.
	skipped []string
	// labels is each judged frontend's "<name> (<type>)", for notes.
	labels map[string]string
}

// notBoundReason is why a frontend no env publishes statically is skipped.
const notBoundReason = "no env binds it to a static runtime (forge.OnHosted / OnBucket / OnFirebase)"

// planStaticExport sorts the inventory. Bindings are rendered only when a
// frontend the lane can check exists.
func planStaticExport(ctx context.Context, projectDir string, cfg *config.ProjectConfig, bindings staticBindingsFunc) staticExportPlan {
	plan := staticExportPlan{labels: map[string]string{}}
	if cfg == nil {
		return plan
	}
	var bound map[string][]staticexport.Binding
	for _, fe := range cfg.Frontends {
		if _, ok := fe.Dir(projectDir); ok && (fe.IsNextJS() || isViteFrontend(fe)) {
			bound = bindings(ctx, projectDir, cfg)
			break
		}
	}
	for _, fe := range cfg.Frontends {
		label := fmt.Sprintf("%s (%s)", fe.Name, frontendTypeLabel(fe))
		skip := func(reason string) {
			plan.skipped = append(plan.skipped, fmt.Sprintf("%s: not applicable — %s", label, reason))
		}
		rel, ok := fe.Dir(projectDir)
		if !ok {
			skip("its code is not in this tree")
			continue
		}
		bs := bound[fe.Name]
		target := staticexport.Frontend{
			Name:     fe.Name,
			Dir:      filepath.Join(projectDir, filepath.FromSlash(rel)),
			RelDir:   rel,
			Bindings: bs,
		}
		switch {
		case fe.IsNextJS():
			if !fe.StaticExport() && len(bs) == 0 {
				skip(fmt.Sprintf("its production build is a Next.js %s server and %s", fe.EffectiveOutput(), notBoundReason))
				continue
			}
			plan.next = append(plan.next, target)
		case isViteFrontend(fe):
			if len(bs) == 0 {
				skip(notBoundReason + ", so nothing publishes its build")
				continue
			}
			plan.vite = append(plan.vite, target)
		default:
			skip(fmt.Sprintf("the lane checks Next.js and Vite builds; a %s frontend's static build is not checked", frontendTypeLabel(fe)))
			continue
		}
		plan.labels[fe.Name] = label
	}
	return plan
}

// staticExportTargets is the Next.js frontends this lane judges.
func staticExportTargets(ctx context.Context, projectDir string, cfg *config.ProjectConfig, bindings staticBindingsFunc) []staticexport.Frontend {
	return planStaticExport(ctx, projectDir, cfg, bindings).next
}

// isViteFrontend reports a Vite frontend (inventory "vite-spa", KCL "vite").
func isViteFrontend(fe config.FrontendConfig) bool {
	switch strings.ToLower(strings.TrimSpace(fe.Type)) {
	case "vite-spa", "vite":
		return true
	}
	return false
}

// frontendTypeLabel is the frontend's type as the inventory spells it.
func frontendTypeLabel(fe config.FrontendConfig) string {
	if t := strings.TrimSpace(fe.Type); t != "" {
		return t
	}
	return "nextjs"
}

// renderStaticBindings renders every env under the deploy tree and collects
// its static-runtime frontend bindings.
func renderStaticBindings(ctx context.Context, projectDir string, cfg *config.ProjectConfig) map[string][]staticexport.Binding {
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
		raw, err := lintKCLRun(ctx, projectDir, envDir, []string{"env=" + strconv.Quote(e.Name())})
		if err != nil {
			continue
		}
		for _, f := range renderedFrontendRuntimes(raw) {
			if staticexport.IsStaticRuntime(f.runtime) {
				out[f.name] = append(out[f.name], staticexport.Binding{Env: e.Name(), Runtime: f.runtime, PublicDir: f.publicDir, BasePath: f.basePath})
			}
		}
	}
	return out
}

type renderedFrontend struct{ name, runtime, publicDir, basePath string }

// renderedFrontendRuntimes reads output.frontends[].{name, runtime.type,
// public_dir, base_path} out of a render's JSON — the fields this lane needs,
// without the cli package's full entity model (which this package must not
// import).
func renderedFrontendRuntimes(raw []byte) []renderedFrontend {
	var doc struct {
		Output struct {
			Frontends []struct {
				Name    string `json:"name"`
				Runtime struct {
					Type string `json:"type"`
				} `json:"runtime"`
				PublicDir string `json:"public_dir"`
				BasePath  string `json:"base_path"`
			} `json:"frontends"`
		} `json:"output"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	out := make([]renderedFrontend, 0, len(doc.Output.Frontends))
	for _, f := range doc.Output.Frontends {
		out = append(out, renderedFrontend{name: f.Name, runtime: f.Runtime.Type, publicDir: f.PublicDir, basePath: f.BasePath})
	}
	return out
}

// staticExportResult is one run of the lane.
type staticExportResult struct {
	findings []finding.Finding
	// notes say what was checked, or why a frontend was not.
	notes []string
	// judgedNext is how many Next.js frontends were judged.
	judgedNext int
	// frontends is how many frontends the inventory holds.
	frontends int
}

// staticExportFindings runs the analysis over every target, with the shared
// suppression directives applied.
func staticExportFindings(ctx context.Context, projectDir string, cfg *config.ProjectConfig, bindings staticBindingsFunc) (staticExportResult, error) {
	plan := planStaticExport(ctx, projectDir, cfg, bindings)
	res := staticExportResult{judgedNext: len(plan.next)}
	if cfg != nil {
		res.frontends = len(cfg.Frontends)
	}
	var all []finding.Finding
	for _, fe := range plan.next {
		fs, err := staticexport.Check(fe)
		if err != nil {
			return staticExportResult{}, fmt.Errorf("static-export lint: frontend %s: %w", fe.Name, err)
		}
		all = append(all, fs...)
	}
	for _, fe := range plan.vite {
		vr, err := staticexport.CheckVite(fe)
		if err != nil {
			return staticExportResult{}, fmt.Errorf("static-export lint: frontend %s: %w", fe.Name, err)
		}
		all = append(all, vr.Findings...)
		for _, n := range vr.Notes {
			res.notes = append(res.notes, plan.labels[fe.Name]+": "+n)
		}
	}
	res.notes = append(res.notes, plan.skipped...)
	sup := suppress.ApplyFiles(projectDir, all, os.ReadFile)
	kept := make([]finding.Finding, 0, len(sup.Kept)+len(sup.Violations))
	kept = append(kept, sup.Kept...)
	kept = append(kept, sup.Violations...)
	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].File != kept[j].File {
			return kept[i].File < kept[j].File
		}
		return kept[i].Line < kept[j].Line
	})
	res.findings = kept
	return res, nil
}

// collectStaticExportJSON is the JSON arm. Error findings gate.
func collectStaticExportJSON(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
	res, err := staticExportFindings(rc.ctx, rc.cwd, rc.cfg, renderStaticBindings)
	if err != nil {
		return nil, false, err
	}
	out := staticExportReport(res)
	return out, anyErrorFinding(out), nil
}

// staticExportReport is a run's findings plus its notes: a summary for a
// clean Next.js run, then one line per frontend checked or skipped.
func staticExportReport(res staticExportResult) []lintJSONFinding {
	out := findingsToJSON(res.findings)
	clean := len(out) == 0
	switch {
	case res.frontends == 0:
		out = append(out, ranNote("this project has no frontend — nothing to check"))
	case clean && res.judgedNext > 0:
		out = append(out, ranNote(fmt.Sprintf("static-export clean — %d Next.js frontend(s) use nothing a static export cannot serve", res.judgedNext)))
	}
	for _, n := range res.notes {
		out = append(out, ranNote(n))
	}
	return out
}

// hasFrontend reports whether the lane has anything to report on: every
// frontend gets a verdict or a not-applicable line.
func hasFrontend(cfg *config.ProjectConfig) bool {
	return cfg != nil && len(cfg.Frontends) > 0
}
