package hostlaunch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/reliant-labs/forge/internal/goexec"
)

// airDefaultExcludeDir is air's built-in `[build] exclude_dir`, which a
// config that sets none runs with.
var airDefaultExcludeDir = []string{"assets", "tmp", "vendor", "testdata"}

// GoDeps is what a binary is built from: the directory of the package
// named (Main), and every path its build reads inside or outside the
// project — each dependency's package directory and each file a dependency
// embeds (Paths, Main included).
type GoDeps struct {
	Main  string
	Paths []string
}

// DepsLister lists what the Go package pkg is built from, resolved from
// dir with the build tags tags (comma-separated, may be empty). GoListDeps
// is the real one; tests stub it.
type DepsLister func(ctx context.Context, dir, tags, pkg string) (GoDeps, error)

// goListTimeout bounds the one `go list` a launch runs. It is a
// convenience, so a stuck module download must not hold up the dev loop.
const goListTimeout = 30 * time.Second

// GoListDeps runs `go list -deps` for pkg in dir. Directories are compared
// by path, not by module, so a go.work that joins nested modules (a gen/
// module beside the root one) needs nothing special: their packages list
// with the directories they live in.
func GoListDeps(ctx context.Context, dir, tags, pkg string) (GoDeps, error) {
	ctx, cancel := context.WithTimeout(ctx, goListTimeout)
	defer cancel()
	// One line per package (P) and per embedded file (E). -deps lists in
	// post-order, so the named package is the last P line.
	args := []string{"list", "-deps"}
	if tags != "" {
		args = append(args, "-tags="+tags)
	}
	args = append(args, "-f", `P {{.Dir}}{{range .EmbedFiles}}{{"\n"}}E {{$.Dir}}/{{.}}{{end}}`, pkg)
	cmd := exec.CommandContext(ctx, "go", args...) // #nosec G204 -- pkg is the workload's own GoBuild.cmd
	cmd.Dir = dir
	cmd.Env = goexec.Env()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			first, _, _ := strings.Cut(msg, "\n")
			return GoDeps{}, fmt.Errorf("go list %s: %s", pkg, first)
		}
		return GoDeps{}, fmt.Errorf("go list %s: %w", pkg, err)
	}
	return parseGoListDeps(string(out)), nil
}

func parseGoListDeps(out string) GoDeps {
	var deps GoDeps
	for _, line := range strings.Split(out, "\n") {
		kind, p, ok := strings.Cut(strings.TrimRight(line, "\r"), " ")
		if !ok || p == "" {
			continue
		}
		p = filepath.Clean(filepath.FromSlash(p))
		deps.Paths = append(deps.Paths, p)
		if kind == "P" {
			deps.Main = p
		}
	}
	return deps
}

// airWatchConfig is the part of an air config that decides what it watches
// and what it builds.
type airWatchConfig struct {
	TmpDir     string
	TestData   string
	ExcludeDir []string
	IncludeDir []string
	BuildCmd   string
}

func (s RunnerSpec) readAirWatchConfig() (airWatchConfig, string, error) {
	cfgPath, base := s.airPaths()
	var doc struct {
		Root     string `toml:"root"`
		TmpDir   string `toml:"tmp_dir"`
		TestData string `toml:"testdata_dir"`
		Build    struct {
			ExcludeDir []string `toml:"exclude_dir"`
			IncludeDir []string `toml:"include_dir"`
		} `toml:"build"`
	}
	if _, err := toml.DecodeFile(cfgPath, &doc); err != nil {
		return airWatchConfig{}, "", err
	}
	root := base
	if r := strings.TrimSpace(doc.Root); r != "" && r != "." {
		root = joinUnder(base, r)
	}
	return airWatchConfig{
		TmpDir: doc.TmpDir, TestData: doc.TestData,
		ExcludeDir: doc.Build.ExcludeDir, IncludeDir: doc.Build.IncludeDir,
		BuildCmd: s.AirBuildCmd(),
	}, root, nil
}

