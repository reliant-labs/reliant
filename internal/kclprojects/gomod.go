package kclprojects

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

	"github.com/reliant-labs/forge/internal/goexec"
)

// goModules is the importing project's Go module configuration: everything
// that decides where the go command reads a dependency from.
type goModules struct {
	dir      string // the go.mod's directory
	gomod    string // the go.mod's path
	module   string // the main module's path
	file     *modfile.File
	work     *modfile.WorkFile // nil outside workspace mode
	workPath string
	// sums maps "path version" to the go.sum (and go.work.sum) h1 hash of
	// that module's zip.
	sums map[string]string
}

// location is where a module's files are, and what said so.
type location struct {
	dir     string
	version string // "" for a directory
	via     string
}

func loadGoModules(dir string) (*goModules, error) {
	g := &goModules{dir: dir, gomod: filepath.Join(dir, "go.mod"), sums: map[string]string{}}
	data, err := os.ReadFile(g.gomod)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", g.gomod, err)
	}
	// Parse, not ParseLax: ParseLax drops replace directives by design.
	if g.file, err = modfile.Parse(g.gomod, data, nil); err != nil {
		return nil, fmt.Errorf("parse %s: %w", g.gomod, err)
	}
	if g.file.Module != nil {
		g.module = g.file.Module.Mod.Path
	}
	readSums(filepath.Join(dir, "go.sum"), g.sums)

	if g.workPath = activeGoWork(dir); g.workPath != "" {
		data, err := os.ReadFile(g.workPath)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", g.workPath, err)
		}
		if g.work, err = modfile.ParseWork(g.workPath, data, nil); err != nil {
			return nil, fmt.Errorf("parse %s: %w", g.workPath, err)
		}
		readSums(g.workPath+".sum", g.sums)
	}
	return g, nil
}

// activeGoWork is the go.work the go command would use for a build in dir:
// none under GOWORK=off, the named file under GOWORK=<file>, else the nearest
// go.work at or above dir.
func activeGoWork(dir string) string {
	switch gw := strings.TrimSpace(os.Getenv("GOWORK")); {
	case gw == "off":
		return ""
	case gw != "":
		if !filepath.IsAbs(gw) {
			gw = filepath.Join(dir, gw)
		}
		return gw
	}
	if found := findUp(dir, "go.work"); found != "" {
		return filepath.Join(found, "go.work")
	}
	return ""
}

// locate finds the directory the go command builds imp.Module from.
func (g *goModules) locate(imp Import) (location, error) {
	// A workspace `use` makes the module a main module: the workspace
	// builds that directory, whatever any go.mod requires.
	if g.work != nil {
		base := filepath.Dir(g.workPath)
		for _, u := range g.work.Use {
			dir := resolveDir(base, u.Path)
			if moduleAt(dir) == imp.Module {
				return location{dir: dir, via: "go.work use " + u.Path}, nil
			}
		}
	}

	version := ""
	for _, r := range g.file.Require {
		if r != nil && r.Mod.Path == imp.Module {
			version = r.Mod.Version
		}
	}
	if version == "" {
		return location{}, fmt.Errorf("%s imports forge project %q (%s), but %s does not require that module.\n"+
			"  A [%s] import is read at the version go.mod pins the module to, so go.mod must pin it:\n"+
			"    go get %s@<version>\n"+
			"  (If your Go code does not depend on %s, there is no Go pin to share: declare a kpm git dependency in\n"+
			"  [dependencies] instead, with its own ref.)",
			imp.DeclaredIn, imp.Name, imp.Module, g.gomod, TableName, imp.Module, imp.Module)
	}

	// go.work's replaces override go.mod's.
	if g.work != nil {
		if loc, ok, err := g.replaced(g.work.Replace, g.workPath, imp, version); ok || err != nil {
			return loc, err
		}
	}
	if loc, ok, err := g.replaced(g.file.Replace, g.gomod, imp, version); ok || err != nil {
		return loc, err
	}
	return g.fromCache(imp, imp.Module, version, fmt.Sprintf("go.mod require %s %s", imp.Module, version))
}

