package generator

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"
)

// guardTemplateMode maps a ConfigGuardConfig to the TypedAccessGuard string
// the golangci.yml template branches on: "" when the guardrail is OFF (so
// the template's {{if .TypedAccessGuard}} blocks render nothing), else the
// effective "warn"/"error" mode. Collapsing "off" to "" keeps the template
// conditionals simple truthiness checks.
func guardTemplateMode(g config.ConfigGuardConfig) string {
	if !g.TypedAccessGuardEnabled() {
		return ""
	}
	return g.EffectiveEnforceTypedAccess()
}

// scaffoldTypedAccess is the config.enforce_typed_access a scaffold writes
// into forge.yaml and renders into .golangci.yml — one answer, so the two
// cannot disagree.
//
// A greenfield project has no env-reading debt, so it starts gating
// ("error"). A project adopted in place does: every os.Getenv it already
// has would fail `forge lint` on the first run, which turns adopting forge
// into a flag day. It starts advisory ("warn") — the same strictness an
// absent key resolves to — and flips to "error" once its reads have moved
// onto the typed config.
func (g *ProjectGenerator) scaffoldTypedAccess() string {
	if g.Adopting {
		return config.EnforceTypedAccessWarn
	}
	return config.EnforceTypedAccessError
}

// projectTemplateData is the single render payload for every frozen
// project-level template. It used to be modeled twice: an anonymous
// ~18-field struct built inline in ProjectGenerator.Generate() (the
// scaffold lane) and a hand-mirrored upgradeTemplateData (the upgrade /
// regenerate lane). The two drifted field-by-field and reimplemented the
// same per-field derivations (protoName, goVersionMinor,
// dockerBuilderGoVersion). Promoting to one named
// type with two constructors — forScaffold (from a *ProjectGenerator) and
// forUpgrade (from a *config.ProjectConfig) — keeps the field set in one
// place; snapshot tests guard that both lanes still emit identical output.
//
// The type is a superset: a handful of fields are populated by exactly one
// lane (the other leaves the zero value). That is safe because the
// managed templates that render in BOTH lanes never branch on a
// lane-specific field:
//
//   - ServicePackage / ForgeVersion — scaffold-only.
//     Consumed by scaffold-only templates (config.proto.tmpl, go.mod.tmpl)
//     that the upgrade lane never renders.
type projectTemplateData struct {
	Name           string
	ProtoName      string
	Module         string
	ServiceName    string
	ServicePackage string
	ServicePort    int
	ProjectName    string
	FrontendName   string
	FrontendPort   int
	// HasFrontend reports whether this project ships a browser at all. It
	// gates the dev identity provider in docker-compose.yml: an IdP exists
	// to complete a REAL browser sign-in, so a project with no browser
	// (a worker, a CLI, an API-key service) must not be handed a second web
	// server it will never call. Derived — never a flag — from whether the
	// project declares a frontend, so it follows `forge scaffold frontend`
	// on the next `forge generate` with nothing for the user to turn on.
	// Both lanes ask the same question — is a frontend declared? — and not
	// whether frontend codegen is on (a frontend-only project has a browser
	// and no codegen).
	HasFrontend            bool
	GoVersion              string
	GoVersionMinor         string
	DockerBuilderGoVersion string
	ConfigFields           map[string]bool
	// RESTEnabled mirrors the `api.rest` toggle in forge.yaml. At scaffold
	// time this is always false (REST is opt-in via a post-scaffold edit),
	// but the field is declared here so buf.yaml's dep gate has a known
	// input shape; the upgrade lane reads the live forge.yaml api.rest value.
	RESTEnabled bool
	// GenModule is ProjectGenerator.genModule: gen/ is a module of its own.
	// go.mod.tmpl requires and replaces it, and go.work.tmpl uses it, only
	// then — a project without codegen, or one whose root module owns gen/
	// (GenInRootModule), has no gen/go.mod to point at.
	GenModule bool
	// ForgeVersion is the forge version go.mod.tmpl and gen-go.mod.tmpl pin
	// (`require github.com/reliant-labs/forge <version>`, no replace): this
	// binary's version when a proxy serves it, else the newest published
	// release its source descends from (a dev build is bridged to its checkout
	// by go.work); for an in-place scaffold adopting a module that already
	// requires forge, that module's pin (ProjectGenerator.ForgePin). Never
	// empty in practice — an empty require lets
	// `go mod tidy` resolve forge/pkg/* to the retired forge/pkg module; see
	// resolveForgeVersion in project_pkgdep.go.
	// Populated by forScaffold only (go.mod is not an upgrade-managed file).
	ForgeVersion string
	// VersionVar mirrors forge.yaml build.version_var. The Dockerfile
	// template stamps an extra `-X <VersionVar>=${FORGE_VERSION}` when set;
	// empty (the default) renders nothing, preserving main.version-only
	// stamping for projects that don't set it.
	VersionVar string
	// TypedAccessGuard is the env-access guardrail strictness the
	// golangci.yml.tmpl branches on. It is "" when the guardrail is OFF (the
	// template's {{if .TypedAccessGuard}} then emits nothing), "warn" when
	// advisory, or "error" when gating — i.e. it carries the EFFECTIVE mode
	// EXCEPT that "off" is collapsed to "". The scaffold lane writes
	// scaffoldTypedAccess ("error" greenfield, "warn" when adopting a project
	// in place); the upgrade lane reads the live forge.yaml
	// config.enforce_typed_access (absent → "warn"). See
	// config.ConfigGuardConfig and guardTemplateMode below.
	TypedAccessGuard string
	// LoaderPackage is the allowlisted package path the guardrail excludes
	// from forbidigo (forge's generated config loader). Defaults to
	// config.DefaultLoaderPackage ("pkg/config").
	LoaderPackage string
	// Binaries enumerates every buildable entrypoint the project ships —
	// one per `cmd/<bin>/` directory on disk (devspace idiom: each binary
	// gets its own cmd/<bin>/main.go). The Dockerfile template ranges over
	// this to `go build -o /app/<bin> ./cmd/<bin>` for EACH binary into a
	// single image. Primary is the server entrypoint (cmd/<projectName>/),
	// the one the production stage runs by default; secondary binaries
	// (proxy / ctl / worker-style) are run by overriding the container
	// command to /app/<bin> per-workload at deploy time.
	//
	// Enumerated from disk (not the component list) so it captures every
	// real cmd/<bin>/ — including binaries that predate the component
	// registry or were added by hand. At scaffold time only the primary
	// exists; forUpgrade re-scans so a project that ran `forge scaffold binary`
	// gets every entrypoint built. Falls back to just the primary when the
	// cmd/ tree can't be read.
	Binaries []BinaryBuild
	// HasMigrations reports whether db/migrations/ holds at least one .sql
	// file, read from DISK via codegen.ProjectHasSQLMigrations — the same
	// predicate that decides whether db/embed.go (forgedb.MigrationsFS)
	// exists. cmd-tree-db.go.tmpl gates on it: `db migrate` applies the
	// EMBEDDED migration set, so the template may only reference forgedb
	// when that package variable is actually emitted. A fresh scaffold has
	// none (db/migrations holds only .gitkeep); the Tier-1 regeneration
	// lane re-reads disk, so the first `forge db migration new` + `forge
	// generate` flips it on.
	HasMigrations bool
}

