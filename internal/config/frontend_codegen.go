package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// frontend_codegen.go — the facts about a frontend that only its own
// directory can state: whether forge scaffolded it, and the per-frontend
// codegen knobs in its package.json.
//
// # Why package.json
//
// A frontend discovered on disk (frontends/<name>/) has no declaration
// anywhere: forge.yaml has no `frontends:` key, and a KCL `forge.Frontend`
// is deploy topology a project may not have — reliant has no deploy/kcl at
// all. The only per-frontend knob forge honoured, `routes`, was therefore
// unsayable for every frontend without a deploy env. A codegen knob is a
// property of the frontend's CODE, not of where it deploys, so it belongs
// beside the code, in the one file every frontend already has:
//
//	"forge": {
//	  "routes": ["none"],                       // CRUD-page allowlist, like KCL routes
//	  "hooks": ["ChatService", "ProjectService"], // services that get React Query hooks
//	  "components_dir": "src/components/forge-ui", // where forge's component library lives
//	  "imported_hooks": [{                      // hooks for another repo's proto module
//	    "proto_root": "../../proto-vendor/controlplane",
//	    "gen_dir": "controlplane",
//	    "services": ["controlplane.v1.BillingService"]
//	  }]
//	}
//
// npm ignores unknown top-level keys, and "forge" is the name tools already
// use for their own block (prettier, eslintConfig, jest), so the file stays
// a valid manifest for every package manager.
//
// # Imported proto modules
//
// `hooks` names services of the project's own proto/ tree. A frontend that
// also calls services whose contract lives in ANOTHER repository — vendored
// into this one as a buf module of its own, deliberately outside the
// project's buf.yaml so the project's lint and breaking checks never judge
// it — declares one `imported_hooks` entry per such module:
//
//   - proto_root: the buf module's directory (it holds a buf.yaml), relative
//     to the frontend's directory and inside the project root.
//   - gen_dir: where the app's own buf template writes that module's TS
//     messages, under the frontend's src/gen/. The hooks import
//     `@/gen/<gen_dir>/<proto path>_pb`; forge does not generate those
//     messages.
//   - services: the fully-qualified services that get hooks. Required: an
//     imported module may declare dozens the app never calls.
//
// The hooks land in src/hooks/<gen_dir>/, outside the src/hooks/index.ts
// barrel, and their query keys are rooted at the fully-qualified service name,
// so a project service of the same short name collides with neither. The
// hooks call `connectClient(Service)` like every other hook: routing an
// imported service to the origin that serves it is the app's own
// src/lib/connect.ts.
//
// # Adopted frontends
//
// forge generate used to assume every frontend it generates into is one it
// scaffolded. For a frontend that already existed — an app moved under
// frontends/ to adopt forge (reliant's web), or one a KCL `path` points at —
// that assumption installed forge's 35-component starter set beside the
// app's own components, scaffolded nav and CRUD pages into it, and rewrote
// its package.json. An adopted frontend gets forge's Tier-1 codegen (hooks,
// mocks, apiurl/otel modules, typed config) and the owned seams that code
// imports, and none of the starter set. See FrontendConfig.Adopted.

// PackageForgeKey is the package.json key holding a frontend's forge block.
const PackageForgeKey = "forge"

// DefaultComponentsDir is where forge's component library lives in a
// frontend that does not say otherwise, relative to the frontend directory.
const DefaultComponentsDir = "src/components/ui"

// frontendPackageForge is the package.json "forge" block. Decoded strictly:
// a misspelt knob ("route") silently ignored would produce exactly the
// frontend its author asked forge not to produce, with nothing saying why.
type frontendPackageForge struct {
	Routes        []string               `json:"routes"`
	Hooks         *[]string              `json:"hooks"`
	ComponentsDir string                 `json:"components_dir"`
	ImportedHooks []packageImportedHooks `json:"imported_hooks"`
}