// replaced applies the replace directive, if any, that the go command would
// apply to module@version: one naming that exact version wins over one that
// names none.
func (g *goModules) replaced(reps []*modfile.Replace, file string, imp Import, version string) (location, bool, error) {
	var hit *modfile.Replace
	for _, r := range reps {
		if r == nil || r.Old.Path != imp.Module {
			continue
		}
		if r.Old.Version == version {
			hit = r
			break
		}
		if r.Old.Version == "" {
			hit = r
		}
	}
	if hit == nil {
		return location{}, false, nil
	}
	fileName := filepath.Base(file)
	if hit.New.Version == "" {
		dir := resolveDir(filepath.Dir(file), hit.New.Path)
		via := fmt.Sprintf("%s replace %s => %s", fileName, imp.Module, hit.New.Path)
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			return location{}, true, fmt.Errorf("%s imports forge project %q, which %s; that directory does not exist (%s).\n"+
				"  The go build reads the same directory, so check it out there (or drop the replace)",
				imp.DeclaredIn, imp.Name, via, dir)
		}
		if got := moduleAt(dir); got != imp.Module {
			return location{}, true, fmt.Errorf("%s imports forge project %q, which %s; but %s declares module %q, not %q",
				imp.DeclaredIn, imp.Name, via, filepath.Join(dir, "go.mod"), got, imp.Module)
		}
		return location{dir: dir, via: via}, true, nil
	}
	loc, err := g.fromCache(imp, hit.New.Path, hit.New.Version,
		fmt.Sprintf("%s replace %s => %s %s", fileName, imp.Module, hit.New.Path, hit.New.Version))
	return loc, true, err
}

// fromCache is path@version in the module cache, downloaded when it is not
// there yet, and verified against the go.sum hash.
func (g *goModules) fromCache(imp Import, modPath, version, via string) (location, error) {
	escPath, err := module.EscapePath(modPath)
	if err != nil {
		return location{}, fmt.Errorf("%s: forge project %q: %w", imp.DeclaredIn, imp.Name, err)
	}
	escVer, err := module.EscapeVersion(version)
	if err != nil {
		return location{}, fmt.Errorf("%s: forge project %q: %w", imp.DeclaredIn, imp.Name, err)
	}
	cache, err := moduleCacheDir()
	if err != nil {
		return location{}, err
	}
	dir := filepath.Join(cache, escPath+"@"+escVer)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		if dir, err = fetchModule(modPath, version); err != nil {
			return location{}, fmt.Errorf("%s imports forge project %q at %s@%s (%s), which is not in the module cache (%s), and downloading it failed: %w",
				imp.DeclaredIn, imp.Name, modPath, version, via, cache, err)
		}
	}

	want := g.sums[modPath+" "+version]
	if want == "" {
		return location{}, fmt.Errorf("%s imports forge project %q at %s@%s (%s), but go.sum has no hash for it, so the files cannot be verified as the ones the pin names.\n"+
			"  The go build refuses the same module (missing go.sum entry). Record it: go mod download %s",
			imp.DeclaredIn, imp.Name, modPath, version, via, modPath)
	}
	ziphash := filepath.Join(cache, "cache", "download", escPath, "@v", escVer+".ziphash")
	got, err := os.ReadFile(ziphash)
	if err != nil {
		return location{}, fmt.Errorf("forge project %q: %s@%s is in the module cache without its hash (%s): %w.\n"+
			"  The cache entry is incomplete; remove it (go clean -modcache) and render again", imp.Name, modPath, version, ziphash, err)
	}
	if strings.TrimSpace(string(got)) != want {
		return location{}, fmt.Errorf("refusing to import forge project %q: the module cache's %s@%s does not match go.sum.\n"+
			"  go.sum:       %s\n  module cache: %s (%s)\n"+
			"  The go command would refuse it too (SECURITY ERROR). Fix the cache (go clean -modcache) or go.sum",
			imp.Name, modPath, version, want, strings.TrimSpace(string(got)), ziphash)
	}
	return location{dir: dir, version: version, via: via}, nil
}

// moduleCacheDir and fetchModule are the two places resolution reaches the
// go toolchain. Package variables so unit tests can stand a fake module cache
// up in a temp dir without running `go`; the e2e corpus covers the real ones.
var (
	moduleCacheDir = modCache
	fetchModule    = download
)

var (
	modCacheMu  sync.Mutex
	modCacheFor = map[string]string{}
)

