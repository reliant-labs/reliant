package cli

import (
	"fmt"
	"os"
	"path"
	"strings"

	"golang.org/x/mod/modfile"
)

// versionStampPackages returns the import paths whose version/commit/date
// vars `forge build` stamps for a GoBuild of cmd in module.
//
// `main` is the conventional home (and where a `kind = cli` scaffold puts
// them). A server scaffold does not: cmd-tree-version.go.tmpl declares them
// in the cobra tree's package, <module>/cmd/<name>/cmd, because `-X` names a
// variable by its full import path and the `version` subcommand lives there.
// Stamping only main.* left every such binary reporting version "dev",
// commit "none" — including the release a Sentry event is filed under
// (control-plane's prod events arrived as `control-plane@dev`).
//
// So forge stamps both. The linker ignores a -X whose package is not linked
// into the binary, so a project that keeps its vars only in main loses
// nothing. An absolute cmd (an import path rather than ./path) is used as
// given; module "" (no readable go.mod) stamps main alone.
func versionStampPackages(module, cmd string) []string {
	pkgs := []string{"main"}
	var target string
	switch {
	case strings.HasPrefix(cmd, "./") || cmd == ".":
		if module == "" {
			return pkgs
		}
		target = path.Join(module, path.Clean(cmd))
	case module != "" && (cmd == module || strings.HasPrefix(cmd, module+"/")):
		target = path.Clean(cmd)
	default:
		return pkgs
	}
	return append(pkgs, target+"/cmd")
}

// versionStampLdflags renders the -X flags for the packages above.
func versionStampLdflags(module, cmd string, v versionInfo) string {
	var b strings.Builder
	for i, pkg := range versionStampPackages(module, cmd) {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "-X %s.version=%s -X %s.commit=%s -X %s.date=%s",
			pkg, v.version, pkg, v.commit, pkg, v.date)
	}
	return b.String()
}

// buildModulePath reads the module path of the project forge is building in
// (the working directory). "" when there is no readable go.mod.
func buildModulePath() string {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		return ""
	}
	return modfile.ModulePath(data)
}