// BinaryBuild is one buildable entrypoint: the cmd/<Dir>/ leaf, which is
// also the output binary name (/app/<Dir>).
type BinaryBuild struct {
	// Dir is the cmd/<Dir>/ leaf — the `go build ./cmd/<Dir>` package path
	// segment AND the output binary basename (/app/<Dir>).
	Dir string
	// Primary marks the server entrypoint (cmd/<projectName>/) that the
	// production image runs by default (ENTRYPOINT/CMD). Exactly one binary
	// is primary; the rest are run via a per-workload command override.
	Primary bool
}

// discoverBinaries enumerates the project's buildable entrypoints from the
// cmd/ tree on disk. Every cmd/<dir>/ that contains a main.go is one
// binary, output to /app/<dir>. primary (the cmd/<projectName>/ server
// leaf) is marked Primary and sorted first; the rest follow in name order
// for a deterministic Dockerfile.
//
// Disk enumeration — not the component registry — is the source of truth:
// it captures hand-added binaries and binaries the registry doesn't model,
// and it matches exactly what `go build` can compile. When the cmd/ tree
// can't be read (fresh scaffold mid-write, missing dir), it falls back to
// the primary alone so the Dockerfile always builds at least the server.
func discoverBinaries(projectDir, primary string) []BinaryBuild {
	fallback := []BinaryBuild{{Dir: primary, Primary: true}}
	if projectDir == "" || primary == "" {
		return fallback
	}
	entries, err := os.ReadDir(filepath.Join(projectDir, "cmd"))
	if err != nil {
		return fallback
	}
	var others []string
	sawPrimary := false
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := e.Name()
		// Only count a dir that actually holds a main.go — `go build
		// ./cmd/<dir>` needs a `package main` entrypoint there. Skips
		// support dirs (e.g. cmd/<bin>/cmd command-tree subpackages are
		// nested, not siblings, so they never show up here).
		if _, statErr := os.Stat(filepath.Join(projectDir, "cmd", dir, "main.go")); statErr != nil {
			continue
		}
		if dir == primary {
			sawPrimary = true
			continue
		}
		others = append(others, dir)
	}
	if !sawPrimary && len(others) == 0 {
		return fallback
	}
	sort.Strings(others)
	bins := make([]BinaryBuild, 0, len(others)+1)
	bins = append(bins, BinaryBuild{Dir: primary, Primary: true})
	for _, d := range others {
		bins = append(bins, BinaryBuild{Dir: d})
	}
	return bins
}