// modCache is the go command's module cache directory. `go env GOMODCACHE`
// when a toolchain is on PATH — it honours `go env -w` — otherwise derived
// the way the go command derives it.
func modCache() (string, error) {
	key := os.Getenv("GOMODCACHE") + "\x00" + os.Getenv("GOPATH") + "\x00" + os.Getenv("GOENV")
	modCacheMu.Lock()
	defer modCacheMu.Unlock()
	if dir, ok := modCacheFor[key]; ok {
		return dir, nil
	}
	dir := ""
	// From a neutral directory: inside a module whose go.mod names a newer
	// toolchain, even `go env` would switch (and maybe download) toolchains.
	cmd := exec.Command("go", "env", "GOMODCACHE")
	cmd.Dir = os.TempDir()
	cmd.Env = goexec.Env()
	if out, err := cmd.Output(); err == nil {
		dir = strings.TrimSpace(string(out))
	}
	if dir == "" {
		dir = strings.TrimSpace(os.Getenv("GOMODCACHE"))
	}
	if dir == "" {
		gopath := strings.TrimSpace(os.Getenv("GOPATH"))
		if gopath != "" {
			gopath = filepath.SplitList(gopath)[0]
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("locate the Go module cache: no GOMODCACHE, no GOPATH, and no home directory: %w", err)
			}
			gopath = filepath.Join(home, "go")
		}
		dir = filepath.Join(gopath, "pkg", "mod")
	}
	modCacheFor[key] = dir
	return dir, nil
}

// download fetches path@version into the module cache with the go command
// and returns its directory.
//
// It runs OUTSIDE any module (a scratch directory, GOWORK=off, no -mod flag):
// a render must never edit the project's go.mod or go.sum, and the hash is
// checked against go.sum by the caller rather than recorded into it. The
// proxy, GOPRIVATE, GONOSUMDB and credentials are the environment's, exactly
// as the project's own `go mod download` would use them.
func download(modPath, version string) (string, error) {
	if _, err := exec.LookPath("go"); err != nil {
		return "", fmt.Errorf("no go toolchain on PATH to download it with; run `go mod download %s` where one is, or put the module cache in place", modPath)
	}
	scratch, err := os.MkdirTemp("", "forge-kcl-import-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	cmd := exec.Command("go", "mod", "download", "-json", modPath+"@"+version)
	cmd.Dir = scratch
	cmd.Env = downloadEnv(goexec.Env())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, runErr := cmd.Output()
	var res struct {
		Dir   string
		Error string
	}
	if jerr := json.Unmarshal(out, &res); jerr != nil && runErr == nil {
		return "", fmt.Errorf("go mod download %s@%s: unreadable output: %w", modPath, version, jerr)
	}
	switch {
	case res.Error != "":
		return "", errors.New(res.Error)
	case runErr != nil:
		return "", fmt.Errorf("go mod download %s@%s: %w: %s", modPath, version, runErr, strings.TrimSpace(stderr.String()))
	case res.Dir == "":
		return "", fmt.Errorf("go mod download %s@%s reported no directory", modPath, version)
	}
	return res.Dir, nil
}

// downloadEnv is env (goexec.Env: -mod=mod already scrubbed) with GOWORK=off
// and every remaining -mod flag dropped from GOFLAGS: outside a module, the go
// command rejects -mod. Nothing is built, so -trimpath is irrelevant here.
func downloadEnv(env []string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		key, val, _ := strings.Cut(kv, "=")
		switch {
		case key == "GOWORK":
			continue
		case key == "GOFLAGS":
			var kept []string
			for _, f := range strings.Fields(val) {
				if strings.HasPrefix(strings.TrimLeft(f, "-"), "mod=") {
					continue
				}
				kept = append(kept, f)
			}
			if len(kept) > 0 {
				out = append(out, "GOFLAGS="+strings.Join(kept, " "))
			}
			continue
		}
		out = append(out, kv)
	}
	return append(out, "GOWORK=off")
}

// readSums adds the module zip hashes in a go.sum file to sums. A missing
// file adds nothing.
func readSums(path string, sums map[string]string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 3 || strings.HasSuffix(f[1], "/go.mod") {
			continue
		}
		sums[f[0]+" "+f[1]] = f[2]
	}
}

func resolveDir(base, p string) string {
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p)
}

// moduleAt is the module path the go.mod in dir declares ("" for none).
func moduleAt(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	return modfile.ModulePath(data)
}
