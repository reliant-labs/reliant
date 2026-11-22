package contract

import (
	"go/ast"
	"regexp"

	"golang.org/x/tools/go/analysis"
)

// Generated-file exemption — the principle:
//
// contractlint judges USER code. A machine-generated file is forge's
// responsibility: its shape is dictated by the emitter, the user cannot
// durably "fix" it (the next `forge generate` re-emits it), and forge's
// own conventions sometimes REQUIRE shapes the contract rules forbid in
// hand-written code (e.g. the documented `<Entity>Columns` package var
// every Tier-1 `_orm.go` exports as the CRUD column allowlist, or the
// data-only `Inventory` descriptor in mounts_services.go). Flagging
// those puts the user in an unwinnable loop, so every contract rule
// skips generated files and stays fully strict on hand-written ones.
//
// A file counts as generated when EITHER signal is present (union):
//
//   - the canonical Go convention header — `// Code generated ... DO NOT
//     EDIT.` before the package clause (ast.IsGenerated, which is what
//     forge's Tier-1 emitters write as line 1);
//   - forge's self-certifying content-hash marker — a standalone
//     `// forge:hash=<sha256-hex>` comment line (internal/checksums
//     stamp.go). This is the belt-and-suspenders fallback for a file
//     whose banner drifted from the exact stdlib-recognized form but
//     that still carries forge's own certification.

// forgeHashMarkerRE matches the exact marker line internal/checksums
// stamps into Go files: `// forge:hash=<64 hex>`. The anchors keep prose
// that merely MENTIONS the marker (e.g. documentation comments quoting
// it with extra indentation) from counting as certification.
var forgeHashMarkerRE = regexp.MustCompile(`^//\s*forge:hash=[0-9a-f]{64}$`)

// isForgeGeneratedFile reports whether f is machine-generated output —
// the canonical `Code generated ... DO NOT EDIT.` header or forge's
// `// forge:hash=` self-certification marker (see the package-level
// rationale above). Only comments positioned before the package clause
// are considered for the hash marker: that is where the stamper places
// it, and it keeps in-body mentions from counting.
func isForgeGeneratedFile(f *ast.File) bool {
	if ast.IsGenerated(f) {
		return true
	}
	for _, cg := range f.Comments {
		if cg.Pos() >= f.Package {
			break
		}
		for _, c := range cg.List {
			if forgeHashMarkerRE.MatchString(c.Text) {
				return true
			}
		}
	}
	return false
}

// generatedFilenames returns the set of on-disk filenames in the pass
// whose *ast.File is generated (isForgeGeneratedFile). Inspector-driven
// analyzers resolve a node's filename via pass.Fset and consult this set
// to skip findings inside generated files while staying strict on the
// hand-written files of the same package.
func generatedFilenames(pass *analysis.Pass) map[string]bool {
	out := make(map[string]bool)
	for _, f := range pass.Files {
		if isForgeGeneratedFile(f) {
			out[pass.Fset.Position(f.Pos()).Filename] = true
		}
	}
	return out
}
