package lint

// golangci-lint sees only the files the build tags it is given select. A file
// behind `//go:build integration` is invisible to a run with no tags, so a
// finding in it (control-plane, 2026-10-22: noctx in an integration test) was
// reported by the project's CI, which lints once per tag set, and not by
// `forge lint`, which linted once.
//
// The default pass stays as it was: golangci-lint applies the project's own
// `run.build-tags` to it. On top of that, every other tag that gates a file in
// the tree gets one pass of its own, under that tag alone and only over the
// packages holding such files. One pass per tag, not one pass with every tag
// at once: tags combine with `!tag` and `&&` in ways a union would
// misjudge (a file tagged `!integration` is DROPPED by the integration pass),
// and a combined pass would lint every untagged package a second time.

import (
	"bufio"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// buildTagPackages is one build tag and the package directories (relative to
// the module root, "./a/b") holding a file that only that tag builds.
type buildTagPackages struct {
	tag  string
	dirs []string
}

// builtinTags are the tags the toolchain defines. None of them is a tag a
// project opts into, so none of them gets a pass of its own.
var builtinTags = map[string]bool{
	"ignore": true, "cgo": true, "gc": true, "gccgo": true, "unix": true,
	"race": true, "msan": true, "asan": true, "purego": true, "boringcrypto": true,
}

var knownGOOS = map[string]bool{
	"aix": true, "android": true, "darwin": true, "dragonfly": true, "freebsd": true,
	"hurd": true, "illumos": true, "ios": true, "js": true, "linux": true, "nacl": true,
	"netbsd": true, "openbsd": true, "plan9": true, "solaris": true, "wasip1": true,
	"windows": true, "zos": true,
}

var knownGOARCH = map[string]bool{
	"386": true, "amd64": true, "amd64p32": true, "arm": true, "armbe": true, "arm64": true,
	"arm64be": true, "loong64": true, "mips": true, "mipsle": true, "mips64": true,
	"mips64le": true, "mips64p32": true, "mips64p32le": true, "ppc": true, "ppc64": true,
	"ppc64le": true, "riscv": true, "riscv64": true, "s390": true, "s390x": true,
	"sparc": true, "sparc64": true, "wasm": true,
}

// toolPinTag is the conventional tag of a tools.go file. It pins tool
// dependencies with blank imports of `package main` programs, which the Go
// type checker refuses ("is a program, not an importable package"), so a pass
// under it can only fail. It is never a project's own build.
const toolPinTag = "tools"

func isToolchainTag(tag string) bool {
	return tag == toolPinTag || builtinTags[tag] || knownGOOS[tag] || knownGOARCH[tag] || strings.HasPrefix(tag, "go1.")
}

// hostTag reports the tags true on the machine linting.
func hostTag(tag string) bool {
	switch tag {
	case runtime.GOOS, runtime.GOARCH, "gc":
		return true
	case "unix":
		return runtime.GOOS != "windows" && runtime.GOOS != "plan9" && runtime.GOOS != "js" && runtime.GOOS != "wasip1"
	}
	return strings.HasPrefix(tag, "go1.")
}

// discoverBuildTags walks the module at root for `//go:build` lines and
// returns, per project-defined tag, the packages whose files only that tag
// builds. Tags sort, and so do directories.
func discoverBuildTags(root string) []buildTagPackages {
	byTag := map[string]map[string]bool{}
	hasCode := map[string]bool{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p == root {
				return nil
			}
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "node_modules" || name == "vendor" || name == "testdata" {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
				return filepath.SkipDir // another module: not this run's packages
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		expr := goBuildExpr(p)
		if expr == nil {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(p))
		if err != nil {
			return nil
		}
		dir := "."
		if rel != "." {
			dir = "./" + filepath.ToSlash(rel)
		}
		pin := isToolPinFile(p)
		for _, tag := range exprTags(expr) {
			if isToolchainTag(tag) {
				continue
			}
			if builtOnlyUnder(expr, tag) {
				if !pin {
					hasCode[tag] = true
				}
				if byTag[tag] == nil {
					byTag[tag] = map[string]bool{}
				}
				byTag[tag][dir] = true
			}
		}
		return nil
	})
	var out []buildTagPackages
	for tag, set := range byTag {
		if !hasCode[tag] {
			continue // every file under it is a tool pin: nothing to lint
		}
		dirs := make([]string, 0, len(set))
		for d := range set {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)
		out = append(out, buildTagPackages{tag: tag, dirs: dirs})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].tag < out[j].tag })
	return out
}

