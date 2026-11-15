package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// frontend_inventory.go — the frontend inventory, derived.
//
// There is no `frontends:` key in forge.yaml. The inventory is the frontends
// found under frontends/ on disk, overlaid by the KCL `forge.Frontend`
// declarations (MergeFrontendInventory). It is a CODEGEN INVENTORY: the set of frontends forge projects
// generated TypeScript into (Connect hooks, config_gen.ts, mocks, CRUD
// pages). It is not a deployment topology — a deploy declares its
// frontends in deploy/kcl/<env>/main.k, and a project may legitimately
// deploy a frontend forge does not generate a single byte for.
//
// Conflating the two is the bug this file closes, in both directions:
//
//   - A project that moved its frontend topology into KCL and deleted the
//     `frontends:` block lost its codegen inventory with it. `forge
//     generate` then emitted nothing for a frontend whose code is right
//     there in the repository, and reported its ~30 already-generated
//     files as stale — including config_gen.ts, so the app kept declaring
//     a config field the proto no longer had.
//
//   - `forge doctor`'s drift check read cfg.Frontends as though it were
//     the deploy inventory and reported every KCL-declared frontend
//     missing from it, including ones forge must never generate into.

// KCLFrontend is the narrow view of a KCL-declared frontend this package
// needs to fold one into the inventory. internal/config is a pure schema
// package with no internal imports, so callers (internal/cli's
// FrontendEntity, internal/doctor's renderedFrontend) convert into it at
// their own boundary rather than making this package depend on theirs.
type KCLFrontend struct {
	Name string
	Type string
	// Path is the frontend's directory as the KCL declared it, relative
	// to the project root (or absolute). Empty means the KCL named no
	// path, in which case the frontends/<name> convention applies.
	Path string
	// HasSource reports whether the declaration pins its code to another
	// repository. Such a frontend has no directory in this tree by
	// design, so it is never a codegen target here.
	HasSource bool
	// Port, DevRunner and BasePath are the declaration's dev-server port,
	// package manager and mount prefix; zero/empty means it declared none.
	Port      int
	DevRunner string
	BasePath  string
	// Routes is the CRUD-page allowlist (forge.Frontend.routes); empty means
	// every CRUD entity, ["none"] means no generated pages.
	Routes []string
}

// OwnsFrontendCode reports whether THIS project's tree contains the
// frontend's source, which is precisely the condition under which forge
// may generate into it.
//
// The test is containment, not existence-of-a-directory: a path that
// escapes the project root (`../reliant/web`) belongs to another
// repository whose owner generates its own code. Writing this project's
// hooks there would put one repo's generated TypeScript in another repo's
// working tree, where its own `forge generate` does not know to refresh
// it and its own git status reports it as an unexplained edit.
//
// control-plane states the same rule from the other side: its
// reliant-web declaration in deploy/kcl/lib/services.k carries the
// comment "control-plane codegen must never be projected into the
// sibling working tree". That invariant was previously enforced only by
// a human remembering to leave a name out of a list; here it is a
// property of the path.
//
// A cross-repo `source:` pin is excluded for the same reason and one
// more: its code is materialized into a machine-local cache at build
// time, so there is no in-tree directory to generate into at all.
func (k KCLFrontend) OwnsFrontendCode(projectDir string) bool {
	rel, ok := k.asConfig().Dir(projectDir)
	if !ok {
		return false
	}
	// The directory must actually be present. A KCL declaration naming
	// an in-repo path that does not exist yet is a deploy the project
	// has written ahead of the code; generating into it would create a
	// half-frontend nothing imports. MissingInRepoCode is the other half
	// of this same test, and reports that case.
	info, err := os.Stat(filepath.Join(projectDir, rel))
	return err == nil && info.IsDir()
}

