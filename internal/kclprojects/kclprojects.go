// Package kclprojects lets one forge project's KCL import another forge
// project's KCL package, at the version the importing project's Go module
// already pins.
//
// # The declaration
//
// The importing project names the other project in its KCL module's kcl.mod,
// in a table forge owns:
//
//	# deploy/kcl/kcl.mod
//	[forge_projects]
//	reliant = { module = "github.com/reliant-labs/reliant" }
//
// and its KCL then says `import reliant.workloads as rwl`. `subdir` names the
// KCL package root inside the module and defaults to `deploy/kcl`, where every
// forge project keeps it. There is no version field, on purpose: see below.
//
// # Where the version comes from: go.mod, and nowhere else
//
// A project that imports another project's KCL almost always runs that
// project's code too — control-plane deploys reliant's binaries, and pins them
// with `require github.com/reliant-labs/reliant <version>` in go.mod. A second
// pin for the KCL (a kpm git dependency with its own ref, a vendored copy with
// its own provenance) is a second fact that can disagree with the first, and
// the failure it permits is the one this package exists to remove: the
// deployment describes one version of the workloads while the image runs
// another. So the KCL comes from exactly where the Go build's copy of that
// module comes from, following the go command's own rules:
//
//   - a go.work `use` of the module wins — the workspace builds that directory;
//   - then a go.work `replace`, then a go.mod `replace`: a directory target is
//     read in place (a sibling checkout, in dev), a module target is read from
//     the module cache at the replacement version;
//   - otherwise the module cache, at the version go.mod requires — downloaded
//     with `go mod download` when it is not there yet, and verified against
//     the go.sum hash, so the bytes rendered are the bytes the pin names.
//
// A module the go.mod does not require is refused: there is no pin to
// resolve. (A project whose Go code does not depend on the other project has
// no Go pin to share, and declares a kpm git dependency in [dependencies]
// instead — that IS its one pin.)
//
// # Why a table of its own, and not an entry in [dependencies]
//
// kpm owns [dependencies]. It parses every entry there as a git, OCI, local or
// registry source, and an entry in a shape it does not know becomes a
// dependency with no source at all, which it then tries to fetch — so every
// render, and the `kcl` CLI and the KCL language server reading the same file,
// would fail on it. kpm ignores a top-level table it does not know, so
// [forge_projects] is invisible to it, and forge supplies each package to the
// evaluation as an external package (`-E <name>=<dir>`), exactly as it supplies
// `import forge` itself (internal/kclvendor). A [dependencies] entry spelled
// with `forge_project =` or `module =` is refused with the move to make.
//
// # Version skew is refused
//
// An imported project's `import forge` resolves to the IMPORTING render's
// forge module — there is one `forge` package per evaluation, and it is the
// rendering binary's. So the imported KCL is only evaluated against the schema
// it was written for when both projects pin the same forge. When their
// forge.yaml `forge_version` pins differ, Resolve refuses and names both pins:
// otherwise a field one side's schema added or removed surfaces as a KCL error
// at the imported project's line numbers, which reads as a bug in that
// project rather than as a version problem. FORGE_ALLOW_VERSION_SKEW
// (internal/forgecompat) overrides, as it does for the binary-vs-pin skew.
//
// # Names
//
// An external package's name is global to the evaluation, so a name that
// would shadow something is refused: `forge`, `kcl_plugin`, a KCL system
// module, a file or directory at the importing package's root, or a kpm
// dependency of the same name. Inside the imported package, its own absolute
// imports (`import config_gen`) resolve against ITS root, not the importer's,
// so two projects that both keep a `config_gen.k` do not collide.
//
// The imported project's own [forge_projects] are resolved too, against the
// ROOT project's go.mod — one build, one version of each module. An imported
// package's kpm [dependencies] are not resolved (forge evaluates the package in
// place), so a non-empty one is refused rather than left to fail as an
// unresolvable import.
package kclprojects

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/forgecompat"
)

// TableName is the kcl.mod table that declares forge-project imports.
const TableName = "forge_projects"

// DefaultSubdir is where a forge project keeps its KCL package root.
const DefaultSubdir = "deploy/kcl"

// Import is one [forge_projects] entry, as declared.
type Import struct {
	// Name is the KCL package name: `import <Name>.workloads`.
	Name string `json:"name"`
	// Module is the Go module path the project is published as.
	Module string `json:"module"`
	// Subdir is the KCL package root inside the module (slash-separated).
	Subdir string `json:"subdir"`
	// DeclaredIn is the kcl.mod that declares it.
	DeclaredIn string `json:"declared_in"`
}