// forScaffold builds the render payload for the `forge project new` scaffold lane
// from a *ProjectGenerator. It reproduces, verbatim, the derivations the
// old inline anonymous struct performed (protoName via hyphen→underscore,
// servicePackage via naming.ServicePackage, the goVersion family, the
// forge/pkg version pin).
func (g *ProjectGenerator) forScaffold() projectTemplateData {
	goVersion := g.resolveGoVersion()

	// Sanitize name for proto files (no hyphens allowed). Use underscores
	// rather than stripping so that "my-cool-app" becomes "my_cool_app"
	// (a valid proto package identifier) instead of "mycoolapp" — which
	// silently loses the word boundaries and breaks grep.
	protoName := strings.ReplaceAll(g.Name, "-", "_")

	// ServicePackage is the Go-package-safe form of ServiceName: hyphens
	// become underscores so the value is valid in `package` declarations
	// and proto package segments. Templates that emit Go/proto identifiers
	// must use ServicePackage; ServiceName is retained for display strings.
	servicePackage := ""
	if g.ServiceName != "" {
		servicePackage = naming.ServicePackage(g.ServiceName)
	}

	data := projectTemplateData{
		Name:                   g.Name,
		ProtoName:              protoName,
		Module:                 g.ModulePath,
		ServiceName:            g.ServiceName,
		ServicePackage:         servicePackage,
		ServicePort:            g.ServicePort,
		ProjectName:            g.Name,
		FrontendName:           g.FrontendName,
		FrontendPort:           g.FrontendPort,
		HasFrontend:            g.FrontendName != "",
		GoVersion:              goVersion,
		GoVersionMinor:         goVersionMinor(goVersion),
		DockerBuilderGoVersion: dockerBuilderGoVersion(goVersion),
		ConfigFields:           codegen.DefaultConfigFieldNames(),
		// forge/pkg is a published module — pin its version, no replace, no
		// vendoring. resolveForgeVersion uses this binary's proxy-resolvable
		// version, or the published release its source descends from; an
		// in-place scaffold adopting a module keeps that module's pin.
		ForgeVersion: g.requiredForgeVersion(),
		// REST is off at scaffold time; users opt-in post-scaffold by
		// editing forge.yaml's `api.rest:` and re-running `forge generate`
		// (RegenerateInfraFiles re-renders buf.yaml from the live value).
		RESTEnabled: false,
		GenModule:   g.genModule(),
		VersionVar:  g.BuildVersionVar,
		// The same strictness the scaffolded forge.yaml records
		// (project_config.go): gating for greenfield, advisory for a project
		// adopted in place (scaffoldTypedAccess).
		TypedAccessGuard: guardTemplateMode(config.ConfigGuardConfig{EnforceTypedAccess: g.scaffoldTypedAccess()}),
		LoaderPackage:    config.DefaultLoaderPackage,
		// At scaffold time the cmd/ tree isn't written yet, so this resolves
		// to the primary alone — exactly right for a fresh `forge project new`. Once
		// the user runs `forge scaffold binary`, the upgrade/regenerate lane
		// (forUpgrade) re-scans cmd/ and the Dockerfile picks up every binary.
		Binaries: discoverBinaries(g.treeDir(), g.binaryName()),
		// A fresh scaffold ships no .sql yet — db/embed.go is not written,
		// so the db command tree must not reference forgedb.
		HasMigrations: codegen.ProjectHasSQLMigrations(g.treeDir()),
	}

	// Strip migration-related config fields when migrations are disabled.
	// The server template conditionally includes migration code based on
	// ConfigFields["AutoMigrate"], so removing the field here prevents
	// the template from emitting app.AutoMigrate() calls.
	if !g.Features.MigrationsEnabled() {
		delete(data.ConfigFields, "AutoMigrate")
		delete(data.ConfigFields, "DatabaseUrl")
		delete(data.ConfigFields, "MaxOpenConns")
		delete(data.ConfigFields, "MaxIdleConns")
		delete(data.ConfigFields, "ConnMaxIdleTime")
		delete(data.ConfigFields, "ConnMaxLifetime")
	}

	return data
}