// MissingInRepoCode reports the complement of OwnsFrontendCode within
// the same admission test: this project owns the frontend's LOCATION —
// a contained path, no cross-repo pin — and there is no directory there.
//
// That is the one frontend forge genuinely cannot build. Every other
// shape has code somewhere forge can reach: a pinned source is
// materialized from the pin at build time, and a path pointing out of
// the tree names a checkout whose presence is a property of the machine
// rather than of this repository's configuration.
//
// Restricting it to contained paths is what keeps the answer stable on
// a bare CI checkout, which is the condition every deployability check
// is required to be answerable under. A sibling-path frontend would
// otherwise be reported on every CI run and pass on every developer's
// machine — a verdict that flips with the working copy trains the reader
// to ignore the whole report.
func (k KCLFrontend) MissingInRepoCode(projectDir string) bool {
	if _, ok := k.asConfig().Dir(projectDir); !ok {
		return false
	}
	return !k.OwnsFrontendCode(projectDir)
}

// InRepoDir resolves the declaration to a path inside this project, and
// reports whether it is one at all. A cross-repo `source:` pin never is:
// its code is materialized into a machine-local cache at build time, so
// there is no in-tree directory to speak of.
func (k KCLFrontend) InRepoDir(projectDir string) (string, bool) {
	return k.asConfig().Dir(projectDir)
}

// asConfig views a KCL declaration as the inventory type, so containment
// and the frontends/<name> convention are answered by FrontendConfig.Dir
// — the one implementation — rather than by a second copy of the rule
// that could drift from it. A cross-repo pin is carried through as a
// Source so Dir excludes it for the same reason the codegen admission
// test does.
func (k KCLFrontend) asConfig() FrontendConfig {
	fe := FrontendConfig{Name: k.Name, Type: k.Type}.WithDir(k.Path)
	if k.HasSource {
		// Set AFTER WithDir, which clears Source by definition. The KCL
		// path of a pinned frontend describes the other repository's
		// layout, so this declaration must still resolve to "no directory
		// here" — the pin is not materialized just because a path was
		// written down.
		fe.Source = &GitSource{Repo: "kcl-declared-source"}
	}
	return fe
}

// ResolveInventoryAtLoad fills a project's frontend inventory from the
// filesystem when forge.yaml declares none, and normalizes the paths and
// types of whatever it ends up with. It runs inside LoadProject, so EVERY
// command that loads a forge.yaml gets the same answer.
//
// The load seam is the whole point. This resolution used to happen in a
// step registered only on the generate pipeline, which mutated the config
// in memory. A project that declares its frontends in KCL instead of
// forge.yaml was therefore generated for and then neither linted,
// typechecked, nor built — `forge generate` saw its frontends and every
// other command saw none, from the same file. Resolving where the config
// is LOADED is what makes one answer structurally impossible to diverge.
//
// Only the CHEAP half lives here, and deliberately so. The marker-gated
// directory scan costs one readdir; deriving from KCL costs a full render
// per environment (measured at 1.6-2.2s each, four environments on
// control-plane) and would put ~7s on every `forge lint`, `forge doctor`
// and shell completion. The render-based half therefore stays with the
// commands that already render — see cli.stepDeriveFrontendInventory,
// which now supplements this rather than replacing it.
//
// Best-effort by construction: a project with no frontends/ directory
// resolves to the empty inventory it already had, never to an error.
func ResolveInventoryAtLoad(cfg *ProjectConfig, projectDir string) {
	if cfg == nil {
		return
	}
	if projectDir != "" {
		cfg.Frontends = DiscoverInRepoFrontends(projectDir)
		if root, ok := DiscoverRootFrontend(projectDir); ok && !hasFrontendNamed(cfg.Frontends, root.Name) {
			cfg.Frontends = append(cfg.Frontends, root)
		}
	}
	NormalizeFrontendDefaults(cfg)
}

// RootFrontendName is the name a frontend at the project root is known by
// until a KCL declaration names it: the name `forge project new --in-place
// --kind site` declares it under, and the scaffold's default frontend name.
const RootFrontendName = "web"