// packageImportedHooks is one `imported_hooks` entry as package.json states
// it. resolveImportedHooks validates it into an ImportedHooks.
type packageImportedHooks struct {
	ProtoRoot string   `json:"proto_root"`
	GenDir    string   `json:"gen_dir"`
	Services  []string `json:"services"`
}

// ImportedHooks is one package.json `forge.imported_hooks` entry, validated:
// the services of a buf module outside the project's own proto/ tree that get
// generated React Query hooks in this frontend.
type ImportedHooks struct {
	// ProtoRoot is the buf module's directory, relative to the project root
	// (slash-separated, cleaned). package.json states it relative to the
	// frontend.
	ProtoRoot string
	// GenDir is where the app generates the module's TS messages, relative
	// to the frontend's src/gen/ (cleaned). It also names the hooks'
	// directory, src/hooks/<GenDir>/.
	GenDir string
	// Services are the fully-qualified service names that get hooks.
	Services []string
}

// WantsService reports whether the entry allowlists the fully-qualified
// service name.
func (h ImportedHooks) WantsService(fullName string) bool {
	return slices.Contains(h.Services, fullName)
}

// UnknownServices returns the allowlisted names known does not contain,
// sorted.
func (h ImportedHooks) UnknownServices(known map[string]bool) []string {
	var out []string
	for _, s := range h.Services {
		if !known[s] {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// readFrontendPackageForge reads the forge block from feDir/package.json.
// A missing manifest or a manifest with no block is not an error.
func readFrontendPackageForge(feDir string) (frontendPackageForge, bool, error) {
	var block frontendPackageForge
	data, err := os.ReadFile(filepath.Join(feDir, "package.json"))
	if err != nil {
		return block, false, nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		// An unparseable manifest is npm's problem to report, and every
		// install will. Codegen proceeds on defaults.
		return block, false, nil
	}
	raw, ok := top[PackageForgeKey]
	if !ok {
		return block, false, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&block); err != nil {
		return block, true, fmt.Errorf("package.json %q block: %w (known keys: routes, hooks, components_dir, imported_hooks; "+
			"an imported_hooks entry knows proto_root, gen_dir, services)", PackageForgeKey, err)
	}
	if block.ComponentsDir != "" {
		clean, err := cleanComponentsDir(block.ComponentsDir)
		if err != nil {
			return block, true, fmt.Errorf("package.json %s.components_dir: %w", PackageForgeKey, err)
		}
		block.ComponentsDir = clean
	}
	return block, true, nil
}

// cleanComponentsDir validates a components_dir: a relative path that stays
// inside the frontend directory.
func cleanComponentsDir(dir string) (string, error) {
	d := path.Clean(filepath.ToSlash(strings.TrimSpace(dir)))
	if d == "." || d == "" {
		return "", fmt.Errorf("%q names the frontend directory itself; name the directory components go in (default %s)", dir, DefaultComponentsDir)
	}
	if path.IsAbs(d) || filepath.IsAbs(dir) || d == ".." || strings.HasPrefix(d, "../") {
		return "", fmt.Errorf("%q must be a path inside the frontend directory, relative to it", dir)
	}
	return d, nil
}

// resolveImportedHooks validates the package.json imported_hooks entries of
// the frontend at feRel (project-relative, "." for a root frontend) into
// ImportedHooks, returning one problem per thing it cannot honour. An entry
// with a problem is dropped; generate refuses to run while any is reported.
func resolveImportedHooks(projectDir, feRel string, entries []packageImportedHooks) ([]ImportedHooks, []string) {
	var out []ImportedHooks
	var problems []string
	genDirs := map[string]int{}
	for i, e := range entries {
		where := fmt.Sprintf("package.json %s.imported_hooks[%d]", PackageForgeKey, i)
		var bad []string
		protoRoot, err := resolveImportedProtoRoot(projectDir, feRel, e.ProtoRoot)
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s.proto_root: %v", where, err))
		}
		genDir, err := cleanImportedGenDir(e.GenDir)
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s.gen_dir: %v", where, err))
		} else if prev, dup := genDirs[genDir]; dup {
			bad = append(bad, fmt.Sprintf("%s.gen_dir: %q is also imported_hooks[%d]'s; each module needs its own (it names the hooks directory src/hooks/%s/)", where, genDir, prev, genDir))
		} else {
			genDirs[genDir] = i
		}
		services, err := cleanImportedServices(e.Services)
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s.services: %v", where, err))
		}
		if len(bad) > 0 {
			problems = append(problems, bad...)
			continue
		}
		out = append(out, ImportedHooks{ProtoRoot: protoRoot, GenDir: genDir, Services: services})
	}
	return out, problems
}

