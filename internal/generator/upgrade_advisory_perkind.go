// Per-kind frontend membership for the scaffold-once advisory lane.
//
// The lane began by covering only the SHARED template roots, on the
// reasoning (still correct) that a shared root is forge's own declaration
// of "this file is generic mechanism I own the design of". That is a sound
// rule for deciding what forge should OWN. It is the wrong rule for
// deciding what forge should REPORT, and the difference is the bug this
// file fixes.
//
// The symptom: frontends/<name>/eslint.config.mjs is written from
// internal/templates/frontend/<kind>/eslint.config.mjs and then never
// mentioned again by any forge command. Hand-edit it and `forge project
// upgrade --check` is completely silent — while .golangci.yml, the same
// kind of lint config on the backend side, prints a diff and both remedies
// because it happens to be a Tier-2 managed file. Same class of file, same
// user gesture, opposite outcome, decided by which registry it landed in.
//
// The rule applied here is deliberately generic, and NOT about lint files:
//
//	if forge wrote it from a template, its drift is reportable.
//
// Registering eslint.config.mjs by name would have fixed the reported
// symptom and left the bug — the next per-kind template forge adds would be
// invisible in exactly the same way. So membership is DERIVED from the
// template tree, and what this file states instead is the far smaller set
// of exclusions, each of which is a file another lane already renders.
//
// ── Reporting a file is not claiming it ───────────────────────────────
//
// Nothing here changes ownership. These files stay scaffold-once: no
// forge:hash marker, no manifest entry, never written unless the user names
// the exact path after --force. The advisory lane reports and does not
// apply, which is what makes widening it safe — the cost of a new row is a
// diff the user can ignore, not a file forge starts overwriting.
//
// ── Why the exclusions are exclusions ─────────────────────────────────
//
// The lane's standing admission rule is that a row may exist only when the
// project has exactly ONE renderer for that path. That rule is why the
// .github WORKFLOWS are absent (two config→data mappings disagree about
// them on a project seconds old, so reporting the difference would report
// forge's own inconsistency as the user's staleness). The same rule
// excludes exactly three groups here, and nothing else:
//
//   - Tier-1 *_gen.* files (apiurl_gen.ts, basepath_gen.ts, otel_gen.ts).
//     Rewritten every `forge generate` and guarded by the drift probe. A
//     row would duplicate a check that already exists and is stricter.
//   - Inventory-seeded files (nav.tsx, dashboard.tsx, page.tsx). The nav
//     generator re-renders these from the DISCOVERED entity set
//     (generate_frontend_nav.go). This lane has the frontend config but not
//     the inventory, so its render would show empty routes against a real
//     project's populated ones — a large, permanent, entirely bogus diff.
//   - src/gen/ and the mock surface, written per-entity from a different
//     registry for the same reason.
//   - Files the MANAGED lane registers (upgrade_frontend_managed.go —
//     today, eslint.config.mjs). Two lanes, one file: `--check` listed it
//     twice, once as a managed Tier-2 entry and once as an advisory row.
//     Cosmetic in the report, but the two lanes disagree about who owns
//     the file, and they do not offer the same thing — managed refreshes
//     a pristine copy and offers `disown`, advisory only ever reports.
//     The managed lane wins because it deliberately claimed the file (see
//     that file's header: symmetry with .golangci.yml, and it is the only
//     lane through which a template improvement actually ARRIVES).
//
// Everything else in the per-kind tree renders from the frontend's own
// config, which this lane has, so it is admitted.
package generator

import (
	"path"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
)