// DiscoverRootFrontend finds a static site's frontend: the app at the
// project root itself (`forge project new --in-place --kind site`), which
// the frontends/<name> scan cannot see.
//
// Admitted only when the project has NO go.mod. A site is its frontend and
// its deploy envs, nothing else, so the root carrying the framework marker
// is the frontend; a Go project's root is its module, and a stray
// vite.config there is not a frontend this rule should guess at — such a
// project declares its frontend in KCL, which every rendering command reads.
//
// It matters because only the generate pipeline renders KCL to learn the
// frontend inventory (stepDeriveFrontendInventory). Without this, a root
// frontend was generated for and then neither linted, typechecked nor built
// by `forge build`: the same divergence ResolveInventoryAtLoad exists to
// close, for the one layout the directory scan missed.
func DiscoverRootFrontend(projectDir string) (FrontendConfig, bool) {
	if fileExists(filepath.Join(projectDir, "go.mod")) || !fileExists(filepath.Join(projectDir, "package.json")) {
		return FrontendConfig{}, false
	}
	feType := frontendTypeFromMarkers(projectDir)
	if feType == "" || feType == "react-native" {
		return FrontendConfig{}, false
	}
	fe := FrontendConfig{
		Name:      RootFrontendName,
		Type:      feType,
		BasePath:  basePathFromNextConfig(projectDir),
		Output:    outputFromNextConfig(projectDir),
		DevRunner: devRunnerFromLockfile(projectDir, projectDir),
	}.WithDir(".")
	attachFrontendDiskFacts(projectDir, ".", &fe)
	return fe, true
}

func hasFrontendNamed(fes []FrontendConfig, name string) bool {
	for _, fe := range fes {
		if fe.Name == name {
			return true
		}
	}
	return false
}

// NormalizeFrontendDefaults fills the two per-frontend fields that are
// omissible in forge.yaml: `path` (the frontends/<name> convention) and
// `type` (nextjs).
//
// A frontend whose code comes from another repository is left with an
// EMPTY path on purpose. Defaulting it to frontends/<name> would invent a
// directory that does not exist and never will — its real directory is
// whatever the source resolver materializes — and every path-consuming
// caller would then shell into the invention. Empty is the honest answer
// until the source is resolved, and it is one callers can test for.
func NormalizeFrontendDefaults(cfg *ProjectConfig) {
	if cfg == nil {
		return
	}
	for i := range cfg.Frontends {
		fe := &cfg.Frontends[i]
		if fe.DeclaredDir() == "" && !fe.HasGitSource() && fe.Name != "" {
			// Assigns the field directly rather than via WithDir: this is
			// filling in an omitted convention for a frontend that never
			// had a Source, not materializing a pin, and WithDir clears
			// Source as part of its own meaning.
			fe.path = "frontends/" + fe.Name
		}
		if fe.Type == "" {
			fe.Type = "nextjs"
		}
	}
}

// MergeFrontendInventory overlays the KCL-declared frontends onto the ones
// discovered on disk, by name.
//
// KCL is authoritative for every field it declares — path, type, port,
// dev_runner, base_path, routes — because it is the file the project
// maintains as the statement of what its frontends ARE. The disk supplies the
// rest: the Next.js output shape, and any of type/dev_runner/base_path the
// declaration left out (the scaffolded stanza omits `type`, so a Vite app
// would otherwise default to nextjs). A KCL frontend with no directory on disk was already
// excluded by DeriveFrontendsFromKCL (it is not this repo's to generate
// into), and a directory with no KCL declaration stays as discovered.
//
// The result is sorted by name so two runs of the same project produce the
// same inventory.
func MergeFrontendInventory(disk, kcl []FrontendConfig) []FrontendConfig {
	byName := make(map[string]FrontendConfig, len(disk)+len(kcl))
	for _, fe := range disk {
		byName[fe.Name] = fe
	}
	for _, k := range kcl {
		base, found := byName[k.Name]
		if !found {
			// The same DIRECTORY discovered under another name is the same
			// frontend: a site's root app is discovered as RootFrontendName
			// and may be declared in KCL under any name. The declaration's
			// name wins, as every field it states does.
			for name, d := range byName {
				if d.DeclaredDir() != "" && d.DeclaredDir() == k.DeclaredDir() {
					base, found = d, true
					delete(byName, name)
					break
				}
			}
		}
		if !found {
			byName[k.Name] = k
			continue
		}
		merged := k
		merged.Output = base.Output
		if merged.Type == "" {
			merged.Type = base.Type
		}
		if merged.DevRunner == "" {
			merged.DevRunner = base.DevRunner
		}
		if merged.BasePath == "" {
			merged.BasePath = base.BasePath
		}
		byName[k.Name] = merged
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]FrontendConfig, 0, len(names))
	for _, n := range names {
		out = append(out, byName[n])
	}
	return out
}

