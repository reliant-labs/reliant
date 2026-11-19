package lint

// Translating `forge lint --scope` into Go package patterns.
//
// golangci-lint (and go/packages under the contract linter) refuses a set of
// patterns that resolves to no package in the module: "no go files to
// analyze". That is not "nothing to report" — it fails the lane. So a scope
// path contributes a pattern only when some lint pass would actually compile
// Go under it. A `.go` file is not enough evidence: reliant's scripts/ held
// shell scripts and one `//go:build ignore` program, the old check counted
// the .go file, and every scripts-only pull request failed golangci-lint.
//
// What no pass compiles, and so contributes no pattern:
//   - a file excluded by its build constraint on every pass (`ignore`, a
//     foreign GOOS/GOARCH, by `//go:build` or by file name);
//   - a nested module's source (a directory with its own go.mod is not this
//     module's package, whatever `./dir/...` looks like);
//   - the trees `go list ./...` itself skips (testdata, vendor, node_modules,
//     `.`- and `_`-prefixed directories).
//
// "Some pass" includes forge's per-tag golangci-lint passes
// (golangci_build_tags.go), so a directory of integration-tagged tests stays
// in scope for the integration pass; defaultPassPackages then keeps it out
// of the passes that compile only the default build. A scope reaching no
// compiled Go at all is vacuous for every package lane, and the engine skips
// each with a one-line note.

import (
	"go/build"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// goPackages translates the scope into Go package patterns for the lanes
// that take them: `./<dir>/...` for a directory, the containing package for
// a .go file. A path no lint pass compiles Go under (proto/, db/migrations/,
// scripts/ holding only shell and `//go:build ignore` programs) contributes
// nothing, and an empty result means no Go package is in scope.
func (s *lintScope) goPackages() []string {
	if s == nil {
		return nil
	}
	tags := golangciConfigBuildTags(s.root)
	var out []string
	seen := map[string]bool{}
	for _, p := range s.paths {
		abs := filepath.Join(s.root, filepath.FromSlash(p))
		fi, err := os.Stat(abs)
		if err != nil {
			continue
		}
		var pattern string
		switch {
		case fi.IsDir():
			if !s.compilesGo(abs, true, tags) {
				continue
			}
			pattern = "./" + p + "/..."
		case strings.HasSuffix(p, ".go"):
			// A file names its package: golangci-lint lints the package, and
			// the scope filters its findings back to the file.
			if !s.compilesGo(filepath.Dir(abs), false, tags) {
				continue
			}
			pattern = "./" + path.Dir(p)
			if path.Dir(p) == "." {
				pattern = "."
			}
		default:
			continue
		}
		if !seen[pattern] {
			seen[pattern] = true
			out = append(out, pattern)
		}
	}
	return out
}

// compilesGo reports whether some lint pass compiles a Go file in dir — under
// it too when recursive — walking as `go list ./dir/...` walks: no nested
// module, no testdata, vendor, node_modules, or `.`/`_`-prefixed directory.
func (s *lintScope) compilesGo(dir string, recursive bool, tags []string) bool {
	if dir != s.root && hasGoMod(dir) {
		return false // another module: none of this module's packages
	}
	found := false
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable subtree is simply not counted
		}
		if d.IsDir() {
			if p == dir {
				return nil
			}
			name := d.Name()
			if !recursive || name == "node_modules" || name == "vendor" || name == "testdata" ||
				strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || hasGoMod(p) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".go") && someLintPassCompiles(filepath.Dir(p), d.Name(), tags) {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

// someLintPassCompiles reports whether a golangci-lint pass compiles the Go
// file dir/name: the default pass (go/build's own matcher — file-name
// GOOS/GOARCH suffixes and the //go:build line — with tags, golangci's
// run.build-tags), or the pass forge adds for one project-defined tag the
// file's constraint names, which runs with tags plus that tag
// (collectGolangciLintJSON).
func someLintPassCompiles(dir, name string, tags []string) bool {
	ctx := build.Default
	ctx.BuildTags = tags
	if ok, _ := ctx.MatchFile(dir, name); ok {
		return true
	}
	expr := goBuildExpr(filepath.Join(dir, name))
	if expr == nil {
		return false
	}
	for _, tag := range exprTags(expr) {
		if isToolchainTag(tag) {
			continue // `ignore`, a GOOS, tools.go's pin: forge runs no pass for it
		}
		ctx.BuildTags = append(append([]string{}, tags...), tag)
		if ok, _ := ctx.MatchFile(dir, name); ok {
			return true
		}
	}
	return false
}

func hasGoMod(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "go.mod"))
	return err == nil
}