// Resolved is an Import located on disk.
type Resolved struct {
	Import
	// Version is the module version the KCL is read at. Empty when the
	// module resolves to a directory (a go.work `use`, a directory
	// `replace`): the go command builds whatever that directory holds, and
	// so does the render.
	Version string `json:"version,omitempty"`
	// Via names the declaration that decided the location, e.g.
	// "go.mod require github.com/x/y v1.2.3" or "go.mod replace => ../y".
	Via string `json:"via"`
	// ModuleDir is the module's root directory.
	ModuleDir string `json:"module_dir"`
	// Dir is the KCL package root: ModuleDir/Subdir.
	Dir string `json:"dir"`
	// ForgePin is the imported project's forge.yaml forge_version.
	ForgePin string `json:"forge_pin,omitempty"`
}

// FindKCLRoot returns the directory of the kcl.mod that owns path (a .k file
// or a package directory), walking up. "" when there is none.
func FindKCLRoot(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	if st, err := os.Stat(abs); err == nil && !st.IsDir() {
		abs = filepath.Dir(abs)
	}
	return findUp(abs, "kcl.mod")
}

// ExternalPkgArgs is the `name=dir` external-package binding for each
// resolved import, sorted by name.
func ExternalPkgArgs(rs []Resolved) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Name+"="+r.Dir)
	}
	sort.Strings(out)
	return out
}