// DiscoverInRepoFrontends finds the frontends sitting in this project's
// frontends/ directory, identified by the framework config file each
// kind's scaffold ships. It is the render-INDEPENDENT half of the
// fallback.
//
// The KCL is the better source when it is available — it is the file the
// project maintains, and it carries the declaration verbatim. But a KCL
// module that does not compile yields nothing, and "no frontends" is
// indistinguishable in its effects from the bug this whole file exists to
// fix: emitters walk an empty list, thirty already-generated files are
// reported stale, and config_gen.ts silently stops tracking its proto.
//
// Making a project's codegen depend on its deploy graph COMPILING is the
// wrong coupling. A developer mid-edit in deploy/kcl — or one whose KCL
// broke for an unrelated reason — must still be able to regenerate the
// frontend that is plainly there on disk. So this runs when the render
// produced nothing, and the two sources agree by construction on the
// case that matters: a directory under frontends/ is inside the project
// by definition, so the containment rule that excludes sibling repos
// cannot be violated here at all.
//
// Only frontends/<name> is scanned. A frontend at a custom path is
// discoverable from KCL alone, which is the correct asymmetry: a
// non-conventional layout is exactly the case where a declaration should
// be required rather than guessed.
func DiscoverInRepoFrontends(projectDir string) []FrontendConfig {
	entries, err := os.ReadDir(filepath.Join(projectDir, "frontends"))
	if err != nil {
		return nil
	}

	var out []FrontendConfig
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rel := filepath.Join("frontends", e.Name())
		feType := frontendTypeFromMarkers(filepath.Join(projectDir, rel))
		if feType == "" {
			continue
		}
		feDir := filepath.Join(projectDir, rel)
		fe := FrontendConfig{
			Name:      e.Name(),
			Type:      feType,
			BasePath:  basePathFromNextConfig(feDir),
			Output:    outputFromNextConfig(feDir),
			DevRunner: devRunnerFromLockfile(projectDir, feDir),
		}.WithDir(filepath.ToSlash(rel))
		attachFrontendDiskFacts(projectDir, filepath.ToSlash(rel), &fe)
		out = append(out, fe)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// basePathFromNextConfig recovers a Next.js frontend's mount prefix from
// its own next.config.ts when forge.yaml does not declare one.
//
// # The bug this closes
//
// BasePath fed two generated artifacts that MUST agree, and a discovered
// frontend supplied neither of them a value:
//
//	next.config.ts           basePath = env ?? "/internal"   (user-owned, correct)
//	src/lib/basepath_gen.ts  createBasePath(env ?? "")       (forge-owned, EMPTY)
//
// An undeclared prefix therefore did not fail — it produced a HALF-PREFIXED
// app. Next served routes and assets under the prefix from its own config,
// while forge's joinBasePath() became a no-op, so every hand-built URL lost
// the prefix. The casualty is the runtime config document: a scaffolded
// layout writes <script src={joinBasePath("/config.js")}>, which rendered as
// "/config.js" and 404'd against a file living at "/internal/config.js".
// window.__FORGE_CONFIG__ then never existed, every runtime value silently
// fell back to its schema default, and the app called whatever API origin
// that default happened to name.
//
// Measured on control-plane's operator console: the deployed frontend dialed
// the API's DEV origin and every request died on CORS — which reads as a
// backend CORS misconfiguration and is actually this. The generated helper
// and the generated next.config disagreeing about the same value is a defect
// forge can see and must not emit.
//
// # Why next.config.ts is the right source
//
// Because it is where the value already IS. A project that declares its
// frontends in KCL rather than forge.yaml (a deploy-topology-first layout,
// which forge explicitly supports — see DiscoverInRepoFrontends) has no
// forge.yaml frontend entry to carry base_path, but its next.config.ts
// necessarily states the prefix or Next could not serve the app at all.
// Reading it back makes the two generated files agree by construction
// instead of by the user remembering to state the prefix twice.
//
// forge.yaml ALWAYS wins when it declares base_path: this runs only on the
// discovery path, which is by definition the case where forge.yaml said
// nothing. Returns "" for a frontend served from the root, an unreadable
// config, or a prefix forge cannot parse — the previous behaviour, so a
// frontend this cannot read is no worse off than before.
func basePathFromNextConfig(feDir string) string {
	for _, name := range []string{"next.config.ts", "next.config.js", "next.config.mjs"} {
		body, err := os.ReadFile(filepath.Join(feDir, name))
		if err != nil {
			continue
		}
		if m := nextConfigBasePathRE.FindSubmatch(body); m != nil {
			candidate := string(m[1])
			// Validate rather than trust: this value is written into
			// generated TypeScript, and the shape rules are the same ones
			// forge.yaml's base_path is held to. A malformed prefix is
			// dropped, not propagated. An empty fallback is the legitimate
			// root-served case and is returned as-is.
			if candidate == "" {
				return ""
			}
			if _, ok := ValidateBasePath(candidate); ok {
				return candidate
			}
		}
		// The config exists but names no parsable prefix — root-served, or
		// a shape forge does not model. Either way, stop: a second config
		// file for the same frontend would be a different framework's.
		return ""
	}
	return ""
}

// nextConfigBasePathRE matches the scaffolded declaration
//
//	const basePath = process.env.NEXT_PUBLIC_BASE_PATH ?? "/prefix";
//
// which is the line forge's own next.config.ts template emits, and which
// warnIfNextConfigIgnoresBasePath already relies on being present. Only the
// literal fallback is read — the env var is a RUNTIME override and cannot be
// resolved at generate time, which is the same reason it must be baked at
// build time for the browser bundle. Both quote styles are accepted because
// a formatter may rewrite them.
var nextConfigBasePathRE = regexp.MustCompile(
	`(?m)^\s*const\s+basePath\s*=\s*process\.env\.NEXT_PUBLIC_BASE_PATH\s*\?\?\s*["']([^"']*)["']`)

// frontendTypeFromMarkers identifies a frontend's kind by the config file
// its framework requires, and returns "" for a directory that is not a
// frontend at all. Requiring a marker is what keeps a stray directory —
// a scratch copy, a half-deleted tree, a fixtures folder — from being
// generated into.
func frontendTypeFromMarkers(dir string) string {
	markers := []struct {
		file   string
		feType string
	}{
		{"next.config.ts", "nextjs"},
		{"next.config.js", "nextjs"},
		{"next.config.mjs", "nextjs"},
		{"vite.config.ts", "vite-spa"},
		{"vite.config.js", "vite-spa"},
		{"app.json", "react-native"},
	}
	for _, m := range markers {
		if _, err := os.Stat(filepath.Join(dir, m.file)); err == nil {
			return m.feType
		}
	}
	return ""
}

// DeriveFrontendsFromKCL folds the KCL-declared frontends this project
// owns the code for into inventory entries, deduplicated by name and
// ordered by name so two renders of the same project produce the same
// inventory — several environments normally declare the same frontend,
// and map iteration would otherwise make generate's output depend on
// which env was read first.
func DeriveFrontendsFromKCL(projectDir string, kcl []KCLFrontend) []FrontendConfig {
	byName := make(map[string]FrontendConfig, len(kcl))
	for _, fe := range kcl {
		if !fe.OwnsFrontendCode(projectDir) {
			continue
		}
		if _, dup := byName[fe.Name]; dup {
			continue
		}
		dir, _ := fe.InRepoDir(projectDir)
		// BasePath is recovered from the frontend's own next.config.ts for
		// the same reason the filesystem-discovery path does it: a KCL
		// declaration carries deploy topology and has no base_path field,
		// so without this a KCL-declared frontend generates a
		// basepath_gen.ts that contradicts the next.config.ts sitting
		// beside it. See basePathFromNextConfig for what that breaks.
		basePath := fe.BasePath
		if basePath == "" {
			basePath = basePathFromNextConfig(filepath.Join(projectDir, dir))
		}
		derived := FrontendConfig{
			Name:      fe.Name,
			Type:      normalizeKCLFrontendType(fe.Type),
			Port:      fe.Port,
			DevRunner: fe.DevRunner,
			BasePath:  basePath,
			Output:    outputFromNextConfig(filepath.Join(projectDir, dir)),
			Routes:    fe.Routes,
		}.WithDir(dir)
		// A KCL `path` pointing at an existing app is the documented way to
		// adopt one (`forge project new --in-place` prints exactly that
		// declaration), so it gets the same disk facts as a frontends/<name>
		// directory: adopted-or-not, and the package.json forge block.
		attachFrontendDiskFacts(projectDir, filepath.ToSlash(dir), &derived)
		byName[fe.Name] = derived
	}
	if len(byName) == 0 {
		return nil
	}

	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]FrontendConfig, 0, len(names))
	for _, name := range names {
		out = append(out, byName[name])
	}
	return out
}