// effectiveExcludeDir is the exclude_dir air runs this config with: the
// config's own list, or air's default when it sets none — with `tmp`
// renamed to a non-default tmp_dir, as air does to its default.
func (c airWatchConfig) effectiveExcludeDir() []string {
	if len(c.ExcludeDir) > 0 {
		return slices.Clone(c.ExcludeDir)
	}
	out := slices.Clone(airDefaultExcludeDir)
	if tmp := strings.TrimSpace(c.TmpDir); tmp != "" && tmp != "tmp" {
		for i, d := range out {
			if d == "tmp" {
				out[i] = tmp
			}
		}
	}
	return out
}

// AirUnwatchedDirs derives, at launch, the top-level directories under the
// air root that hold Go packages the workload's binary (GoPkg) is not
// built from — examples/, benchmarks/, tools/, a nested module the binary
// does not use. An edit there cannot change the binary, yet air rebuilt and
// restarted the server for it. The directories the config already excludes
// are not listed again.
//
// Deriving the list rather than writing it into .air.toml is what applies
// it to every existing project without editing a user-owned file, and keeps
// it current as packages come and go.
//
// It returns nothing, and no error, where it has nothing sound to derive
// from: a runner other than air, no Go build, an unreadable config, a config
// that sets `include_dir` (its author chose the watch set), a root different
// from air's launch directory, a build command
// that does not visibly build GoPkg (a `make build` could build anything),
// or a binary whose package is outside the air root (a cross-repo
// working_dir). An error means list failed; the caller says so and
// launches air unchanged.
//
// Only top-level directories are considered. The list is decided once, at
// launch, while imports change during the session: a top-level tree the
// binary imports nothing from rarely starts being imported mid-session,
// whereas a package under internal/ routinely does — and an exclusion that
// went stale would leave a stale binary running with no rebuild, which is
// worse than a spurious restart.
func (s RunnerSpec) AirUnwatchedDirs(ctx context.Context, list DepsLister) ([]string, error) {
	if strings.TrimSpace(s.Runner) != "air" || strings.TrimSpace(s.GoPkg) == "" {
		return nil, nil
	}
	cfg, root, err := s.readAirWatchConfig()
	if err != nil || len(cfg.IncludeDir) > 0 || !buildsPackage(cfg.BuildCmd, s.GoPkg) {
		return nil, nil
	}
	// Derived names are relative to the scanned root, but air resolves
	// exclude_dir from its launch directory. Leave custom roots unchanged
	// rather than risk excluding a different tree containing dependencies.
	_, base := s.airPaths()
	if filepath.Clean(root) != filepath.Clean(base) {
		return nil, nil
	}
	dir := s.ProjectDir
	if dir == "" {
		dir = root
	}
	deps, err := list(ctx, dir, buildTags(append(slices.Clone(s.GoBuildFlags), goBuildArgs(cfg.BuildCmd)...)), strings.TrimSpace(s.GoPkg))
	if err != nil {
		return nil, err
	}
	roots := []string{filepath.Clean(root)}
	if r, err := filepath.EvalSymlinks(root); err == nil && filepath.Clean(r) != roots[0] {
		roots = append(roots, filepath.Clean(r))
	}
	if deps.Main == "" || !underAny(roots, deps.Main) {
		return nil, nil
	}
	skip := cfg.effectiveExcludeDir()
	for _, d := range []string{cfg.TmpDir, cfg.TestData} {
		if d = strings.TrimSpace(d); d != "" {
			skip = append(skip, d)
		}
	}
	return unbuiltTopDirs(roots, deps.Paths, skip)
}

