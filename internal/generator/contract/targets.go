package contract

import (
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/depsurface"
	"github.com/reliant-labs/forge/internal/pkgdirective"
)

// ExcludeContractDirective is the package directive that opts a package out
// of contract codegen — the per-package equivalent of forge.yaml
// `contracts.exclude`: `//forge:exclude-contract: <why>`.
const ExcludeContractDirective = "forge:exclude-contract"

// ExcludedByDirective reports whether the package in dir carries
// `//forge:exclude-contract`, reasoned or bare. The bare form still excludes:
// codegen never breaks a project over the spelling of an opt-out (`forge
// lint` is what refuses it).
func ExcludedByDirective(dir string) bool {
	_, ok := pkgdirective.Find(dir, ExcludeContractDirective)
	return ok
}

// Targets lists every file the contracts step generates from under
// internalDir — each contract.go, and the marked deps.go of a package without
// one — pruning testdata/ subtrees and every directory excluded reports true
// for (its module-relative, slash-separated path, matched against forge.yaml
// `contracts.exclude`). A nil excluded excludes nothing.
//
// Packages that opt out with `//forge:exclude-contract` are still listed:
// the caller decides what an opted-out package gets (forge generate retires
// its artifacts; the mock bootstrap skips it). Listing them costs the
// Resolver nothing it would not also resolve for a sibling.
//
// `forge generate` builds ONE Resolver over this list before its contracts
// walk, and internal/tools/mockgen generates from it, so the two agree on
// which packages have mocks. A deps.go left out of the Resolver still
// generates, but every interface it embeds from another package resolves to
// nothing: its mock silently lacks those methods and fails generate's build
// validation. Walk errors are not fatal here: the generating walk reports
// them.
func Targets(internalDir, projectDir string, excluded func(rel string) bool) []string {
	var paths []string
	_ = filepath.WalkDir(internalDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if d.Name() == "testdata" {
			return filepath.SkipDir
		}
		if rel, relErr := filepath.Rel(projectDir, path); relErr == nil && excluded != nil && excluded(filepath.ToSlash(rel)) {
			return filepath.SkipDir
		}
		contractPath := filepath.Join(path, "contract.go")
		if _, statErr := os.Stat(contractPath); statErr == nil {
			paths = append(paths, contractPath)
		} else if os.IsNotExist(statErr) && depsurface.HasDepsMarker(path) {
			paths = append(paths, filepath.Join(path, depsurface.DepsFileName))
		}
		return nil
	})
	return paths
}