// outputFromNextConfig reads a Next.js frontend's build shape back from its
// own next.config.ts. The scaffold writes exactly one of three spellings:
// `output: "export"` (static), `output: "standalone"`, or no `output:` at all
// (server). "" means the file is absent or unreadable, which every caller
// treats as the standalone default.
func outputFromNextConfig(feDir string) string {
	for _, name := range []string{"next.config.ts", "next.config.js", "next.config.mjs"} {
		body, err := os.ReadFile(filepath.Join(feDir, name))
		if err != nil {
			continue
		}
		m := nextConfigOutputRE.FindSubmatch(body)
		switch {
		case m == nil:
			return "server"
		case string(m[1]) == "export":
			return "static"
		default:
			return "standalone"
		}
	}
	return ""
}

// nextConfigOutputRE matches a live `output: "<mode>"` property. Lines that
// are comments (`// output: "export"` in the scaffold's own prose) are
// excluded by anchoring on indentation + the property start.
var nextConfigOutputRE = regexp.MustCompile(`(?m)^[^/\n]*\boutput:\s*["'](export|standalone)["']`)

// devRunnerFromLockfile picks the package manager from the lockfile beside
// the frontend, or at the project root for a pnpm/yarn workspace. "" means
// no lockfile says, which resolves to npm.
func devRunnerFromLockfile(projectDir, feDir string) string {
	for _, dir := range []string{feDir, projectDir} {
		switch {
		case fileExists(filepath.Join(dir, "pnpm-lock.yaml")):
			return DevRunnerPNPM
		case fileExists(filepath.Join(dir, "yarn.lock")):
			return DevRunnerYarn
		case fileExists(filepath.Join(dir, "package-lock.json")):
			return DevRunnerNPM
		}
	}
	// Before the first install a new scaffold has no lockfile. Its pinned
	// package manager must still drive dev (rather than defaulting to npm).
	if body, err := os.ReadFile(filepath.Join(feDir, "package.json")); err == nil {
		var pkg struct {
			PackageManager string `json:"packageManager"`
		}
		if json.Unmarshal(body, &pkg) == nil {
			name, _, _ := strings.Cut(pkg.PackageManager, "@")
			switch name {
			case DevRunnerPNPM, DevRunnerNPM, DevRunnerYarn:
				return name
			}
		}
	}
	return ""
}

// normalizeKCLFrontendType maps KCL's Frontend.type spellings (nextjs | vite |
// rn) onto the inventory's (nextjs | vite-spa | react-native), which is what
// every generator compares against.
func normalizeKCLFrontendType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "vite":
		return "vite-spa"
	case "rn":
		return "react-native"
	}
	return t
}
