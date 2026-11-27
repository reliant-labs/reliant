package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/reliant-labs/forge/internal/goexec"
)

// blockedBuildPackages finds project packages that cmd/go did not compile
// because a dependency failed. Independent callers are already collected by
// go list -e -export -deps; importers of a broken caller can be missing even
// when they contain independent errors of their own (e.g. assigning an updated
// store to a consumer-declared interface at the composition root).
func blockedBuildPackages(projectDir string, pkgs []*listedPackage) []string {
	var blocked []string
	for _, p := range pkgs {
		if p.DepOnly || p.testBuild() || p.Error != nil || len(p.DepsErrors) == 0 || p.Dir == "" {
			continue
		}
		rel, err := filepath.Rel(projectDir, p.Dir)
		if err == nil && filepath.IsLocal(rel) {
			blocked = append(blocked, p.ImportPath)
		}
	}
	return blocked
}

// blockedBuildDiagnostics augments a failed compiler verdict, never changes
// it. On failure only, go/packages uses export data where available and checks
// the blocked packages from source, even when a dependency's implementation
// did not compile but its declarations still have usable types.
func blockedBuildDiagnostics(projectDir string, blocked []string) string {
	if len(blocked) == 0 {
		return ""
	}
	absDir, err := filepath.Abs(projectDir)
	if err != nil {
		return ""
	}
	pkgs, err := packages.Load(&packages.Config{
		Dir: absDir,
		Env: goexec.CheckEnv("GOPACKAGESDRIVER=off"),
		// NeedTypes without NeedDeps uses available export data and falls
		// back to source in broken dependencies; it checks function bodies
		// only in the root packages we actually want diagnostics from.
		Mode: packages.LoadTypes,
	}, blocked...)
	if err != nil {
		return ""
	}
	var extra strings.Builder
	for _, p := range pkgs {
		var own strings.Builder
		for _, diag := range p.Errors {
			if diag.Kind != packages.TypeError || diag.Pos == "" || strings.HasPrefix(diag.Msg, "could not import ") {
				// A missing import's diagnostics were already shown by cmd/go.
				continue
			}
			pos, err := filepath.Rel(absDir, diag.Pos)
			if err != nil || !filepath.IsLocal(pos) {
				continue
			}
			fmt.Fprintf(&own, "%s: %s\n", filepath.ToSlash(pos), diag.Msg)
		}
		if own.Len() > 0 {
			fmt.Fprintf(&extra, "# %s (type-checked from source; dependency did not compile)\n%s", p.PkgPath, own.String())
		}
	}
	return extra.String()
}
