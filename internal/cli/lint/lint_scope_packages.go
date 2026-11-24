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
//     module's package, whatever `./dir/...` looks like). That holds for a
//     path anywhere inside the nested module, not only at its root: a
//     pre-push hook scoping to a changed gen/services/x/v1/x.pb.go, where
//     gen/ has its own go.mod, handed `./gen/services/x/v1` to the parent
//     module, and both Go lanes failed "main module does not contain
//     package". An unscoped run never lints a nested module either (`./...`
//     stops at its go.mod), so dropping the path keeps the two agreeing; the
//     skip note names the module (scopeSkipMsg);
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
	"fmt"
	"go/build"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
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
		if s.nestedModuleOf(abs, fi.IsDir()) != "" {
			continue // another module's source, at any depth inside it
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

// nestedModuleOf returns the project-relative directory of the nested module
// the scope path abs lies in — the nearest go.mod at or above it (at or above
// its directory, for a file) and below the project root — or "" when abs is
// this module's.
func (s *lintScope) nestedModuleOf(abs string, isDir bool) string {
	dir := abs
	if !isDir {
		dir = filepath.Dir(abs)
	}
	root := filepath.Clean(s.root)
	for dir = filepath.Clean(dir); dir != root; dir = filepath.Dir(dir) {
		rel, err := filepath.Rel(root, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "" // not under the root (parseLintScope refuses that)
		}
		if hasGoMod(dir) {
			return filepath.ToSlash(rel)
		}
	}
	return ""
}

// nestedModules lists the nested modules the scope's paths lie in, sorted.
func (s *lintScope) nestedModules() []string {
	if s == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range s.paths {
		abs := filepath.Join(s.root, filepath.FromSlash(p))
		fi, err := os.Stat(abs)
		if err != nil {
			continue
		}
		if mod := s.nestedModuleOf(abs, fi.IsDir()); mod != "" && !seen[mod] {
			seen[mod] = true
			out = append(out, mod)
		}
	}
	sort.Strings(out)
	return out
}

// scopeSkipMsg is the note a package lane prints when the scope reaches no
// Go package of this module. When scope paths lie in a nested module it says
// so by name, so a scope of only generated gen/ files does not read as a
// typo: that module is linted by a run inside it, never by this one.
func (s *lintScope) scopeSkipMsg(lane string) string {
	mods := s.nestedModules()
	if len(mods) == 0 {
		return "no Go package under --scope — skipping " + lane
	}
	noun := "is its own Go module"
	if len(mods) > 1 {
		noun = "are their own Go modules"
	}
	return fmt.Sprintf("no Go package of this module under --scope (%s %s, which forge lint lints only when run inside it) — skipping %s",
		strings.Join(mods, ", "), noun, lane)
}

func hasGoMod(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "go.mod"))
	return err == nil
}
