package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
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
//	  "components_dir": "src/components/forge-ui" // where forge's component library lives
//	}
//
// npm ignores unknown top-level keys, and "forge" is the name tools already
// use for their own block (prettier, eslintConfig, jest), so the file stays
// a valid manifest for every package manager.
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
	Routes        []string  `json:"routes"`
	Hooks         *[]string `json:"hooks"`
	ComponentsDir string    `json:"components_dir"`
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
		return block, true, fmt.Errorf("package.json %q block: %w (known keys: routes, hooks, components_dir)", PackageForgeKey, err)
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