// goBuildExpr returns the //go:build constraint of the Go file at p, or nil.
// The constraint sits above the package clause, so the scan stops there.
func goBuildExpr(p string) constraint.Expr {
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "package ") || line == "package" {
			return nil
		}
		if constraint.IsGoBuild(line) {
			expr, err := constraint.Parse(line)
			if err != nil {
				return nil
			}
			return expr
		}
	}
	return nil
}

// isToolPinFile reports a Go file that declares nothing but blank imports:
// the shape of tools.go under whatever tag a project gave it.
func isToolPinFile(p string) bool {
	f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.SkipObjectResolution)
	if err != nil || len(f.Decls) == 0 {
		return false
	}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.IMPORT {
			return false
		}
		for _, sp := range gd.Specs {
			if is, ok := sp.(*ast.ImportSpec); !ok || is.Name == nil || is.Name.Name != "_" {
				return false
			}
		}
	}
	return true
}

func exprTags(e constraint.Expr) []string {
	seen := map[string]bool{}
	var walk func(constraint.Expr)
	walk = func(e constraint.Expr) {
		switch x := e.(type) {
		case *constraint.TagExpr:
			seen[x.Tag] = true
		case *constraint.NotExpr:
			walk(x.X)
		case *constraint.AndExpr:
			walk(x.X)
			walk(x.Y)
		case *constraint.OrExpr:
			walk(x.X)
			walk(x.Y)
		}
	}
	walk(e)
	tags := make([]string, 0, len(seen))
	for t := range seen {
		tags = append(tags, t)
	}
	sort.Strings(tags)
	return tags
}

// builtOnlyUnder reports whether the file is built with tag set and not
// without it, on this host: the files a default run cannot see.
func builtOnlyUnder(e constraint.Expr, tag string) bool {
	with := e.Eval(func(t string) bool { return t == tag || hostTag(t) })
	without := e.Eval(hostTag)
	return with && !without
}

// golangciConfigBuildTags returns run.build-tags from the golangci-lint
// config a run in dir loads. golangci-lint applies them to the default pass
// itself, so forge must not repeat them as a pass of their own.
func golangciConfigBuildTags(dir string) []string {
	cfg := findGolangciConfig(dir)
	if cfg == "" || strings.HasSuffix(cfg, ".toml") {
		return nil
	}
	raw, err := os.ReadFile(cfg)
	if err != nil {
		return nil
	}
	var doc struct {
		Run struct {
			BuildTags []string `yaml:"build-tags"`
		} `yaml:"run"`
	}
	if yaml.Unmarshal(raw, &doc) != nil {
		return nil
	}
	return doc.Run.BuildTags
}

// scopeBuildTags narrows sets to the packages the run's package patterns
// (`./...`, `./a/...`, `./a`, `.`) reach, dropping a tag left with none.
func scopeBuildTags(sets []buildTagPackages, patterns []string) []buildTagPackages {
	var out []buildTagPackages
	for _, set := range sets {
		var dirs []string
		for _, d := range set.dirs {
			for _, pat := range patterns {
				if patternReaches(pat, d) {
					dirs = append(dirs, d)
					break
				}
			}
		}
		if len(dirs) > 0 {
			out = append(out, buildTagPackages{tag: set.tag, dirs: dirs})
		}
	}
	return out
}

func patternReaches(pattern, dir string) bool {
	pattern = filepath.ToSlash(pattern)
	recursive := pattern == "..." || strings.HasSuffix(pattern, "/...")
	pattern = strings.TrimSuffix(strings.TrimSuffix(pattern, "..."), "/")
	pattern = path.Clean("./" + strings.TrimPrefix(pattern, "./"))
	base := pattern
	if base != "." {
		base = "./" + base
	}
	if recursive {
		return base == "." || dir == base || strings.HasPrefix(dir, base+"/")
	}
	return dir == base
}