// resolveImportedProtoRoot resolves an imported_hooks proto_root, stated
// relative to the frontend at feRel, to a project-relative directory that
// exists, lies inside the project root (after symlinks: ImportedProtoRootDir)
// and holds a buf.yaml.
func resolveImportedProtoRoot(projectDir, feRel, protoRoot string) (string, error) {
	rel, err := importedProtoRootPath(feRel, protoRoot)
	if err != nil {
		return "", err
	}
	abs, err := ImportedProtoRootDir(projectDir, rel)
	if errors.Is(err, ErrImportedProtoRootEscapes) {
		return "", fmt.Errorf("%q (%s): %w", protoRoot, rel, err)
	}
	if fi, statErr := os.Stat(abs); err != nil || statErr != nil || !fi.IsDir() {
		return "", fmt.Errorf("%q (%s) is not a directory", protoRoot, rel)
	}
	if _, err := os.Stat(filepath.Join(abs, "buf.yaml")); err != nil {
		return "", fmt.Errorf("%q (%s) has no buf.yaml: name the buf module's own directory", protoRoot, rel)
	}
	return rel, nil
}

// ErrImportedProtoRootEscapes is ImportedProtoRootDir's verdict on a
// proto_root that is inside the project by its path but resolves, through a
// symlink, to a directory outside it.
var ErrImportedProtoRootEscapes = errors.New("resolves through a symlink to a directory outside the project root; vendor the module inside the project")

// ImportedProtoRootDir is the one containment check for an imported module:
// it resolves the project-relative proto_root rel (as importedProtoRootPath
// returns it) through every symlink and returns the real directory, or
// ErrImportedProtoRootEscapes when that directory is not under the project's
// own real root. Config validation, buf extraction and the generate cache
// each call it, so none of them reads a module the others would refuse. Any
// other error (nothing at rel) is the filesystem's, unwrapped.
func ImportedProtoRootDir(projectDir, rel string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(filepath.Join(projectDir, filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	inside, err := filepath.Rel(realRoot, real)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", ErrImportedProtoRootEscapes
	}
	return real, nil
}

// importedProtoRootPath is resolveImportedProtoRoot's lexical half: the
// project-relative path a proto_root names, without asking the disk whether
// a module is there.
func importedProtoRootPath(feRel, protoRoot string) (string, error) {
	raw := strings.TrimSpace(protoRoot)
	if raw == "" {
		return "", fmt.Errorf("required: the directory of the buf module (its buf.yaml), relative to the frontend")
	}
	if filepath.IsAbs(raw) || path.IsAbs(filepath.ToSlash(raw)) {
		return "", fmt.Errorf("%q must be relative to the frontend directory", protoRoot)
	}
	rel := path.Clean(path.Join(filepath.ToSlash(feRel), filepath.ToSlash(raw)))
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%q resolves outside the project root (%s)", protoRoot, rel)
	}
	return rel, nil
}