// Resolve locates every forge project the KCL module rooted at kclRoot
// imports, directly or through another imported project. A kclRoot with no
// kcl.mod, or a kcl.mod with no [forge_projects], resolves to nothing and
// costs one file read — the common case for every project that imports none.
func Resolve(kclRoot string) ([]Resolved, error) {
	if kclRoot == "" {
		return nil, nil
	}
	rootMod := filepath.Join(kclRoot, "kcl.mod")
	root, err := readKCLMod(rootMod)
	if err != nil || len(root.imports) == 0 {
		return nil, err
	}

	goModDir := findUp(kclRoot, "go.mod")
	if goModDir == "" {
		return nil, fmt.Errorf("%s imports forge projects (%s), but there is no go.mod at or above %s.\n"+
			"  A [%s] import is read at the version go.mod pins the module to; without a go.mod there is no pin",
			rootMod, importNames(root.imports), kclRoot, TableName)
	}
	g, err := loadGoModules(goModDir)
	if err != nil {
		return nil, err
	}
	importerPin := forgecompat.PinnedForgeVersion(kclRoot)

	res := &resolver{g: g, kclRoot: kclRoot, rootDeps: root.kpmDeps, importerPin: importerPin, seen: map[string]Resolved{}}
	queue := append([]Import(nil), root.imports...)
	var out []Resolved
	for len(queue) > 0 {
		imp := queue[0]
		queue = queue[1:]
		if prev, ok := res.seen[imp.Name]; ok {
			if prev.Module != imp.Module || prev.Subdir != imp.Subdir {
				return nil, fmt.Errorf("two forge-project imports are named %q: %s (%s) in %s, and %s (%s) in %s.\n"+
					"  A KCL package name is global to the evaluation; rename one of them",
					imp.Name, prev.Module, prev.Subdir, prev.DeclaredIn, imp.Module, imp.Subdir, imp.DeclaredIn)
			}
			continue
		}
		r, nested, err := res.resolve(imp)
		if err != nil {
			return nil, err
		}
		res.seen[imp.Name] = r
		out = append(out, r)
		queue = append(queue, nested...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

type resolver struct {
	g           *goModules
	kclRoot     string
	rootDeps    []string
	importerPin string
	seen        map[string]Resolved
}

func (r *resolver) resolve(imp Import) (Resolved, []Import, error) {
	if err := r.checkName(imp); err != nil {
		return Resolved{}, nil, err
	}
	loc, err := r.g.locate(imp)
	if err != nil {
		return Resolved{}, nil, err
	}
	out := Resolved{Import: imp, Version: loc.version, Via: loc.via, ModuleDir: loc.dir}
	out.Dir = filepath.Join(loc.dir, filepath.FromSlash(imp.Subdir))
	at := describeAt(out)

	if _, err := os.Stat(filepath.Join(out.Dir, "kcl.mod")); err != nil {
		return Resolved{}, nil, fmt.Errorf("%s imports forge project %q from %s, which has no KCL package at %s (no %s).\n"+
			"  Set `subdir` in the [%s] entry to the directory holding that project's kcl.mod",
			imp.DeclaredIn, imp.Name, at, imp.Subdir, filepath.Join(out.Dir, "kcl.mod"), TableName)
	}
	pin, isForge, err := readForgePin(loc.dir)
	if err != nil {
		return Resolved{}, nil, err
	}
	if !isForge {
		return Resolved{}, nil, fmt.Errorf("%s imports %q from %s, which is not a forge project (no forge.yaml at the module root %s).\n"+
			"  [%s] imports another forge project's KCL; for any other KCL package, declare a kpm dependency in [dependencies]",
			imp.DeclaredIn, imp.Name, at, loc.dir, TableName)
	}
	out.ForgePin = pin
	if err := r.checkForgeSkew(out, at); err != nil {
		return Resolved{}, nil, err
	}

	nestedMod := filepath.Join(out.Dir, "kcl.mod")
	nested, err := readKCLMod(nestedMod)
	if err != nil {
		return Resolved{}, nil, err
	}
	if len(nested.kpmDeps) > 0 {
		return Resolved{}, nil, fmt.Errorf("forge project %q (%s) declares kpm dependencies in %s: %s.\n"+
			"  forge evaluates an imported project's KCL package in place and resolves only its [%s], so those imports would not resolve.\n"+
			"  Declare them in the importing project's own kcl.mod instead",
			imp.Name, at, nestedMod, strings.Join(nested.kpmDeps, ", "), TableName)
	}
	return out, nested.imports, nil
}

// checkName refuses a package name that would shadow something else the
// evaluation can import.
func (r *resolver) checkName(imp Import) error {
	if imp.Module == r.g.module {
		return fmt.Errorf("%s imports forge project %q as %s — the importing project itself. Import its packages directly",
			imp.DeclaredIn, imp.Name, imp.Module)
	}
	if reservedNames[imp.Name] {
		return fmt.Errorf("%s: forge-project import %q shadows the built-in KCL package of that name; pick another name",
			imp.DeclaredIn, imp.Name)
	}
	for _, dep := range r.rootDeps {
		if dep == imp.Name {
			return fmt.Errorf("%s: forge-project import %q has the same name as a kpm dependency in %s [dependencies]; rename one of them",
				imp.DeclaredIn, imp.Name, filepath.Join(r.kclRoot, "kcl.mod"))
		}
	}
	for _, local := range []string{imp.Name + ".k", imp.Name} {
		if _, err := os.Stat(filepath.Join(r.kclRoot, local)); err == nil {
			return fmt.Errorf("%s: forge-project import %q has the same name as %s at the root of the importing KCL package, so `import %s` would be ambiguous; rename one of them",
				imp.DeclaredIn, imp.Name, filepath.Join(r.kclRoot, local), imp.Name)
		}
	}
	return nil
}

// checkForgeSkew refuses an imported project that pins a different forge
// than the importer: its `import forge` would resolve to the importer's
// module, not the one it was written against.
func (r *resolver) checkForgeSkew(res Resolved, at string) error {
	if res.ForgePin == "" || r.importerPin == "" || res.ForgePin == r.importerPin {
		return nil
	}
	if forgecompat.SkewOverridden() {
		fmt.Fprintf(os.Stderr, "warning: forge project %q (%s) pins forge %s, this project pins %s; rendering anyway (%s is set)\n",
			res.Name, at, res.ForgePin, r.importerPin, forgecompat.SkewOverrideEnv)
		return nil
	}
	return fmt.Errorf("refusing to import forge project %q: version skew.\n"+
		"  %s pins forge %s (its forge.yaml forge_version)\n"+
		"  this project pins forge     %s\n"+
		"  The imported KCL's `import forge` resolves to THIS render's forge KCL module, so it would be evaluated against\n"+
		"  a schema it was not written for, and a mismatch would surface as a KCL error on its own line numbers.\n"+
		"  Make the pins agree, the imported project FIRST — upgrading forge moves the dependency before its consumer:\n"+
		"    1. in %s itself, pin forge %s (forge.yaml forge_version and go.mod) and publish that commit;\n"+
		"    2. here, go get %s@<that commit or tag>, and render again.\n"+
		"  Or keep %s as it is and pin this project's forge to %s. (%s=1 renders anyway.)",
		res.Name, at, res.ForgePin, r.importerPin, res.Module, r.importerPin, res.Module, res.Module, res.ForgePin, forgecompat.SkewOverrideEnv)
}

func describeAt(r Resolved) string {
	if r.Version != "" {
		return fmt.Sprintf("%s@%s (%s)", r.Module, r.Version, r.Via)
	}
	return fmt.Sprintf("%s (%s)", r.Module, r.Via)
}

// reservedNames are package names an import may not take: forge's own
// module, the plugin namespace, and KCL's system modules.
var reservedNames = map[string]bool{
	"forge": true, "kcl_plugin": true,
	"base64": true, "collection": true, "crypto": true, "datetime": true, "file": true,
	"json": true, "manifests": true, "math": true, "net": true, "regex": true,
	"runtime": true, "template": true, "units": true, "uuid": true, "yaml": true,
}

var identRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// kclMod is the part of a kcl.mod this package reads.
type kclMod struct {
	imports []Import
	// kpmDeps are the [dependencies] names, sorted.
	kpmDeps []string
}

// readKCLMod reads path's [forge_projects] and [dependencies]. A missing
// file is an empty module.
func readKCLMod(modPath string) (kclMod, error) {
	data, err := os.ReadFile(modPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return kclMod{}, nil
		}
		return kclMod{}, fmt.Errorf("read %s: %w", modPath, err)
	}
	var raw struct {
		Dependencies  map[string]any `toml:"dependencies"`
		ForgeProjects map[string]any `toml:"forge_projects"`
	}
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return kclMod{}, fmt.Errorf("parse %s: %w", modPath, err)
	}
	var m kclMod
	for name, v := range raw.Dependencies {
		if t, ok := v.(map[string]any); ok {
			_, fp := t["forge_project"]
			_, mod := t["module"]
			if fp || mod {
				return kclMod{}, fmt.Errorf("%s declares %q under [dependencies] as a forge project.\n"+
					"  [dependencies] belongs to kpm, which cannot resolve it. Move it to its own table:\n\n"+
					"    [%s]\n    %s = { module = \"<go module path>\" }",
					modPath, name, TableName, name)
			}
		}
		m.kpmDeps = append(m.kpmDeps, name)
	}
	sort.Strings(m.kpmDeps)
	for name, v := range raw.ForgeProjects {
		imp, err := parseImport(modPath, name, v)
		if err != nil {
			return kclMod{}, err
		}
		m.imports = append(m.imports, imp)
	}
	sort.Slice(m.imports, func(i, j int) bool { return m.imports[i].Name < m.imports[j].Name })
	return m, nil
}

func parseImport(modPath, name string, v any) (Import, error) {
	bad := func(format string, args ...any) (Import, error) {
		return Import{}, fmt.Errorf("%s [%s] %s: %s", modPath, TableName, name, fmt.Sprintf(format, args...))
	}
	if !identRE.MatchString(name) {
		return bad("not a valid KCL package name (letters, digits and underscores, not starting with a digit)")
	}
	t, ok := v.(map[string]any)
	if !ok {
		return bad(`expected a table, e.g. %s = { module = "github.com/acme/api" }`, name)
	}
	imp := Import{Name: name, Subdir: DefaultSubdir, DeclaredIn: modPath}
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s, isStr := t[k].(string)
		switch k {
		case "module":
			if !isStr || strings.TrimSpace(s) == "" {
				return bad("module must be a non-empty string (the Go module path)")
			}
			imp.Module = strings.TrimSpace(s)
		case "subdir":
			clean := pathClean(s)
			if !isStr || clean == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) || filepath.IsAbs(filepath.FromSlash(clean)) {
				return bad("subdir must be a relative path inside the module, e.g. %q", DefaultSubdir)
			}
			imp.Subdir = clean
		case "version", "ref", "commit", "tag", "branch":
			return bad("%q is not allowed: the version comes from go.mod's require of the module (and its replace, if any), so the KCL can never be read at a different version than the Go build uses. Change the version with `go get`", k)
		default:
			return bad("unknown key %q (known: module, subdir)", k)
		}
	}
	if imp.Module == "" {
		return bad("module is required (the Go module path, as go.mod requires it)")
	}
	return imp, nil
}

// pathClean is s as a cleaned slash path, "" for a blank one.
func pathClean(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, `\`, "/"))
	if s == "" {
		return ""
	}
	return path.Clean(s)
}

func importNames(imps []Import) string {
	names := make([]string, 0, len(imps))
	for _, i := range imps {
		names = append(names, i.Name)
	}
	return strings.Join(names, ", ")
}

// readForgePin reads forge_version from the forge.yaml at exactly dir (no
// walking up: a module cache directory's ancestors are not the project).
// isForge is false when there is no forge.yaml.
func readForgePin(dir string) (pin string, isForge bool, err error) {
	file := filepath.Join(dir, "forge.yaml")
	data, err := os.ReadFile(file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read %s: %w", file, err)
	}
	var meta struct {
		ForgeVersion string `yaml:"forge_version"`
	}
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return "", true, fmt.Errorf("parse %s: %w", file, err)
	}
	return strings.TrimSpace(meta.ForgeVersion), true, nil
}

// findUp returns the nearest directory at or above dir holding name.
func findUp(dir, name string) string {
	for cur := dir; ; {
		if st, err := os.Stat(filepath.Join(cur, name)); err == nil && !st.IsDir() {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
}