// perKindAdvisoryExclusions are the per-kind destination paths (relative to
// the frontend root, .tmpl already stripped) that a SECOND renderer owns.
//
// Keyed by destination rather than by template name because that is the
// thing the exclusion is about: "some other lane writes this path". A
// template that moves between kinds keeps its exclusion; a new template at
// a new path is admitted, which is the direction that fails safe — a
// spurious row is a diff someone reads once, a missing row is a file that
// silently drifts forever.
var perKindAdvisoryExclusions = map[string]string{
	// The nav generator re-renders these from the discovered entity set,
	// refreshing them until the user first edits them
	// (emitScaffoldUntilTouched). This lane cannot see that inventory.
	"src/components/nav.tsx": "re-rendered from the discovered entity set by the nav generator",
	"src/app/dashboard.tsx":  "re-rendered from the discovered entity set by the nav generator",
	"src/app/page.tsx":       "seeded once by the nav generator, which owns the entity list it shows",

	// package.json is RECONCILED after the template render, by
	// EnsureWebRuntimeDependency: the @reliantlabs/forge-web-runtime specifier
	// is `^x.y.z` from a released forge and `file:<path>` from a dev build,
	// so the bytes on disk legitimately differ from a plain template
	// render. That is the two-renderer condition exactly — reporting it
	// would show every developer a diff describing forge's own build mode.
	"package.json": "reconciled after render by EnsureWebRuntimeDependency (published vs dev web-runtime specifier)",
}

// webTsconfigRel is the browser frontends' tsconfig, whose web-runtime peer
// pins are reconciled after the render — by the post-install pass
// (ReconcileFrontendTsconfigPeers) and by `forge generate`
// (reconcileFrontendTsconfigPeers). The template names package DIRECTORIES
// because nothing is installed when it renders; the reconcile aims each pin at
// the layout npm produced and at the package's installed DECLARATION FILE. So a
// plain render differs from every correctly-scaffolded tsconfig — the
// two-renderer condition — and adopting that render would write back the very
// directory pins that split the webpack bundle ("No QueryClient set").
//
// Web trees only: the React Native tsconfig carries no peer pins, has exactly
// one renderer, and stays reported.
const webTsconfigRel = "tsconfig.json"

// perKindAdvisoryEligible reports whether a per-kind template file should
// get an advisory row.
//
// tree is the template tree ("nextjs", "vite-spa", "react-native"); rel is
// the path within the composed tree, .tmpl suffix still attached.
func perKindAdvisoryEligible(tree, rel string) bool {
	dest := strings.TrimSuffix(rel, ".tmpl")

	if _, excluded := perKindAdvisoryExclusions[dest]; excluded {
		return false
	}
	if dest == webTsconfigRel && FrontendTypeHasMockSurface(tree) {
		return false
	}
	// Claimed by the managed lane, which tracks and refreshes it. Asked of
	// that registry rather than spelled out here, so a file moving into or
	// out of managed status cannot leave the two lanes disagreeing — the
	// duplicate-registration bug this whole lane was written to avoid.
	if dest == frontendManagedRel {
		return false
	}
	// Tier-1 generated files: the drift probe owns them, and it PROVES
	// drift from an embedded marker rather than inferring it from line
	// counts, so a row here would be the weaker of two overlapping checks.
	if isGeneratedFrontendFile(dest) {
		return false
	}
	// Generated protobuf-es output and the per-entity mock surface: written
	// per-entity from registries this lane does not consult.
	if hasPathPrefix(dest, "src/gen") || hasPathPrefix(dest, "src/mocks") {
		return false
	}
	return true
}

// isGeneratedFrontendFile reports whether a destination path is a Tier-1
// regenerated file, identified by forge's `_gen` filename convention.
func isGeneratedFrontendFile(dest string) bool {
	base := path.Base(dest)
	ext := path.Ext(base)
	return strings.HasSuffix(strings.TrimSuffix(base, ext), "_gen")
}

// hasPathPrefix reports whether dest is at or under the given slash-
// separated directory.
func hasPathPrefix(dest, dir string) bool {
	return dest == dir || strings.HasPrefix(dest, dir+"/")
}

// frontendAdvisoryOutput resolves the Next.js build shape a frontend's
// next.config.ts and Dockerfile were scaffolded with, so they render here
// exactly as they were written.
//
// An empty Output is "standalone" (config.FrontendConfig.EffectiveOutput),
// NOT the current scaffold default: every frontend whose forge.yaml entry
// leaves the field empty predates the static default and was scaffolded
// standalone. Rendering those against the static template would report a
// permanent diff — and `upgrade --force` would quietly convert the build.
func frontendAdvisoryOutput(fe config.FrontendConfig) string {
	return fe.EffectiveOutput()
}