// ImportedProtoRoots returns the project-relative proto_root of every
// imported_hooks entry in the package.json of the directory at feRel
// (slash-separated, "." for the project root), whether or not a module is
// there yet. It is what the generate cache fingerprints: an imported module
// usually lives outside the project's buf workspace, often in a gitignored
// vendor directory, so its files are in no listing the cache already reads.
// An unreadable block, or an entry that names no path inside the project,
// contributes nothing: generate reports those, and the package.json itself is
// fingerprinted, so fixing it is a change.
func ImportedProtoRoots(projectDir, feRel string) []string {
	block, found, err := readFrontendPackageForge(filepath.Join(projectDir, filepath.FromSlash(feRel)))
	if !found || err != nil {
		return nil
	}
	var out []string
	for _, e := range block.ImportedHooks {
		rel, err := importedProtoRootPath(feRel, e.ProtoRoot)
		if err != nil || slices.Contains(out, rel) {
			continue
		}
		out = append(out, rel)
	}
	return out
}

// cleanImportedGenDir validates an imported_hooks gen_dir: a relative path
// inside the frontend's src/gen/, naming a directory under it.
func cleanImportedGenDir(dir string) (string, error) {
	d := path.Clean(filepath.ToSlash(strings.TrimSpace(dir)))
	if d == "." || d == "" {
		return "", fmt.Errorf("required: the directory under src/gen/ the app generates this module's TS messages into")
	}
	if path.IsAbs(d) || filepath.IsAbs(dir) || d == ".." || strings.HasPrefix(d, "../") {
		return "", fmt.Errorf("%q must be a path inside src/gen/, relative to it", dir)
	}
	return d, nil
}

// cleanImportedServices validates an imported_hooks services allowlist:
// non-empty, every entry fully qualified. Duplicates are dropped.
func cleanImportedServices(services []string) ([]string, error) {
	if len(services) == 0 {
		return nil, fmt.Errorf("required: the fully-qualified services that get hooks (an imported module may declare many the app never calls)")
	}
	var out []string
	for _, s := range services {
		s = strings.TrimSpace(s)
		if !strings.Contains(s, ".") || strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") {
			return nil, fmt.Errorf("%q is not a fully-qualified service name (<package>.<Service>, e.g. other.v1.FooService)", s)
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out, nil
}

// attachFrontendDiskFacts fills the fields of fe that only its directory can
// supply: Adopted, and the package.json forge block. absFeDir is the
// frontend's directory on disk; feRel the same path relative to projectDir
// (slash-separated, "." for a root frontend).
//
// Routes are taken from package.json only when nothing upstream declared
// them (a KCL declaration is authoritative for every field it states). An
// adopted frontend that declares no routes anywhere gets `none`: CRUD pages
// are starter code, and an app forge did not scaffold has its own routes.
func attachFrontendDiskFacts(projectDir, feRel string, fe *FrontendConfig) {
	absFeDir := filepath.Join(projectDir, filepath.FromSlash(feRel))
	fe.Adopted = !frontendScaffoldedByForge(projectDir, feRel, absFeDir)
	block, found, err := readFrontendPackageForge(absFeDir)
	if err != nil {
		fe.CodegenProblems = append(fe.CodegenProblems, err.Error())
	}
	if found && err == nil {
		if len(fe.Routes) == 0 && len(block.Routes) > 0 {
			fe.Routes = append([]string(nil), block.Routes...)
		}
		if block.Hooks != nil {
			fe.Hooks = append([]string{}, (*block.Hooks)...)
		}
		fe.ComponentsDir = block.ComponentsDir
		imported, problems := resolveImportedHooks(projectDir, feRel, block.ImportedHooks)
		fe.ImportedHooks = imported
		fe.CodegenProblems = append(fe.CodegenProblems, problems...)
	}
	if fe.Adopted && len(fe.Routes) == 0 {
		fe.Routes = []string{RoutesNone}
	}
}

// frontendScaffoldedByForge reports whether forge scaffolded the frontend at
// feRel. Two records say so, and either suffices:
//
//   - the birth ledger (.forge/scaffolded/, committed) records the
//     frontend's package.json. `forge scaffold frontend` and `forge project
//     new --frontend` record every file they write, and package.json is the
//     one a frontend cannot exist without. It is checked by name rather than
//     "anything under the directory" on purpose: generate itself records
//     births inside an ADOPTED frontend (its hook tests, its seams), and a
//     prefix test would flip the frontend to scaffolded on its second run.
//   - the nested go.mod forge writes into every web frontend it scaffolds as
//     a `go test ./...` boundary marker. Frontends scaffolded before the
//     ledger existed (control-plane's internal-console) carry this and no
//     ledger entry.
func frontendScaffoldedByForge(projectDir, feRel, absFeDir string) bool {
	pkgKey := path.Join(filepath.ToSlash(feRel), "package.json")
	if ledgerRecords(projectDir, pkgKey) {
		return true
	}
	gomod, err := os.ReadFile(filepath.Join(absFeDir, "go.mod"))
	return err == nil && bytes.Contains(gomod, []byte(frontendGoModBoundaryMarker))
}

// frontendGoModBoundaryMarker is the first line of the nested go.mod forge's
// frontend scaffold writes (generator.GenerateFrontendFilesWithOptions).
const frontendGoModBoundaryMarker = "// Nested module boundary so `go test ./...`"

// scaffoldLedgerDir and legacyScaffoldLedgerFile are the birth ledger's
// project-relative locations: one entry file per path, and the one-file
// ledger it replaced. The format's owner is internal/checksums
// (scaffoldledger.go); this package is a pure schema package with no
// internal imports, so it reads the one fact it needs — whether a key is
// recorded — with a local view of the layout.
const (
	scaffoldLedgerDir        = ".forge/scaffolded"
	legacyScaffoldLedgerFile = ".forge/scaffolded.json"
)

func ledgerRecords(projectDir, key string) bool {
	if _, err := os.Stat(filepath.Join(projectDir, filepath.FromSlash(scaffoldLedgerDir+"/"+key+".json"))); err == nil {
		return true
	}
	data, err := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(legacyScaffoldLedgerFile)))
	if err != nil {
		return false
	}
	var ledger struct {
		Files map[string]json.RawMessage `json:"files"`
	}
	if json.Unmarshal(data, &ledger) != nil {
		return false
	}
	_, ok := ledger.Files[key]
	return ok
}