// forUpgrade builds the render payload for the upgrade / Tier-1
// regeneration lane from a *config.ProjectConfig. It reproduces, verbatim,
// the derivations the old buildTemplateData performed.
//
// projectDir (when non-empty) is used to read the project's go.mod `go`
// directive so upgrade doesn't silently retarget the project to the host's
// Go version, and to parse proto/<project>/config/v1 for the live ConfigFields set. When
// projectDir is empty or go.mod can't be parsed, we fall back to the host's
// detected version.
func forUpgrade(cfg *config.ProjectConfig, projectDir string) projectTemplateData {
	goVersion := goVersionFromGoMod(projectDir)
	if goVersion == "" {
		goVersion = detectGoVersion()
	}
	protoName := strings.ReplaceAll(cfg.Name, "-", "_")

	// The servers are read off the project, not off cfg: a forge.yaml carries
	// no component list, so the proto descriptor + package tree under
	// projectDir is the only thing that knows what this project serves. An
	// empty projectDir (no tree to read) leaves the "api" default below.
	//
	// The port is the same for all of them: every service in the binary mounts
	// onto one Connect mux and the process listens once (config.DefaultServePort).
	servers := codegen.DiscoverProjectComponents(projectDir, cfg.Name).OfKind(config.ComponentKindServer)
	serviceName := "api"
	servicePort := config.DefaultServePort
	if len(servers) > 0 {
		serviceName = servers[0].Name
	}

	frontendName := ""
	frontendPort := 3000
	if len(cfg.Frontends) > 0 {
		frontendName = cfg.Frontends[0].Name
		if cfg.Frontends[0].Port != 0 {
			frontendPort = cfg.Frontends[0].Port
		}
	}

	// Parse config fields from proto/<project>/config/v1/ so templates can conditionally
	// include code blocks that reference specific config fields.
	configFields := codegen.DefaultConfigFieldNames()
	if projectDir != "" {
		if msgs, err := codegen.ParseConfigProtosFromDir(filepath.Join(projectDir, "proto", "config")); err == nil && len(msgs) > 0 {
			configFields = codegen.RootConfigFieldNames(msgs, projectDir)
		}
	}

	return projectTemplateData{
		Name:         cfg.Name,
		ProtoName:    protoName,
		Module:       cfg.ModulePath,
		ServiceName:  serviceName,
		ServicePort:  servicePort,
		ProjectName:  cfg.Name,
		FrontendName: frontendName,
		FrontendPort: frontendPort,
		// Read from the LIVE forge.yaml, so a frontend added after
		// scaffolding brings the IdP with it on the next `forge generate`,
		// and removing the last one takes it away again.
		HasFrontend:            len(cfg.Frontends) > 0,
		GoVersion:              goVersion,
		GoVersionMinor:         goVersionMinor(goVersion),
		DockerBuilderGoVersion: dockerBuilderGoVersion(goVersion),
		ConfigFields:           configFields,
		RESTEnabled:            cfg.API.REST,
		// The forge.yaml `build:` block is gone; the Dockerfile's
		// extra-version-stamp escape hatch is now user-owned (edit the
		// generated Dockerfile's -ldflags directly, or stamp the extra -X
		// via a KCL GoBuild.ldflags entry that `forge build` honors). No
		// VersionVar flows from forge.yaml anymore.
		VersionVar: "",
		// Resolve the typed-access guardrail from the live forge.yaml. An
		// absent config: block resolves to "warn" (advisory) so existing
		// projects gain the guardrail without a flag-day; an explicit
		// off/error is honored.
		TypedAccessGuard: guardTemplateMode(cfg.Config),
		LoaderPackage:    cfg.Config.EffectiveLoaderPackage(),
		// Re-scan the cmd/ tree so the Dockerfile builds EVERY entrypoint
		// the project has grown (server + every `forge scaffold binary`), each
		// into /app/<bin> in the single image.
		Binaries: discoverBinaries(projectDir, cfg.Name),
		// Re-read disk: the first migration a project writes flips this on,
		// and db.go + db/embed.go are regenerated together on the same run.
		HasMigrations: codegen.ProjectHasSQLMigrations(projectDir),
	}
}
