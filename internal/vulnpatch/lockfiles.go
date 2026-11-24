package vulnpatch

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
	"golang.org/x/mod/modfile"
)

// The manifests the patcher reads. Each knows which packages (and versions)
// it pins, so a finding can be tied to the files a bump must change.

// goModule is one go.mod: the module and what it requires.
type goModule struct {
	rel     string            // go.mod, relative to the tree
	dir     string            // its directory, absolute
	path    string            // the module path
	require map[string]string // module path -> required version
}

func readGoMod(root, abs string) (*goModule, error) {
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	f, err := modfile.ParseLax(abs, data, nil)
	if err != nil {
		return nil, err
	}
	m := &goModule{rel: relTo(root, abs), dir: filepath.Dir(abs), require: map[string]string{}}
	if f.Module != nil {
		m.path = f.Module.Mod.Path
	}
	for _, r := range f.Require {
		m.require[r.Mod.Path] = r.Mod.Version
	}
	return m, nil
}

// jsLockfile is one npm, pnpm or yarn lockfile and the package.json beside it.
type jsLockfile struct {
	eco      Ecosystem
	rel      string // the lockfile, relative to the tree
	dir      string // its directory, absolute
	berry    bool   // yarn 2+ (`yarn up`), not yarn 1 (`yarn upgrade`)
	versions map[string][]string
	direct   map[string]bool // dependencies the package.json names
	// pkgJSONErr is why the package.json beside the lockfile could not be
	// read unambiguously; nil when it is fine or absent.
	pkgJSONErr error
}

// has reports whether the lockfile pins name at version.
func (l *jsLockfile) has(name, version string) bool {
	for _, v := range l.versions[name] {
		if npmVersion(v) == npmVersion(version) {
			return true
		}
	}
	return false
}

func readJSLockfile(root, abs string) (*jsLockfile, error) {
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	l := &jsLockfile{rel: relTo(root, abs), dir: filepath.Dir(abs), versions: map[string][]string{}}
	switch filepath.Base(abs) {
	case "package-lock.json", "npm-shrinkwrap.json":
		l.eco = EcosystemNPM
		err = parsePackageLock(data, l.versions)
	case "pnpm-lock.yaml":
		l.eco = EcosystemPNPM
		err = parsePNPMLock(data, l.versions)
	case "yarn.lock":
		l.eco = EcosystemYarn
		l.berry, err = parseYarnLock(data, l.versions)
	default:
		return nil, fmt.Errorf("%s is not a lockfile", abs)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", l.rel, err)
	}
	for name, vs := range l.versions {
		l.versions[name] = uniqueSorted(vs)
	}
	l.direct, l.pkgJSONErr = readDirectDeps(filepath.Join(l.dir, "package.json"))
	return l, nil
}

// readDirectDeps is every dependency a package.json names. A missing
// package.json names none: each bump is then an override (which needs one,
// so the planner skips it). A package.json that is not exactly one JSON
// object without duplicate keys, at the top level and in each dependency
// block, is an error: which dependencies are direct is then not known, and
// the planner touches nothing beside it.
func readDirectDeps(path string) (map[string]bool, error) {
	out := map[string]bool{}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	root, err := parseObject(data)
	if err != nil {
		return out, err
	}
	for _, block := range []string{"dependencies", "devDependencies", "optionalDependencies"} {
		raw, ok := root.vals[block]
		if !ok {
			continue
		}
		deps, err := parseObject(raw)
		if err != nil {
			return out, fmt.Errorf("%q: %w", block, err)
		}
		for _, name := range deps.keys {
			out[name] = true
		}
	}
	return out, nil
}

// parsePackageLock reads npm's lockfile: v2/v3's "packages" (keyed by
// install path, node_modules/<name>), and v1's nested "dependencies".
func parsePackageLock(data []byte, into map[string][]string) error {
	type depV1 struct {
		Version      string           `json:"version"`
		Dependencies map[string]depV1 `json:"dependencies"`
	}
	var lock struct {
		Packages map[string]struct {
			Version string `json:"version"`
			Link    bool   `json:"link"`
		} `json:"packages"`
		Dependencies map[string]depV1 `json:"dependencies"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return err
	}
	for path, p := range lock.Packages {
		i := strings.LastIndex(path, "node_modules/")
		if i < 0 || p.Link || p.Version == "" {
			continue
		}
		into[path[i+len("node_modules/"):]] = append(into[path[i+len("node_modules/"):]], p.Version)
	}
	var walk func(map[string]depV1)
	walk = func(deps map[string]depV1) {
		for name, d := range deps {
			if d.Version != "" {
				into[name] = append(into[name], d.Version)
			}
			walk(d.Dependencies)
		}
	}
	if len(lock.Packages) == 0 {
		walk(lock.Dependencies)
	}
	return nil
}

// parsePNPMLock reads pnpm-lock.yaml's package keys, in every format pnpm
// has written: v5 "/name/1.2.3_peer", v6 "/name@1.2.3(peer)", v9
// "name@1.2.3(peer)" (under packages and snapshots).
func parsePNPMLock(data []byte, into map[string][]string) error {
	var lock struct {
		Packages  map[string]yaml.Node `yaml:"packages"`
		Snapshots map[string]yaml.Node `yaml:"snapshots"`
	}
	if err := yaml.Unmarshal(data, &lock); err != nil {
		return err
	}
	for _, keys := range []map[string]yaml.Node{lock.Packages, lock.Snapshots} {
		for k := range keys {
			if name, v, ok := parsePNPMKey(k); ok {
				into[name] = append(into[name], v)
			}
		}
	}
	return nil
}

func parsePNPMKey(k string) (name, version string, ok bool) {
	k = strings.TrimPrefix(k, "/")
	if i := strings.IndexByte(k, '('); i >= 0 {
		k = k[:i]
	}
	if i := strings.LastIndexByte(k, '@'); i > 0 {
		name, version = k[:i], k[i+1:]
	} else if i := strings.LastIndexByte(k, '/'); i > 0 {
		name, version = k[:i], k[i+1:] // v5
		if j := strings.IndexByte(version, '_'); j >= 0 {
			version = version[:j]
		}
	}
	if name == "" || canonical(version) == "" {
		return "", "", false
	}
	return name, version, true
}

// parseYarnLock reads yarn.lock, yarn 1's format and yarn 2+'s (YAML with
// a __metadata entry), and reports which.
func parseYarnLock(data []byte, into map[string][]string) (berry bool, err error) {
	berry = bytes.Contains(data, []byte("__metadata:"))
	var names []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if line[0] != ' ' && strings.HasSuffix(trimmed, ":") {
			names = names[:0]
			for _, spec := range strings.Split(strings.TrimSuffix(trimmed, ":"), ",") {
				spec = strings.Trim(strings.TrimSpace(spec), `"`)
				if i := strings.LastIndexByte(spec, '@'); i > 0 {
					names = appendUnique(names, spec[:i])
				}
			}
			continue
		}
		if !strings.HasPrefix(trimmed, "version") || len(names) == 0 {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(trimmed, "version"))
		v = strings.Trim(strings.TrimSpace(strings.TrimPrefix(v, ":")), `"`)
		for _, n := range names {
			into[n] = append(into[n], v)
		}
		names = names[:0]
	}
	return berry, sc.Err()
}

func relTo(root, abs string) string {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}

func appendUnique(xs []string, x string) []string {
	for _, y := range xs {
		if y == x {
			return xs
		}
	}
	return append(xs, x)
}

func uniqueSorted(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return compareVersions(out[i], out[j]) < 0 })
	return out
}