// ComponentsDirRel is where forge's component library lives in this
// frontend, relative to its directory: the package.json components_dir, or
// DefaultComponentsDir.
func (f FrontendConfig) ComponentsDirRel() string {
	if f.ComponentsDir != "" {
		return f.ComponentsDir
	}
	return DefaultComponentsDir
}

// WantsHooksFor reports whether this frontend gets generated React Query
// hooks for the named service. nil Hooks (no allowlist) means every service;
// an entry matches the proto service name ("ChatService") or its
// fully-qualified name ("reliant.v1.ChatService").
func (f FrontendConfig) WantsHooksFor(name, fullName string) bool {
	if f.Hooks == nil {
		return true
	}
	for _, h := range f.Hooks {
		h = strings.TrimSpace(h)
		if h == name || (fullName != "" && h == fullName) {
			return true
		}
	}
	return false
}

// UnknownHooks returns the allowlist entries that name none of known (proto
// service names, either short or fully qualified), sorted.
func (f FrontendConfig) UnknownHooks(known map[string]bool) []string {
	var out []string
	for _, h := range f.Hooks {
		if !known[strings.TrimSpace(h)] {
			out = append(out, h)
		}
	}
	sort.Strings(out)
	return out
}

// FrontendComponentsDir is the components directory of the frontend at
// feDir, relative to it: its package.json components_dir, or
// DefaultComponentsDir when it states none (or states one forge cannot use,
// which generate reports).
func FrontendComponentsDir(feDir string) string {
	block, _, err := readFrontendPackageForge(feDir)
	if err != nil || block.ComponentsDir == "" {
		return DefaultComponentsDir
	}
	return block.ComponentsDir
}