// unbuiltTopDirs lists, sorted, the top-level directories of roots[0] that
// contain a .go file and none of the paths kept (a package directory or an
// embedded file the binary is built from). roots holds the root as given
// and, when it differs, its symlink-resolved form, since go list reports
// resolved directories. Hidden directories are never listed (air does not
// watch them), nor any named in skip.
func unbuiltTopDirs(roots, kept, skip []string) ([]string, error) {
	entries, err := os.ReadDir(roots[0])
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		// A symlinked directory is not a DirEntry dir; air does not follow
		// it either unless follow_symlink is set, so leave it alone.
		if !e.IsDir() || strings.HasPrefix(name, ".") || slices.Contains(skip, name) || strings.Contains(name, ",") {
			continue
		}
		var dirs []string
		for _, r := range roots {
			dirs = append(dirs, filepath.Join(r, name))
		}
		if slices.ContainsFunc(kept, func(p string) bool { return underAny(dirs, p) }) {
			continue
		}
		if hasGoFile(dirs[0]) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out, nil
}

// errFound stops a walk at the first .go file.
var errFound = errors.New("found")

// hasGoFile reports whether dir's tree holds a .go file outside the
// directories the go tool ignores (hidden, testdata) and node_modules.
// A directory with no Go code is left watched: its other files (yaml, say)
// may be read by the server at run time.
func hasGoFile(dir string) bool {
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			n := d.Name()
			if p != dir && (strings.HasPrefix(n, ".") || n == "testdata" || n == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".go") {
			return errFound
		}
		return nil
	})
	return errors.Is(err, errFound)
}

// underAny reports whether p is one of dirs or inside one of them.
func underAny(dirs []string, p string) bool {
	for _, d := range dirs {
		rel, err := filepath.Rel(d, p)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

// buildsPackage reports whether an air build command visibly builds the
// Go package pkg: it names the package's path (`./cmd/acme` → `cmd/acme`).
// The scaffolded command does; a `make build` names nothing, and forge
// then cannot tell what the binary is built from.
func buildsPackage(buildCmd, pkg string) bool {
	p := path.Clean(filepath.ToSlash(strings.TrimSpace(pkg)))
	if p == "." {
		return strings.Contains(buildCmd, "go build")
	}
	return strings.Contains(filepath.ToSlash(buildCmd), strings.TrimPrefix(p, "./"))
}

// goBuildArgs returns the arguments of the first `go build` in a build
// command line, up to the next shell separator.
func goBuildArgs(cmd string) []string {
	f := strings.Fields(cmd)
	for i := 0; i+1 < len(f); i++ {
		if f[i] != "go" || f[i+1] != "build" {
			continue
		}
		var out []string
		for _, a := range f[i+2:] {
			if a == "&&" || a == ";" || a == "||" {
				break
			}
			out = append(out, a)
		}
		return out
	}
	return nil
}

// buildTags returns the value of the last -tags flag in args (the one the
// go tool honours), or "".
func buildTags(args []string) string {
	tags := ""
	for i, a := range args {
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || name != "tags" {
			continue
		}
		if !hasVal {
			if i+1 >= len(args) {
				continue
			}
			val = args[i+1]
		}
		tags = strings.Trim(val, `"'`)
	}
	return tags
}

// airExcludeArgs is the `-build.exclude_dir` flag that adds
// AirExcludeDirs to the exclude_dir the air config runs with. air's
// command-line value REPLACES the config's list rather than extending it,
// so the flag carries the config's own entries first: every directory the
// config excludes stays excluded, and the file is never edited. Nothing is
// added when the config cannot be read (air reports it) or an entry holds
// the comma air splits the value on.
func (s RunnerSpec) airExcludeArgs() []string {
	if len(s.AirExcludeDirs) == 0 {
		return nil
	}
	cfg, _, err := s.readAirWatchConfig()
	if err != nil {
		return nil
	}
	merged := cfg.effectiveExcludeDir()
	before := len(merged)
	for _, d := range s.AirExcludeDirs {
		if !slices.Contains(merged, d) {
			merged = append(merged, d)
		}
	}
	if len(merged) == before || slices.ContainsFunc(merged, func(d string) bool { return strings.Contains(d, ",") }) {
		return nil
	}
	return []string{"-build.exclude_dir", strings.Join(merged, ",")}
}
