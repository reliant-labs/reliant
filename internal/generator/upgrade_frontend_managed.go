// Per-frontend Tier-2 managed files.
//
// `forge project upgrade` has two lanes, and the difference between them is
// the whole point of this file:
//
//	the ADVISORY lane (upgrade_advisory.go) REPORTS. It says "the template
//	moved on, here is the diff" and never writes unless the exact path is
//	named after --force. It reports staleness in one direction only —
//	template content the file lacks — so a file the user merely ADDED to is
//	silent, and there is no `disown`, because a file forge handed over at
//	birth was never forge's to be disowned from.
//
//	the MANAGED lane (upgrade.go) TRACKS. A pristine render is refreshed
//	automatically on upgrade; an edited one is reported as `user-modified
//	(skipped)` with a diff and BOTH remedies — `--force <path>` to adopt,
//	`disown` to claim.
//
// frontends/<name>/eslint.config.mjs was in NEITHER, which is the reported
// bug: hand-edit it and no forge command mentions it, ever. `.golangci.yml`
// — the same kind of file on the backend side — is a Tier-2 managed file
// and reports exactly as described above.
//
// It is registered here in the MANAGED lane rather than the advisory one
// for two reasons, and the second is load-bearing:
//
//  1. Symmetry with .golangci.yml is what was actually asked for: the same
//     file class should behave the same way on both sides of the stack,
//     including the two remedies. The advisory lane cannot offer `disown`
//     and cannot report a pure addition.
//
//  2. It is the only lane that makes template improvements ARRIVE. An
//     advisory row tells you a rule exists; a managed file DELIVERS it to
//     every project whose copy is still pristine. That distinction decided
//     the design of the frontend process.env guardrail, which documented
//     the constraint precisely: "eslint.config.mjs is SCAFFOLD-ONCE … not
//     in generator.managedFiles(), so `forge project upgrade` never
//     refreshes it — a rule added there would reach projects created after
//     this change and NO existing project". Registering it here is what
//     retires that objection.
//
// ── Tier 2, not Tier 1 ────────────────────────────────────────────────
//
// A lint config is a file users legitimately edit — a project-specific rule,
// a disabled check, an extra ignore. Tier-1 would stomp that every generate.
// Tier-2 is the tier whose contract is exactly "keep it current while it is
// untouched, and never stomp an edit": reporting drift is not the same as
// forge owning the file.
//
// ── Why these entries carry a render closure ──────────────────────────
//
// Every other managedFile names a template in the project/ category and
// writes to a fixed path. These are neither: the template is one per
// frontend KIND (internal/templates/frontend/<kind>/eslint.config.mjs) and
// the destination is one per declared FRONTEND. So the entries are
// generated per frontend with their render attached — see managedFile.render.
package generator

import (
	"path/filepath"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/templates"
)

// frontendManagedRel is the per-frontend lint config, relative to the
// frontend root. It is the same name in every tree that has one.
const frontendManagedRel = "eslint.config.mjs"

// frontendManagedFiles returns one Tier-2 managed entry per declared
// frontend whose template tree ships a lint config.
//
// react-native ships none, and a project may declare no frontends at all,
// so the result is frequently empty — which is why every caller treats a
// missing entry as "this project has no such file" rather than an error.
func frontendManagedFiles(cfg *config.ProjectConfig) []managedFile {
	if cfg == nil {
		return nil
	}
	var out []managedFile
	for _, fe := range cfg.Frontends {
		tree := frontendTemplateTreeFor(fe)
		tmplPath := frontendLintTemplatePath(tree)
		if tmplPath == "" {
			continue
		}
		// A frontend with no directory in this repository — a cross-repo
		// `source:` pin, or a sibling-checkout path — is SKIPPED rather
		// than given the conventional frontends/<name> label the retired
		// EffectivePath returned. Managed files are WRITTEN, not just
		// named: forge rewrites this path on every upgrade, so a
		// fabricated location would have forge creating a lint config in
		// a directory no frontend lives in, and the stale-artifact sweep
		// would then keep it alive. The real tree belongs to another
		// repository, whose own forge manages its lint config.
		dir, ok := fe.Dir(".")
		if !ok {
			continue
		}
		out = append(out, managedFile{
			destPath: filepath.Join(filepath.FromSlash(dir), frontendManagedRel),
			tier:     Tier2,
			render: func() ([]byte, error) {
				return templates.FrontendTemplates().Get(tmplPath)
			},
		})
	}
	return out
}

// frontendLintTemplatePath returns the composed-tree template path for a
// frontend kind's lint config, or "" when that kind ships none.
//
// Resolved by ASKING the template tree rather than by listing kinds, so a
// tree that gains or loses a lint config is handled without an edit here —
// forgetting to update a second registry is the bug class this whole change
// exists to fix, and repeating it one file later would be a poor joke.
func frontendLintTemplatePath(tree string) string {
	files, err := templates.ListFrontendTree(tree)
	if err != nil {
		return ""
	}
	for _, f := range files {
		if f.Rel == frontendManagedRel {
			return f.Path
		}
	}
	return ""
}

// IsFrontendManagedPath reports whether a project-relative path is a
// per-frontend managed file, by SHAPE rather than by membership.
//
// The stale-artifact sweep needs this. That sweep deletes any file carrying
// forge's certification marker that the current `forge generate` run did
// not write: the pristine ones outright, the hand-edited ones under
// `--force-cleanup`. A
// Tier-2 managed file is stamped and is written by `forge project upgrade`,
// not by `forge generate`, so it lands in the sweep's crosshairs exactly as
// the command-tree files did — the data-loss incident UpgradeManagedPaths
// documents at length.
//
// UpgradeManagedPaths answers that question from a static union over
// (kind, binary), computed with no project config. These paths depend on
// the frontends a project declares, so they cannot appear in that union.
// A shape test is the honest way to express "frontends/<any>/eslint.config.mjs
// belongs to the upgrade lane" without a config in hand.
func IsFrontendManagedPath(rel string) bool {
	slashed := filepath.ToSlash(rel)
	if filepath.Base(slashed) != frontendManagedRel {
		return false
	}
	// frontends/<name>/eslint.config.mjs — the conventional layout the
	// scaffolder writes. A frontend at a custom `path:` is covered by the
	// config-aware ManagedPathsFor, which the user-facing --force
	// validation uses.
	parts := splitSlash(slashed)
	return len(parts) == 3 && parts[0] == "frontends"
}

// splitSlash splits a slash-separated path into its segments.
func splitSlash(p string) []string {
	var out []string
	start := 0
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			out = append(out, p[start:i])
			start = i + 1
		}
	}
	return append(out, p[start:])
}
