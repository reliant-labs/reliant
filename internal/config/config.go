// Package config defines the canonical forge.yaml types shared by
// both the CLI (read) and the generator (write) packages. The
// forge.yaml schema deliberately exposes ~40 typed sections so YAML
// unmarshal can hydrate each block; splitting this file would just
// scatter the same surface across multiple packages and break the
// "one schema, one file" contract the generator relies on. The
// max-public-structs revive rule is therefore suppressed at the
// package-doc line below.

// internal/config is a pure schema/data/constants package: it declares the
// canonical forge.yaml types plus package-level lookup tables (e.g.
// ExperimentalFeatureNames — the stable display order shared by `forge project audit`,
// the startup warning, and `forge project features`). The directive opts it
// out of the contract lint rules — specifically the exported-vars rule, which
// would otherwise demand ExperimentalFeatureNames become a getter. A getter is
// the ideal fix but its call sites reach into internal/cli (an ordered slice
// spread as `config.ExperimentalFeatureNames...`); converting it there is a
// cross-package change outside this pass. This is the accepted suppression for
// a genuine data/catalogue package.
//
// The directive is a FULL opt-out, not a lint-only one: it also stops
// contract codegen, so the narrow Service seam in contract.go gets no
// generated mock. That is the intended trade here — nothing mocks
// config — but it is the whole cost, so weigh it before adding the
// directive to a package whose mock anyone compiles against.
//
//forge:exclude-contract: pure forge.yaml schema/data/constants package, including package-level lookup tables
//nolint:revive // max-public-structs: see package doc above.
package config

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/linter/suppress"
)

// ProjectKind identifies the shape of a forge project. The default,
// "service", produces a Connect-RPC service scaffold (handlers,
// middleware, deploy manifests). "cli" produces a Cobra-based CLI
// binary with no server-shaped scaffolding. "library" produces a
// pure Go module with no cmd/ entry point.
const (
	ProjectKindService = "service"
	ProjectKindCLI     = "cli"
	ProjectKindLibrary = "library"
)

// ProjectBinary describes the binary packaging shape for a service
// project. "per-service" (the default) emits the canonical layout:
// one `cmd/server.go` cobra root with `server [services...]` filtering
// at the runtime layer, and one Application per service in deploy/
// KCL. "shared" emits one cobra subcommand per service so callers can
// invoke `./project <svc>` directly, and KCL emits a single
// MultiServiceApplication (one image, N Deployments) instead of N
// Applications. See FORGE_BACKLOG.md "Layer B" for tradeoffs.
const (
	ProjectBinaryPerService = "per-service"
	ProjectBinaryShared     = "shared"
)

// EffectiveProjectKind returns the project kind, defaulting to
// "service" so that older forge.yaml files without a kind: field
// continue to behave as service projects.
func EffectiveProjectKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case ProjectKindCLI:
		return ProjectKindCLI
	case ProjectKindLibrary:
		return ProjectKindLibrary
	default:
		return ProjectKindService
	}
}

// EffectiveProjectBinary returns the binary mode, defaulting to
// "per-service" so projects predating the field keep their existing
// codegen shape.
func EffectiveProjectBinary(binary string) string {
	switch strings.ToLower(strings.TrimSpace(binary)) {
	case ProjectBinaryShared:
		return ProjectBinaryShared
	default:
		return ProjectBinaryPerService
	}
}

// ProjectConfig represents the forge.yaml file.
// Fields align with proto/forge/project/v1/project.proto.
type ProjectConfig struct {
	Name       string `yaml:"name"`
	ModulePath string `yaml:"module_path,omitempty"`
	// Kind is the project shape (service|cli|library). It is not a forge.yaml
	// field: the loader DERIVES it from the project's real sources — the KCL
	// deploy tree, the pkg/app composition root, internal/handlers/, the
	// service protos, and cmd/<name>/main.go (see deriveProjectKindFromSources).
	// The yaml tag is "-" so a stale `kind:` in forge.yaml is reported with a
	// migration hint instead of being honored.
	Kind   string `yaml:"-"`
	Binary string `yaml:"binary,omitempty"` // "per-service" (default), "shared" — one Go binary, cobra subcommand per service
	// ForgeVersion records the forge binary version that this project's
	// generated artifacts were last produced against. It is set at
	// `forge project new` time, bumped after a successful `forge project upgrade`, and
	// consulted by `forge generate` to warn when the forge binary on
	// PATH has drifted from the version pinned by the project. Empty
	// (legacy) projects are treated as "0.0.0".
	ForgeVersion string `yaml:"forge_version,omitempty"`
	// Harness records which AI harness this project was scaffolded for
	// (`forge project new --harness`). It is the ONLY durable record of that
	// choice: the flag is read once at scaffold time, and without it on disk
	// every later `forge generate` has to guess. It selects the memory file
	// (reliant.md, CLAUDE.md, ...). Forge never writes skill files for any
	// harness. Empty means reliant.
	Harness string `yaml:"harness,omitempty"`
	// NOTE: there is intentionally NO project-level `hot_reload:` field.
	// The single hot-reload switch is `features.hot_reload`, which the dev
	// loop and the air-config codegen actually read; a second top-level key
	// spelled the same way resolved through a different accessor that no
	// caller ever invoked. A `hot_reload:` key at the top level is reported
	// with a migration hint (see removedSchemaKeys).
	//
	// NOTE: there is intentionally NO `components:` field, in yaml or in
	// memory. What a project builds and runs — Connect servers, workers,
	// crons, operators, secondary binaries — is declared by the code itself
	// (the proto descriptor, internal/workers/, internal/operators/, cmd/),
	// and a config object carrying a copy would be a second source of truth
	// that is empty exactly when nobody remembered to fill it. A caller that
	// needs the component inventory asks the project for it:
	// codegen.DiscoverProjectComponents. A `components:` key in forge.yaml is
	// reported with a migration hint (see removedSchemaKeys).
	//
	// NOTE: there is intentionally NO `packages:` field either. An internal
	// package is declared by internal/<name>/contract.go — the file the
	// bootstrap/injector codegen has always walked for — and its
	// outbound-boundary claim by the `//forge:outbound-io` marker in its own
	// source. The retired list was never a codegen input: deleting it left
	// every package still imported, constructed and middleware-wrapped in
	// internal/app/compose.go. It steered only the REPORTERS, so a stale
	// entry made `forge project map` / `forge project audit` / the
	// architecture doc name a package that does not exist while hiding one
	// that does. They now resolve through codegen.DiscoverInternalPackages.
	// A `packages:` key is reported with a migration hint (see
	// removedSchemaKeys).
	// Frontends is the frontend inventory. It is DERIVED, never read from
	// forge.yaml (there is no `frontends:` key): the loader fills it from the
	// frontends found on disk, and `forge generate` overlays the KCL
	// `forge.Frontend` declarations onto it. See frontend_inventory.go.
	Frontends []FrontendConfig `yaml:"-"`
	// workspaces records whether the project uses the pnpm-workspaces
	// layout. It is DERIVED from the tree (see DeriveFrontendWorkspaces), not
	// a forge.yaml key.
	workspaces bool
	// projectDir is the directory the project was loaded from; derivation
	// reads the tree under it. Empty for a hand-constructed config.
	projectDir string
	// The section blocks below are all omitempty: a freshly scaffolded
	// forge.yaml leaves them absent and the loader fills shape-derived
	// defaults (see ApplyDerivedDefaults in derive.go). A present block
	// is taken literally — write the block (or a single key) to override.
	Database DatabaseConfig `yaml:"database,omitempty"`
	CI       CIConfig       `yaml:"ci,omitempty"`
	// NOTE: there is intentionally NO `build:` field. Build is declared
	// per-service, per-env in KCL (the polymorphic Build union —
	// GoBuild | DockerBuild | ShellBuild on each forge.Service); forge.yaml
	// carries zero build config. A `build:` key in forge.yaml is REJECTED
	// as an unknown key by the strict loader (walkUnknownKeys) rather than
	// silently ignored — version stamping moves to a KCL GoBuild.ldflags
	// `-X` entry.
	Deploy    DeployConfig    `yaml:"deploy,omitempty"`
	Docker    DockerConfig    `yaml:"docker,omitempty"`
	K8s       K8sConfig       `yaml:"k8s,omitempty"`
	Lint      LintConfig      `yaml:"lint,omitempty"`
	Contracts ContractsConfig `yaml:"contracts,omitempty"`
	// Config steers humans and LLMs away from reading the environment
	// directly (os.Getenv / os.LookupEnv / os.Environ) and toward forge's
	// generated, dependency-injected typed config object. See
	// [ConfigGuardConfig] for the enforce_typed_access / loader_package
	// semantics and the absent-key default (warn).
	Config ConfigGuardConfig `yaml:"config,omitempty"`
	// NOTE: there is intentionally NO `auth:` block. Authentication is owned
	// code — internal/app/auth.go's SetupAuth picks the validator, and
	// issuer/audience/JWKS are per-deployment values that live in env vars
	// routed through KCL. An `auth:` key is reported with a migration hint
	// (see removedSchemaKeys).
	// Features is DERIVED from what exists in the repo (see
	// DeriveFeatureDefaults). The optional `features:` block pins a feature
	// on or off whatever the tree says — the switch for a project whose
	// shape implies a feature it does not want (see FeaturesConfig).
	Features FeaturesConfig `yaml:"features,omitempty"`
	// Observability seeds the OWNED per-package observe_chain.go seam that a
	// generated component decorator routes through. The values are read at
	// `forge scaffold` time to stamp the initial chain (log level, etc.);
	// the seam is user-owned thereafter. See [ObservabilityConfig].
	Observability ObservabilityConfig `yaml:"observability,omitempty"`
	// API toggles project-level API protocol skins layered on top of the
	// Connect mux. Default zero-value leaves both REST and OpenAPI off so
	// existing projects regenerate identically. See [APIConfig] for the
	// per-field semantics.
	API APIConfig `yaml:"api,omitempty"`
	// DevStack bounds forge's parallel-dev-stack port allocation — how many
	// stacks can run side by side, and how far apart their port blocks sit.
	// See [DevStackConfig] for why this has to be declared rather than
	// inferred.
	DevStack DevStackConfig `yaml:"dev_stack,omitempty"`
	// Generate declares commands `forge generate` runs for the project —
	// the escape hatch for a generator forge does not own. See
	// [GenerateConfig] / [GenerateHook]; read by the "generate hooks
	// (forge.yaml)" pipeline step.
	Generate GenerateConfig `yaml:"generate,omitempty"`
}

// DevStackConfig bounds the parallel-dev-stack port allocator
// (fp.allocate_port(base, key) → base + block(key)*BlockSize).
//
// Why this is CONFIG rather than a constant. The number of parallel stacks a
// project supports is not forge's to choose: it is fixed by the project's own
// cluster config. control-plane's deploy/k3d.yaml PRE-MAPS host ports at
// cluster-create time, so a stack whose block falls outside the pre-mapped
// range renders ports the cluster never mapped. Before this block existed the
// ceiling was stated twice — implicitly as "unbounded" in the allocator, and
// literally as a hand-maintained `block = 1..7` comment in k3d.yaml — with
// nothing tying them together. The 8th worktree therefore allocated cleanly
// and then failed as "gateway unreachable", nowhere near the cause.
//
// Declaring it once makes the allocator refuse a block the cluster cannot
// host, and lets the pre-map range be generated from the same number.
type DevStackConfig struct {
	// MaxStacks is how many dev stacks may hold a port block at once,
	// counting the default stack (block 0). Zero means unset → the
	// DefaultMaxStacks default, which is what every existing project gets.
	//
	// It bounds NEW allocations only. A registry that already holds a block
	// at or past the ceiling keeps resolving it: a block multiplies into k3d
	// host mappings and into the dev IdP's baked-in `iss` claim and redirect
	// URIs, so lowering this value must never move a port that has already
	// been issued. Reclaiming space is `forge env devstack prune`.
	MaxStacks int `yaml:"max_stacks,omitempty"`

	// BlockSize is the port-number distance between adjacent stacks —
	// stack N's port for base B is B + N*BlockSize. Zero means unset → the
	// DefaultBlockSize default (100), the value this was hardcoded to before
	// it was configurable.
	//
	// It is exposed because it is half of the spacing invariant: two bases
	// that are congruent modulo BlockSize collide at some block, and whether
	// that block is reachable depends on MaxStacks. The
	// forgeconv-allocate-port-spacing lint reads both.
	BlockSize int `yaml:"block_size,omitempty"`
}

// Dev-stack allocation defaults, applied when the dev_stack: block (or a key
// in it) is absent.
const (
	// DefaultMaxStacks — the default checkout plus seven linked worktrees.
	// Chosen to match the block range control-plane's k3d.yaml already
	// pre-mapped by hand, so adopting the ceiling changes no behavior.
	DefaultMaxStacks = 8
	// DefaultBlockSize is the historical hardcoded quantum.
	DefaultBlockSize = 100
)

// EffectiveMaxStacks returns the configured parallel-stack ceiling, or the
// default when unset.
func (d DevStackConfig) EffectiveMaxStacks() int {
	if d.MaxStacks > 0 {
		return d.MaxStacks
	}
	return DefaultMaxStacks
}

// EffectiveBlockSize returns the configured port-block quantum, or the default
// when unset.
func (d DevStackConfig) EffectiveBlockSize() int {
	if d.BlockSize > 0 {
		return d.BlockSize
	}
	return DefaultBlockSize
}

// Component kind constants. Kind is the single discriminator on a
// [ComponentConfig] — it replaces the old `services[].type` +
// `services[].kind` pair and the separate `binaries:` block.
//
//   - server   — Connect-RPC handlers + client + frontend
//     hooks + bootstrap row + cobra subcommand (was type=go_service).
//   - worker   — in-process ContextWorker goroutine; bootstrap Workers row.
//   - cron     — scheduled job; Schedule drives it. In-process scheduled
//     goroutine for dev, CronJob in deploy. First-class (was
//     worker + kind:cron).
//   - job      — ONE-SHOT: runs a command to completion, exits 0, and
//     gates the components named in Before. The portable "do this once,
//     then proceed" primitive (k8s init container / compose
//     service_completed_successfully / host run-and-wait).
//   - operator — controller-runtime manager + CRDs.
//   - binary   — standalone cobra subcommand cmd/<name>.go (one image,
//     run `./app <name>`); no bootstrap wiring (was the binaries: block).
const (
	ComponentKindServer   = "server"
	ComponentKindWorker   = "worker"
	ComponentKindCron     = "cron"
	ComponentKindJob      = "job"
	ComponentKindOperator = "operator"
	ComponentKindBinary   = "binary"
)

// ComponentConfig describes one buildable/runnable unit of a forge project
// — a Connect server, worker, cron, operator or secondary binary. Its Kind
// field selects which scaffold + deploy treatment the component receives;
// see the ComponentKind* constants.
//
// It carries NO struct tags, and that is load-bearing: a component is never
// serialized to or parsed from a config file. The declaration is the code
// (the proto descriptor, internal/workers/, internal/operators/, cmd/) and
// this type is only the in-memory shape codegen.DiscoverProjectComponents
// hands back after reading it. Per-environment facts (placement, replicas,
// exposed ports) are declared in `deploy/kcl/<env>/main.k`, not here.
type ComponentConfig struct {
	Name string
	// Kind is THE discriminator: server|worker|cron|operator|binary.
	// See the ComponentKind* constants.
	Kind          string
	Path          string
	Schedule      string // cron expression for kind=cron
	ProtoPackages []string
	// Group is the API group for kind=operator components. e.g.
	// "reliant.dev". Read from the operator package's APIGroup const.
	Group string
	// Version is the API version for kind=operator components. e.g.
	// "v1alpha1". Read from the operator package's APIVersion const.
	Version string
	// CRDs lists the CRDs reconciled by this operator — one per
	// operators/<operator>/<crd-name>_controller.go on disk.
	CRDs []CRDConfig
}

// DefaultServePort is the ONE port fact forge itself knows: the port a
// scaffolded binary listens on. Every service in a binary mounts onto the
// same Connect mux and the process listens exactly once, on AppConfig.port
// (env PORT, default 8080) — so the port belongs to the BINARY, never to an
// individual component.
//
// Any OTHER port is a deploy fact and lives in `deploy/kcl/<env>/main.k`
// (forge.workloads.Workload.ports, refined per environment). Nothing forge
// introspects — the proto descriptor, the owned worker/operator files, cmd/ —
// carries a port, so there is deliberately no per-component port field to read.
const DefaultServePort = 8080

// EffectiveKind returns the lowercased, trimmed kind, defaulting to
// "server" for empty input (a component with no kind is a Connect
// server — the historical `type: go_service` default).
func (c ComponentConfig) EffectiveKind() string {
	k := strings.ToLower(strings.TrimSpace(c.Kind))
	if k == "" {
		return ComponentKindServer
	}
	return k
}

// IsServer reports whether the component is a Connect-RPC server.
func (c ComponentConfig) IsServer() bool { return c.EffectiveKind() == ComponentKindServer }

// IsWorker reports whether the component is an in-process worker.
func (c ComponentConfig) IsWorker() bool { return c.EffectiveKind() == ComponentKindWorker }

// IsCron reports whether the component is a scheduled cron job.
func (c ComponentConfig) IsCron() bool { return c.EffectiveKind() == ComponentKindCron }

// IsJob reports whether the component is a one-shot job.
func (c ComponentConfig) IsJob() bool { return c.EffectiveKind() == ComponentKindJob }

// IsOperator reports whether the component is a controller-runtime operator.
func (c ComponentConfig) IsOperator() bool { return c.EffectiveKind() == ComponentKindOperator }

// IsBinary reports whether the component is a standalone binary subcommand.
func (c ComponentConfig) IsBinary() bool { return c.EffectiveKind() == ComponentKindBinary }

// CRDConfig represents a single Custom Resource Definition reconciled by an
// operator, discovered from the <crd-name>_controller.go shim that
// `forge scaffold crd <name> --operator <op>` writes. Untagged for the
// same reason as [ComponentConfig]: it is never serialized.
type CRDConfig struct {
	// Name is the PascalCase CRD type name. e.g. "Workspace".
	Name string
	// Group is the API group, defaulting to the parent operator's
	// Group. Carried explicitly so a single operator can manage CRDs
	// from multiple groups.
	Group string
	// Version is the API version. Defaults to the parent operator's
	// Version.
	Version string
	// Shape is the reconciler scaffold style. One of
	// "state-machine" (phase-driven), "config" (declarative-only,
	// no state), "composite" (manages sub-resources). Drives which
	// template is rendered for the controller shim.
	Shape string
}

// FrontendConfig is one frontend in a project's inventory.
//
// There is no `frontends:` key in forge.yaml. Every field is either declared
// in the KCL `forge.Frontend` (kcl/schema.k) or read off the frontend's own
// files (frontend_inventory.go), so nothing here is a setting you write.
type FrontendConfig struct {
	Name string
	// Type is "nextjs", "react-native" or "vite-spa" — detected from the
	// framework's own config file, or the KCL declaration's `type`.
	Type string
	// path is the frontend's directory, relative to the project root (or
	// absolute) — later REWRITTEN in memory to the resolved directory for a
	// frontend whose code came from a `source:` pin (see
	// cli.resolveFrontendSources).
	//
	// It is UNEXPORTED on purpose, and that is the single load-bearing line
	// in this type. The convention "empty means frontends/<name>" used to be
	// applied by each caller, inline, ~20 times, none of them checking
	// containment. Unexporting is what makes the inline version not compile.
	// Read the directory through Dir, which applies the fallback AND the
	// containment check, or through DeclaredDir when the empty case is the
	// thing you are asking about.
	path string
	// Source pins this frontend's code to another repository at a ref (the
	// KCL `source = forge.GitSource {...}`). nil for an in-repo frontend.
	Source *GitSource
	// Port is the dev-server listen port (KCL `port`). 0 means EPHEMERAL:
	// `forge env up` allocates a free OS port at launch.
	Port int
	// DevRunner is the package manager forge drives the frontend with:
	// "npm", "pnpm" or "yarn". KCL `dev_runner`, else the lockfile on disk.
	DevRunner string
	// BasePath is the URL prefix the frontend is mounted under ("/admin"),
	// "" for the host root. KCL `base_path`, else the literal fallback in the
	// frontend's own next.config.ts. Only meaningful for nextjs.
	BasePath string
	// Output is the Next.js build shape: "standalone", "static" or "server".
	// Read from the frontend's own next.config.ts; "" means standalone.
	Output string
	// Routes restricts which entities this frontend gets generated CRUD pages
	// for (KCL `routes`): route SLUGS ("llm-keys"), an ALLOWLIST. Empty means
	// every CRUD entity. The single value `none` (RoutesNone) means NO
	// generated pages at all. An adopted frontend that declares none gets
	// `none` (frontend_codegen.go).
	Routes []string
	// Adopted reports that forge did not scaffold this frontend — it existed
	// before forge generated into it. generate writes only Tier-1 codegen and
	// the owned seams that code imports into an adopted frontend: no starter
	// components, no CRUD pages, nav or dashboard scaffolds, and no edits to
	// its package.json. Read off disk (frontendScaffoldedByForge); false for
	// a FrontendConfig built in code, which is a frontend forge made.
	Adopted bool
	// Hooks is the package.json `forge.hooks` allowlist: the proto services
	// that get generated React Query hooks in this frontend. nil means every
	// service; an empty list means none. See WantsHooksFor.
	Hooks []string
	// ComponentsDir is the package.json `forge.components_dir`: where forge's
	// component library lives, relative to the frontend directory. "" means
	// DefaultComponentsDir. Read it through ComponentsDirRel.
	ComponentsDir string
	// CodegenProblems are the reasons the frontend's package.json forge block
	// could not be honoured (an unknown key, an escaping components_dir).
	// generate refuses to run while any is present.
	CodegenProblems []string
}

// RoutesNone is the frontends[].routes value meaning "generate no CRUD
// pages for this frontend".
const RoutesNone = "none"

// RoutesNone reports whether this frontend opted out of generated CRUD pages
// entirely (`routes: [none]`).
func (f FrontendConfig) RoutesNone() bool {
	for _, r := range f.Routes {
		if strings.EqualFold(strings.TrimSpace(r), RoutesNone) {
			return true
		}
	}
	return false
}

// Output values for FrontendConfig.Output — the Next.js production shape.
const (
	// FrontendOutputStatic is a static export into out/ (`output: "export"`).
	FrontendOutputStatic = "static"
	// FrontendOutputStandalone is a self-contained Node server
	// (`output: "standalone"`).
	FrontendOutputStandalone = "standalone"
	// FrontendOutputServer is full Next.js (`next start`, no `output:`).
	FrontendOutputServer = "server"

	// FrontendOutputScaffoldDefault is what a NEW Next.js frontend is
	// scaffolded with, and written into forge.yaml. Static because the
	// hosted runtime serves frontends as static sites, and because the
	// generated CRUD routes export cleanly — so the default ships
	// anywhere. It is deliberately not what an empty field means; see
	// EffectiveOutput.
	FrontendOutputScaffoldDefault = FrontendOutputStatic
)

// EffectiveOutput returns the frontend's Next.js production shape,
// lowercased. An unset field is "standalone": the shape every frontend
// scaffolded before the field was always written was given, and still has
// in its scaffold-once next.config.ts. See the Output field.
func (f FrontendConfig) EffectiveOutput() string {
	if o := strings.ToLower(strings.TrimSpace(f.Output)); o != "" {
		return o
	}
	return FrontendOutputStandalone
}

// StaticExport reports whether the frontend's production build is a static
// export — files a CDN or bucket can serve with no server behind them. That
// is the precondition of every static runtime (forge.OnHosted for a
// frontend, forge.OnBucket, forge.OnFirebase): each publishes the build's
// public_dir and runs nothing.
//
// Only a Next.js frontend has a choice. A Vite SPA and an Expo web build
// emit static files by construction, whatever `output:` says.
func (f FrontendConfig) StaticExport() bool {
	return !f.IsNextJS() || f.EffectiveOutput() == FrontendOutputStatic
}

// IsNextJS reports whether the frontend is a Next.js app. An empty type is
// Next.js: it is the scaffold default, and the load seam fills it in for a
// frontend whose directory holds a next.config.
func (f FrontendConfig) IsNextJS() bool {
	t := strings.ToLower(strings.TrimSpace(f.Type))
	return t == "" || t == "nextjs"
}

// Dev-runner values for FrontendConfig.DevRunner, mirroring the KCL
// Frontend schema's `dev_runner`.
const (
	DevRunnerNPM  = "npm"
	DevRunnerPNPM = "pnpm"
	DevRunnerYarn = "yarn"
)

// EffectiveDevRunner returns the frontend's package manager, defaulting to npm.
func (f FrontendConfig) EffectiveDevRunner() string {
	if r := strings.ToLower(strings.TrimSpace(f.DevRunner)); r != "" {
		return r
	}
	return DevRunnerNPM
}

// GitSource declares a component's code as "that repository at that ref"
// rather than a directory that must already exist on disk.
//
// It is the schema half of internal/gitsource (which holds the resolver,
// the cache and the local-override rules); this type is the plain data
// forge.yaml and KCL both unmarshal into. It lives here rather than in
// gitsource so internal/config keeps its no-internal-imports shape — the
// resolver converts to its own Source type at the boundary.
//
// The point is reproducibility, not convenience. A filesystem path to a
// sibling checkout fails wherever the sibling is absent (CI), and where it
// IS present it silently ships whatever happened to be checked out, so two
// machines produce different artifacts from identical commits. A ref makes
// the cross-repo dependency an explicit, reviewable, promotable version —
// the same property go.mod already gives the Go half of the same
// dependency.
type GitSource struct {
	// Repo is the repository: host/owner/name
	// ("github.com/reliant-labs/reliant"), which forge expands to an https
	// clone URL, or an explicit https://, ssh://, or git@host:owner/name
	// URL, which is used verbatim.
	Repo string `yaml:"repo" json:"repo"`
	// Ref is the tag, branch, or commit sha to check out. Required —
	// forge does not default to a repository's default branch, because an
	// unpinned dependency is precisely the problem this type solves.
	Ref string `yaml:"ref" json:"ref"`
	// Subdir is the path within the repository holding this component's
	// source ("web"). Empty means the repository root.
	Subdir string `yaml:"subdir,omitempty" json:"subdir,omitempty"`
}

// Dir resolves the frontend's directory against the project root and
// reports whether it is inside it. It is the ONE answer to "where does
// this frontend live", and it subsumes both helpers it replaced:
// EffectivePath's fallback (empty `path:` means frontends/<name>) and
// FrontendDirWithin's containment check, which the fallback alone never
// had.
//
// ok == false means there is no directory in THIS repository to speak
// of, for one of two reasons the caller almost always wants to treat
// alike:
//
//   - the path escapes the project root (`../reliant/web`) — it names
//     another repository's working tree, whose own forge owns its
//     codegen. Writing here puts one repo's generated TypeScript in
//     another repo's git status.
//   - the frontend is unnamed, so the convention has nothing to fill in.
//
// A `source:` frontend resolves normally ONCE its pin has been
// materialized, because resolution rewrites path to the cache directory;
// before that it has no in-tree location and reports ok == false rather
// than the invented frontends/<name>. That is the behavior difference
// from the retired EffectivePath, which returned the conventional label
// for a tree that was not there — fine for a caller printing a name,
// wrong for the many that then shelled into it.
//
// The returned path is slash-separated and relative to projectDir.
func (f FrontendConfig) Dir(projectDir string) (string, bool) {
	// An unnamed frontend has no identity to resolve, and a still-pinned
	// cross-repo one has no directory in this tree — even when a path is
	// present, since a `source:` frontend's path is either the invalid
	// both-declared shape validate.go rejects, or a KCL declaration whose
	// path names the OTHER repo's layout. Both are excluded before the
	// path is consulted, which is what KCLFrontend.OwnsFrontendCode has
	// always required. Once the pin is materialized the resolver clears
	// Source and rewrites the path, and the frontend resolves normally.
	if f.Name == "" || f.HasGitSource() {
		return "", false
	}
	dir := strings.TrimSpace(f.path)
	if dir == "" {
		dir = filepath.Join("frontends", f.Name)
	}

	abs := dir
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(projectDir, dir)
	}
	rel, err := filepath.Rel(projectDir, abs)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}

// DeclaredDir is the path exactly as declared or resolved, with no
// fallback and no containment check — empty when forge.yaml named none.
//
// Use it ONLY where the empty case, or an out-of-tree path, is the thing
// being asked about: reporting a sibling-repo frontend, deciding whether
// a declaration exists, or handing a path to a resolver that will rewrite
// it. Every caller that wants "the directory to read or write files in"
// wants Dir instead, which is why this one is deliberately awkward to use
// by accident.
func (f FrontendConfig) DeclaredDir() string { return f.path }

// WithDir returns a copy whose directory is dir, and whose code is
// therefore IN this tree — so it clears Source.
//
// Clearing it is the point, not a side effect. Source means "the code is
// in another repository and there is no directory here yet", which is
// exactly what Dir reports ok == false for. A resolver that materialized
// the pin has made that false: the code now sits in the cache directory
// it is passing in. Leaving Source set would keep every reader excluding
// a frontend whose code is on disk and ready to build — the shape of the
// original `forge build --target reliant-web` bug.
//
// It is also how a caller assembles a FrontendConfig from a declaration
// that did not come from forge.yaml (a KCL entity, a discovered
// directory), which is why the parameter is a directory rather than a
// pin.
func (f FrontendConfig) WithDir(dir string) FrontendConfig {
	f.path = dir
	f.Source = nil
	return f
}

// HasGitSource reports whether this frontend's code comes from another
// repository rather than from a directory in the project tree.
func (f FrontendConfig) HasGitSource() bool {
	return f.Source != nil && f.Source.Repo != ""
}

// ToolchainFrontends returns the frontends forge owns a Node toolchain for:
// every frontend in the inventory with its path resolved. It is the single
// project-level answer to "which frontends may forge shell into, and
// where do they live" — shared by the build target resolution and the
// lint pipeline's frontend lane so the two can never disagree about the
// set. Command-specific filters (--target, KCL deploy mode, per-command
// skips) layer on top of this set; they do not re-derive it.
//
// A cross-repo frontend whose source has not been materialized yet is
// EXCLUDED rather than given the conventional fallback. This set is the
// one forge shells into — `npm run build`, `tsc` — and such a frontend has
// no directory in this tree by design. Naming it frontends/<name> invents
// a path that does not exist and never will, so the command fails deep
// inside npm against a fabricated directory instead of simply not
// attempting a frontend whose code is not on disk. Callers that DO
// materialize sources (forge build) rewrite Path first, so the frontend
// is back in this set by the time they read it.
//
// Entries are returned AS THEY ARE. Since resolution happens once, at
// the config load seam (ResolveInventoryAtLoad), a path read here is
// already the resolved one and this function does not re-derive it —
// which is what keeps a RESOLVED path from becoming indistinguishable
// from a DECLARED one. Callers read the directory through Dir, which
// applies the frontends/<name> convention for a config assembled in
// memory rather than loaded from disk.
func (c *ProjectConfig) ToolchainFrontends() []FrontendConfig {
	if c == nil {
		return nil
	}
	out := make([]FrontendConfig, 0, len(c.Frontends))
	for _, fe := range c.Frontends {
		if fe.DeclaredDir() == "" && fe.HasGitSource() {
			continue
		}
		out = append(out, fe)
	}
	return out
}

// IsFrontendWorkspacesEnabled reports whether the project uses the
// pnpm-workspaces layout (root pnpm-workspace.yaml + packages/api +
// packages/hooks shared across frontends). It is read off the tree, not
// configured: see DeriveFrontendWorkspaces.
func (c ProjectConfig) IsFrontendWorkspacesEnabled() bool {
	return c.workspaces
}

// WithFrontendWorkspaces returns a copy that uses (or does not use) the
// pnpm-workspaces layout, for a config assembled in memory rather than loaded
// from a tree that already shows the layout.
func (c ProjectConfig) WithFrontendWorkspaces(on bool) ProjectConfig {
	c.workspaces = on
	return c
}

// HasReactNativeFrontend reports whether any frontend in the project is
// a React Native (Expo) app. Used to gate features that only apply to
// mobile — e.g. the `@<scope>/ui-native` workspace package.
//
// Returns true for frontends declared with `type: react-native` (or the
// historic `type: react_native` underscore form the validator also
// accepts).
func (c ProjectConfig) HasReactNativeFrontend() bool {
	for _, fe := range c.Frontends {
		t := strings.ToLower(strings.TrimSpace(fe.Type))
		if t == "react-native" || t == "react_native" {
			return true
		}
	}
	return false
}

// DatabaseConfig holds database-related settings.
//
// Only migration_safety is forge.yaml surface. The driver and migrations
// directory are DERIVED: a service project whose tree has db/migrations uses
// postgres and that directory, and one without it has no database (driver
// "none"). The dev seed has no config at all — rows, auto-seed and table
// scoping are fixed defaults (see internal/cli/db_seed.go).
type DatabaseConfig struct {
	// Driver is "postgres" or "none" — derived, see DeriveDatabase.
	Driver string `yaml:"-"`
	// MigrationsDir is always db/migrations when there is a database.
	MigrationsDir   string                `yaml:"-"`
	MigrationSafety MigrationSafetyConfig `yaml:"migration_safety,omitempty"`
	// ORM places the generated data layer. Absent, it is internal/db.
	ORM ORMConfig `yaml:"orm,omitempty"`
	// PostgresVersion is the postgres the project runs in production — a
	// major ("16") or an exact release ("16.9.0") — and so the one its
	// tests run against: forge/pkg/pgtest boots its embedded server at this
	// version (and checks an external FORGE_TEST_POSTGRES_URL server's
	// major against it). Empty means pgtest's default, the newest major.
	// Read by pgtest itself from the nearest forge.yaml, so a bare
	// `go test` honours it; FORGE_TEST_POSTGRES_VERSION overrides it for
	// one run.
	PostgresVersion string `yaml:"postgres_version,omitempty"`
}

// DefaultMigrationsDir is where migrations live.
const DefaultMigrationsDir = "db/migrations"

// DefaultORMPackage is where the generated data layer lands when
// database.orm.package is unset.
const DefaultORMPackage = "internal/db"

// ORMConfig is the generated data layer's placement.
type ORMConfig struct {
	// Package is the project-relative directory the ORM projects into —
	// every <entity>_orm_gen.go, store_gen.go and _repo_ext.go seam — and
	// so the Go package they declare (its last path element). Empty means
	// DefaultORMPackage.
	//
	// It exists for a project that already has a data layer of its own in
	// internal/db: there the projection's names (Chat, Message, Store,
	// NewStore) collide with the project's, one package cannot declare
	// both, and porting onto the store one table at a time needs the two
	// to live side by side. `package: internal/store` puts the projection
	// in its own package; every generated consumer (CRUD wiring, the store
	// Deps in compose.go, factories, shapes, the drift guard) follows it.
	Package string `yaml:"package,omitempty"`
}

// PackageDir is the ORM's project-relative directory, slash-separated.
func (c ORMConfig) PackageDir() string {
	if p := strings.Trim(strings.TrimSpace(c.Package), "/"); p != "" {
		return path.Clean(p)
	}
	return DefaultORMPackage
}

// PackageName is the Go package name the ORM's files declare: the last
// element of PackageDir.
func (c ORMConfig) PackageName() string { return path.Base(c.PackageDir()) }

// ORMPackage returns the ORM placement of cfg, the default for a nil cfg —
// the one accessor every consumer reads, so a project with no forge.yaml
// and one with no `database.orm` block land in the same place.
func ORMPackage(cfg *ProjectConfig) ORMConfig {
	if cfg == nil {
		return ORMConfig{}
	}
	return cfg.Database.ORM
}

// MigrationSafetyConfig controls migrationlint's three severity dials
// (unsafe add-column, destructive change, volatile default). An intentional
// destructive migration is marked in the file itself with
// `-- forge:allow-destructive`; there is no forge.yaml allowlist.
type MigrationSafetyConfig struct {
	Enabled           *bool  `yaml:"enabled,omitempty"`            // nil = enabled
	UnsafeAddColumn   string `yaml:"unsafe_add_column,omitempty"`  // error, warn, off
	DestructiveChange string `yaml:"destructive_change,omitempty"` // error, warn, off
	VolatileDefault   string `yaml:"volatile_default,omitempty"`   // warn, error, off
	// RequireCompatDeclaration makes `forge lint --migration-safety` require
	// every migration after the baseline to declare `-- forge:backward-compatible
	// — <why>` or `-- forge:breaks-rollback <why>` (migration-compat-undeclared,
	// migration-compat-reason). Off by default.
	RequireCompatDeclaration bool `yaml:"require_compat_declaration,omitempty"`
}

// IsEnabled reports whether migration safety linting is on. Nil
// Enabled means "on by default" so opt-in is implicit.
func (c MigrationSafetyConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// EffectiveUnsafeAddColumn returns the configured severity for the
// unsafe-add-column rule, falling back to "error" when unset/invalid.
func (c MigrationSafetyConfig) EffectiveUnsafeAddColumn() string {
	return effectiveSeverity(c.UnsafeAddColumn, "error")
}

// EffectiveDestructiveChange returns the configured severity for the
// destructive-change rule, falling back to "error" when unset/invalid.
func (c MigrationSafetyConfig) EffectiveDestructiveChange() string {
	return effectiveSeverity(c.DestructiveChange, "error")
}

// EffectiveVolatileDefault returns the configured severity for the
// volatile-default rule, falling back to "warn" when unset/invalid.
func (c MigrationSafetyConfig) EffectiveVolatileDefault() string {
	return effectiveSeverity(c.VolatileDefault, "warn")
}

func effectiveSeverity(value, fallback string) string {
	switch strings.ToLower(value) {
	case "error", "warn", "off":
		return strings.ToLower(value)
	case "warning":
		return "warn"
	default:
		return fallback
	}
}

// CIConfig holds CI/CD settings. Every field here is an input to the
// ONE-TIME workflow render: .github/workflows/*.yml is scaffold-once,
// user-owned (Tier-2) and `forge generate` never re-renders it, so editing
// this block changes nothing until the workflow file is deleted.
//
// There is intentionally NO `go_version:` key either: every setup-go step in
// the generated workflows pins with `go-version-file: go.mod`, so CI tracks
// the project's own declared toolchain and cannot drift from it.
//
// There is intentionally NO `extra_jobs:` key. Adding a job to a YAML file
// you already own is one edit in .github/workflows/ci.yml; routing it
// through forge.yaml was a second source of truth that the workflow
// generator never read (the template's `range .ExtraJobs` was fed from a
// struct nothing populated), so declared jobs silently vanished.
type CIConfig struct {
	Provider    string       `yaml:"provider"`
	Lint        CILintConfig `yaml:"lint,omitempty"`
	Test        CITestConfig `yaml:"test,omitempty"`
	VulnScan    CIVulnConfig `yaml:"vuln_scan,omitempty"`
	E2E         CIE2EConfig  `yaml:"e2e,omitempty"`
	Permissions CIPermConfig `yaml:"permissions,omitempty"`
	// TestSkips tunes `forge ci verify-test-run` — the gate that reads a
	// `go test -json` stream and reports packages that skipped so much
	// their pass proves nothing. Absent (the common case) means the
	// shipped defaults; the block exists so a project can declare the
	// packages whose heavy skipping is legitimate.
	TestSkips CITestSkipsConfig `yaml:"test_skips,omitempty"`
	// Gate extends `forge ci gate` — the local gate behind `task ci` and
	// `task ci:checks` — with the project's own checks.
	Gate CIGateConfig `yaml:"gate,omitempty"`
}

// CIGateConfig is what a project adds to the checks forge owns in
// `forge ci gate`. Forge's own step list (verify-generated, lint, build,
// vuln-scan, license-check, validate-kcl, deployability) and the versions it
// pins are forge's: a project turns one off with CI_SKIP, which the summary
// still lists, or takes one over with an extra check that `replaces` it.
type CIGateConfig struct {
	// Env is set for every check of the gate, forge's and the project's
	// (GOPRIVATE for a private sibling module, a silencing flag).
	Env map[string]string `yaml:"env,omitempty"`
	// Path lists project-relative directories put first on every check's
	// PATH, ahead of forge's pinned tools: a project's own pinned toolchain
	// (`.tools/bin`). `requires` resolves against the same PATH.
	Path []string `yaml:"path,omitempty"`
	// AffectedIgnore lists files no Go test reads, as project-relative globs
	// (`*.md` matches by base name anywhere, `docs/**` a whole tree): a
	// change to one does not force the full unit suite when the gate tests
	// only the packages a branch affects. Any other non-Go change does.
	AffectedIgnore []string `yaml:"affected_ignore,omitempty"`
	// ExtraChecks are the project's own rows. By default each runs in the
	// `checks` lane, so the scaffolded CI job (`task ci:checks`) and a
	// developer's `task ci` run the same ones.
	ExtraChecks []CIGateCheck `yaml:"extra_checks,omitempty"`
}

// CIGateCheck is one project-owned check in `forge ci gate`.
//
// The gate runs checks concurrently: a check waits only for its `after`
// dependencies, a `heavy` one also for a host-wide heavy slot (pkg/hostslot),
// and a `mutates_tree` one runs alone. Every check still runs when another
// failed.
type CIGateCheck struct {
	// Name is the row in the summary table and the name CI_SKIP and CI_ONLY
	// match. Unique, and not the name of a check forge owns unless this
	// check `replaces` it.
	Name string `yaml:"name"`
	// Command is the argv to run (no shell), from the project root unless
	// Dir is set. Exit 0 is PASS.
	Command []string `yaml:"command"`
	// Dir is the working directory, project-relative.
	Dir string `yaml:"dir,omitempty"`
	// Env is added to the check's environment.
	Env map[string]string `yaml:"env,omitempty"`
	// Requires lists binaries that must be on PATH. A missing one makes the
	// check SKIPPED — which fails the gate unless CI_SKIP names it — rather
	// than a PASS for work that never ran.
	Requires []string `yaml:"requires,omitempty"`
	// Probe is a command that must succeed for the check to run (a docker
	// daemon that answers, a sibling checkout). When it fails the check is
	// SKIPPED with Probe.Reason.
	Probe *CIGateProbe `yaml:"probe,omitempty"`
	// SkipExitCode, when non-zero, is the exit status by which Command says
	// "I could not run here" (77, the automake convention, is the usual
	// choice): the row is SKIPPED with the command's last output line as the
	// reason, instead of FAIL.
	SkipExitCode int `yaml:"skip_exit_code,omitempty"`
	// Lane is the gate lane the check belongs to: checks (the default),
	// test, integration, or a lane of the project's own naming — which only
	// `forge ci gate <lane>` runs, never `all`.
	Lane string `yaml:"lane,omitempty"`
	// Group is a label CI_ONLY can select by (`CI_ONLY=kcl`). Default:
	// "extras".
	Group string `yaml:"group,omitempty"`
	// After names checks this one depends on: it starts once they have
	// finished (whatever their verdict), and selecting it with CI_ONLY
	// selects them too.
	After []string `yaml:"after,omitempty"`
	// Heavy makes the check hold a host-wide heavy slot while it runs, so a
	// shared machine never runs more heavy jobs than it has slots for. Set
	// it for whole-module builds, test runs, renders; leave it off for
	// linters of a few files and network calls.
	Heavy bool `yaml:"heavy,omitempty"`
	// MutatesTree marks a check that writes tracked files (a formatter, a
	// regenerate-and-diff): it runs alone, before every check that only
	// reads the tree, and when it fails on a tree that was clean the gate
	// restores the committed tree.
	MutatesTree bool `yaml:"mutates_tree,omitempty"`
	// Cache, when false, never reuses a cached PASS for this check: set it
	// for a check whose verdict depends on state outside the tree (the
	// network, a sibling checkout, today's advisories). Default true.
	Cache *bool `yaml:"cache,omitempty"`
	// Replaces takes over one of forge's own checks: this check runs in its
	// place, in its lane and position, and forge's command does not run.
	Replaces string `yaml:"replaces,omitempty"`
}

// CIGateProbe is a command whose success a check needs before it runs.
type CIGateProbe struct {
	Command []string `yaml:"command"`
	// Reason is the SKIPPED note when Command fails.
	Reason string `yaml:"reason,omitempty"`
}

// CILintConfig controls which linters run in CI.
type CILintConfig struct {
	Golangci        bool `yaml:"golangci"`         // default true
	Buf             bool `yaml:"buf"`              // default true
	BufBreaking     bool `yaml:"buf_breaking"`     // default true
	Frontend        bool `yaml:"frontend"`         // default true
	MigrationSafety bool `yaml:"migration_safety"` // default true
}

// CITestConfig controls test settings in CI.
type CITestConfig struct {
	Race     bool `yaml:"race"`     // default true
	Coverage bool `yaml:"coverage"` // default false
}

// CITestSkipsConfig tunes the silent-mass-skip gate.
//
// A Go suite that skips almost everything exits 0 and reads as green. Measured
// on one real package: 9 pass / 124 skip without DATABASE_URL, 103 pass / 1
// skip with it — same exit code, 7% of the tests. `forge ci verify-test-run`
// reads the run's own `go test -json` output and reports the packages whose
// pass therefore means nothing.
//
// Both numeric knobs default when absent (0 is not "disable it"); to switch
// the gate off, take the command out of the pipeline rather than setting a
// ratio of 1 and leaving a check that can never fire.
type CITestSkipsConfig struct {
	// MaxSkipRatio is the share of a package's tests that may skip before
	// it is reported. Default 0.5 — "more than half of this package did
	// not run". Range (0,1].
	MaxSkipRatio float64 `yaml:"max_skip_ratio,omitempty"`
	// MinTests is the sample-size floor below which a ratio is arithmetic
	// rather than evidence (1-of-2 is 50% and means nothing). Default 5.
	// It does not apply to a package that skipped EVERY test — "none of
	// them ran" is unambiguous at any size.
	MinTests int `yaml:"min_tests,omitempty"`
	// Allow declares the packages whose heavy skipping is expected, so
	// legitimate skips stop being noise. Each entry REQUIRES a reason.
	Allow []CITestSkipAllow `yaml:"allow,omitempty"`
}

// CITestSkipAllow is one declared exemption from the mass-skip gate.
//
// Reason is required and is not read by any logic: it exists to be read by a
// human in a code review. An exemption nobody had to justify is one nobody
// will revisit, which is how a suppression outlives the condition that earned
// it — the same contract as `forge project disown <path> --reason`.
type CITestSkipAllow struct {
	// Package matches the Go import path. Accepted spellings: the full
	// import path, the tail fragment people actually think in
	// ("internal/threads"), or either with a "/..." subtree suffix.
	Package string `yaml:"package"`
	// Reason is why this package legitimately skips. Required.
	Reason string `yaml:"reason"`
	// MaxSkipRatio is the share THIS package may skip. Absent means fully
	// exempt, which is the common case (an integration-only package on a
	// machine with no docker skips 100%, correctly, forever).
	MaxSkipRatio float64 `yaml:"max_skip_ratio,omitempty"`
}

// CIVulnConfig controls vulnerability scanning in CI.
type CIVulnConfig struct {
	Go     bool `yaml:"go"`     // govulncheck, default true
	Docker bool `yaml:"docker"` // trivy, default true
	NPM    bool `yaml:"npm"`    // npm audit, default true

	// Exemptions accepts specific advisory IDs that must not fail the
	// gate. It exists for exactly one situation: a vulnerability with no
	// fixed version, reached only through a code path the project does not
	// execute. Without it the only options are a red gate forever or
	// disabling the scan — and a disabled scan is how the NEXT advisory,
	// the reachable one, goes unnoticed.
	//
	// One list serves both scanners, and each entry's ID says which one it
	// belongs to: govulncheck reports GO-YYYY-NNNN ids, npm audit reports
	// GHSA-xxxx-xxxx-xxxx ids. An id neither scanner reports fails the gate,
	// since it can accept nothing.
	//
	// Scoped by ID on purpose: an exemption suppresses the advisories it
	// names and nothing else, so a new CVE in the very same module or
	// package still fails. See CIVulnExemption for the per-entry contract.
	Exemptions []CIVulnExemption `yaml:"exemptions,omitempty"`
}

// CIVulnExemption accepts one advisory ID against the vulnerability gate
// (govulncheck or npm audit), with the justification and the expiry date
// attached to it.
//
// Both extra fields are REQUIRED, and that is the whole design. An
// allowlist of bare IDs rots invisibly: nobody can tell a still-valid
// acceptance from one whose premise disappeared two upgrades ago, so the
// safe-looking action is always to leave it. Reason makes the claim
// reviewable, and Expires forces the re-review to happen on a date rather
// than never — an exemption past its expiry FAILS the gate exactly as if
// it were not listed.
//
// An entry that no longer matches anything is reported too. That is the
// good outcome (the advisory got a fix, or the dependency went away) and
// it should be deleted, not left behind to suppress a future finding
// nobody chose to accept.
type CIVulnExemption struct {
	// ID is the advisory ID exactly as the scanner printed it: a Go
	// advisory ID for govulncheck ("GO-2026-4887"), a GitHub advisory ID
	// for npm audit ("GHSA-vfj7-8cjw-p6xm"). The namespace routes the
	// entry to its scanner.
	ID string `yaml:"id"`
	// Reason states why this advisory cannot affect this project —
	// the unreachability argument, not a ticket number.
	Reason string `yaml:"reason"`
	// Expires is the YYYY-MM-DD date this acceptance stops being
	// honored, forcing a re-review.
	Expires string `yaml:"expires"`
}

// UsesDefaultScanners reports whether the project left the scanner
// selection unset, which by project convention means "all enabled".
//
// It deliberately ignores Exemptions: listing an accepted advisory says
// nothing about WHICH scanners should run, and letting it count as a
// selection would silently turn every unlisted scanner OFF as a side
// effect of accepting one CVE. This is why the config is no longer
// compared with `== CIVulnConfig{}` — adding the slice both broke that
// comparison at compile time and made it the wrong question.
func (c CIVulnConfig) UsesDefaultScanners() bool {
	return !c.Go && !c.Docker && !c.NPM
}

// CIE2EConfig controls end-to-end testing in CI.
type CIE2EConfig struct {
	Enabled bool   `yaml:"enabled"`           // default false
	Runtime string `yaml:"runtime,omitempty"` // "docker-compose" or "k3d"
}

// CIPermConfig controls CI workflow permissions.
type CIPermConfig struct {
	Contents string `yaml:"contents,omitempty"` // default "read"
}

// IsLintEnabled returns true if any linter is enabled.
// Zero value (all false) is treated as "all enabled" (sensible default).
func (c *CIConfig) IsLintEnabled() bool {
	return c.Lint == (CILintConfig{}) || c.Lint.Golangci || c.Lint.Buf || c.Lint.BufBreaking || c.Lint.Frontend || c.Lint.MigrationSafety
}

// IsTestRaceEnabled returns true if the race detector should be used.
// Zero value is treated as enabled.
func (c *CIConfig) IsTestRaceEnabled() bool {
	return c.Test == (CITestConfig{}) || c.Test.Race
}

// IsVulnScanEnabled returns true if any vulnerability scanner is enabled.
// No explicit selection is treated as "all enabled".
func (c *CIConfig) IsVulnScanEnabled() bool {
	return c.VulnScan.UsesDefaultScanners() || c.VulnScan.Go || c.VulnScan.Docker || c.VulnScan.NPM
}

// EffectivePermContents returns the contents permission, defaulting to "read".
func (c *CIConfig) EffectivePermContents() string {
	if c.Permissions.Contents != "" {
		return c.Permissions.Contents
	}
	return "read"
}

// DeployConfig holds deployment PIPELINE-CONTROL settings (target_arch,
// migration_test, concurrency, frontend_deploy) plus an input consumed by the
// CI-workflow generator (generator.CIWorkflows):
//
//   - the CI provider (github/gitlab/…) lives in `ci.provider`, not here
//     — the dead `deploy.provider` field was removed (see
//     removedSchemaKeys); generate_ci.go reads cfg.CI.Provider.
//   - there is no registry here: an image registry is declared in the env's
//     KCL and nowhere else (see refusedSchemaKeys).
//   - `deploy.environments` supplies optional per-env auto/protection/url
//     metadata for the generated deploy.yml. When empty, the deployable
//     environment set is derived from the on-disk deploy/kcl/<env>/
//     directories (generator.CIWorkflows' promotion-ordered fallback).
type DeployConfig struct {
	Environments   []DeployEnvConfig `yaml:"environments,omitempty"`
	Concurrency    DeployConcurrency `yaml:"concurrency,omitempty"`
	FrontendDeploy string            `yaml:"frontend_deploy,omitempty"` // "firebase", "vercel", "none"
	MigrationTest  bool              `yaml:"migration_test,omitempty"`  // test migrations before deploy

	// TargetArch is the GOARCH the deploy target cluster runs on. When
	// unset, forge defaults to amd64 (the predominant k8s host arch).
	// Setting this at the project level means Mac/arm64 dev machines
	// will cross-compile the Go binary (GOOS=linux GOARCH=<target>
	// CGO_ENABLED=0) and pass --platform=linux/<target> to docker
	// buildx so the image kubelet pulls actually runs on the node.
	//
	// Without cross-compile, an arm64-built image deployed onto an
	// amd64 node fails at pod startup with the opaque kernel-level
	// "exec format error". The CLI's --target-arch flag overrides
	// this per-invocation.
	TargetArch string `yaml:"target_arch,omitempty"`
}

// EffectiveTargetArch returns the deploy-target GOARCH. Order of
// precedence: explicit override (caller-provided), forge.yaml's
// deploy.target_arch, then the default "amd64". The "amd64" default
// reflects the empirical reality that the vast majority of k8s nodes
// are amd64; arm64 deployments must opt in via forge.yaml or
// --target-arch.
func (d *DeployConfig) EffectiveTargetArch(override string) string {
	if override != "" {
		return override
	}
	if d.TargetArch != "" {
		return d.TargetArch
	}
	return "amd64"
}

// DeployEnvConfig defines a deployment environment.
type DeployEnvConfig struct {
	Name       string `yaml:"name"`                 // staging, preprod, prod
	Auto       bool   `yaml:"auto,omitempty"`       // auto-deploy
	Protection bool   `yaml:"protection,omitempty"` // environment protection gates
	URL        string `yaml:"url,omitempty"`        // environment URL
}

// DeployConcurrency controls deployment concurrency settings.
type DeployConcurrency struct {
	Enabled          bool `yaml:"enabled"`                      // default true
	CancelInProgress bool `yaml:"cancel_in_progress,omitempty"` // default false
}

// IsConcurrencyEnabled returns true if deploy concurrency is enabled.
// Zero value is treated as enabled.
func (d *DeployConfig) IsConcurrencyEnabled() bool {
	return d.Concurrency == (DeployConcurrency{}) || d.Concurrency.Enabled
}

// (BuildConfig was removed: forge.yaml no longer carries any build
// config. Build is a per-service, per-env polymorphic declaration in KCL
// — see the GoBuild | DockerBuild | ShellBuild union on forge.Service.
// Version stamping moved to a KCL GoBuild.ldflags `-X` entry. A `build:`
// key in forge.yaml is now rejected as an unknown key.)

// DockerConfig holds Docker build configuration for the PROJECT image (the
// root `Dockerfile` built by `forge build`). Per-service DockerBuild services
// declared in KCL carry their OWN build_contexts on the KCL DockerBuild block;
// these are the project-image defaults / fallback.
//
// There is no registry here, and none on any env either: an image registry is
// part of a WORKLOAD's `image` in deploy/kcl/workloads.k. The project image is
// tagged and pushed to the reference the workload declaring it wrote, which is
// the same reference the runtime pulls.
//
// forge is FULLY base-image-AGNOSTIC: it does NOT discover, mirror, pin, or
// inject base images, and offers no mirror/pull-through setting. A Dockerfile's
// `FROM` lines are the COMPLETE source of truth — pin with `FROM …@sha256:…`,
// and route through a pull-through mirror by writing the mirror host into the
// `FROM` ref directly (e.g. `FROM us-docker.pkg.dev/<p>/dockerhub/alpine:3.21`).
// forge never rewrites a FROM.
type DockerConfig struct {
	// BuildContexts maps a build-context name to anything `docker buildx
	// --build-context name=value` accepts:
	//
	//   - A local filesystem path. Relative paths are resolved against the
	//     project root (the directory holding forge.yaml). The typical
	//     case is a sibling-checkout local replace directive (e.g. a
	//     `replace x => ../x` in go.mod where ../x lives outside the
	//     project's build context).
	//   - A `docker-image://<image>` ref. Passed through verbatim so a
	//     Dockerfile `FROM <name>` can be overridden with a specific image
	//     at build time (local override of a base image during dev,
	//     pin-by-digest in CI, etc.).
	//   - A GitSource mapping (repo, ref, optional subdir). The CLI resolves
	//     it through the shared source cache and explicit local overrides.
	//   - Any other scheme buildkit understands (e.g. `oci-layout://`,
	//     `https://`). Anything containing `://` is passed through
	//     unchanged.
	//
	// Each entry becomes a `--build-context name=value` arg to `docker
	// build`, letting Dockerfiles consume it via `FROM <name>` or
	// `COPY --from=<name>`. Empty when not set; existing projects with no
	// contexts see no change in build behaviour or output.
	BuildContexts map[string]BuildContext `yaml:"build_contexts,omitempty"`
}

// Typed-access enforcement levels for [ConfigGuardConfig.EnforceTypedAccess].
//
//   - off   — emit no env-reading guardrail at all.
//   - warn  — emit the forbidigo guardrail as an ADVISORY check: violations
//     are reported (during `forge lint` and in the generated .golangci.yml)
//     but never fail the build. This is the default for an ABSENT `config:`
//     block, so existing projects upgrade without a flag-day, and what
//     `forge project new --in-place` scaffolds when it adopts an existing
//     project, whose code already reads the environment directly.
//   - error — emit the forbidigo guardrail as a GATING check: violations
//     fail `forge lint` / CI. `forge project new` scaffolds this for greenfield
//     projects, which carry no legacy env-reading debt.
const (
	EnforceTypedAccessOff   = "off"
	EnforceTypedAccessWarn  = "warn"
	EnforceTypedAccessError = "error"
)

// DefaultLoaderPackage is the package path forge's config-loader codegen
// emits into a consuming project (generated from
// proto/<project>/config/v1/config.proto via the (forge.v1.config) annotation). It is
// the ONE package allowed to read the environment directly; the typed-access
// guardrail allowlists it so the generated loader can legitimately call
// os.Getenv / os.LookupEnv.
const DefaultLoaderPackage = "pkg/config"

// ConfigGuardConfig is the `config:` section of forge.yaml. It steers
// humans and LLMs away from reading the environment directly and toward
// forge's generated, dependency-injected typed config object.
//
// The block is OPTIONAL. When absent, EnforceTypedAccess resolves to "warn"
// (see [EnforceTypedAccessWarn]) so existing projects gain the advisory
// guardrail without a flag-day; `forge project new` writes an explicit
// `enforce_typed_access: error` for greenfield projects, and an explicit
// `warn` when `--in-place` adopts an existing project.
//
// ADOPTION / re-render: the strictness is projected into the generated
// .golangci.yml (forbidigo in `linters.enable` for error, settings-only for
// warn, absent for off). That file is a SCAFFOLD-ONCE, user-owned Tier-2
// artifact — `forge generate` never re-renders it, and `forge project upgrade` only
// auto-updates it when the on-disk copy is an unedited forge render (a
// verifying forge:hash marker). A freshly-scaffolded .golangci.yml carries no
// marker and is "user-owned from birth", so after changing
// enforce_typed_access in forge.yaml the user must explicitly re-render:
// `rm .golangci.yml && forge project upgrade` (re-scaffold), or `forge project upgrade
// --force` when they have no local .golangci.yml edits. This is the
// deliberate Tier-2 contract — forge will not silently stomp a hand-tuned
// linter config — not a bug. The forbidigo `msg`, the template's warn-mode
// comment, and `forge lint`'s advisory line all teach this path.
type ConfigGuardConfig struct {
	// EnforceTypedAccess selects the env-access guardrail strictness:
	// "off" | "warn" | "error". Empty resolves to "warn" — use
	// [ConfigGuardConfig.EffectiveEnforceTypedAccess], don't read the raw
	// string. Unknown values are rejected at load time (validate.go).
	EnforceTypedAccess string `yaml:"enforce_typed_access,omitempty"`
	// LoaderPackage is the allowlisted package path that may read the
	// environment directly — the home of forge's generated config loader.
	// Empty resolves to [DefaultLoaderPackage] ("pkg/config"); use
	// [ConfigGuardConfig.EffectiveLoaderPackage].
	LoaderPackage string `yaml:"loader_package,omitempty"`
}

// EffectiveEnforceTypedAccess returns the resolved guardrail strictness,
// defaulting an absent/empty value to "warn" (advisory). It normalizes case
// and the "warning" alias. Validation (validate.go) has already rejected
// any other non-empty value, so this never silently swallows a typo.
func (c ConfigGuardConfig) EffectiveEnforceTypedAccess() string {
	switch strings.ToLower(strings.TrimSpace(c.EnforceTypedAccess)) {
	case EnforceTypedAccessOff:
		return EnforceTypedAccessOff
	case EnforceTypedAccessError:
		return EnforceTypedAccessError
	case EnforceTypedAccessWarn, "warning":
		return EnforceTypedAccessWarn
	default:
		return EnforceTypedAccessWarn
	}
}

// EffectiveLoaderPackage returns the allowlisted loader package path,
// defaulting an absent/empty value to [DefaultLoaderPackage].
func (c ConfigGuardConfig) EffectiveLoaderPackage() string {
	if p := strings.TrimSpace(c.LoaderPackage); p != "" {
		return p
	}
	return DefaultLoaderPackage
}

// TypedAccessGuardEnabled reports whether the env-access guardrail should be
// emitted at all (true unless the strictness is "off").
func (c ConfigGuardConfig) TypedAccessGuardEnabled() bool {
	return c.EffectiveEnforceTypedAccess() != EnforceTypedAccessOff
}

// TypedAccessGuardGates reports whether the guardrail FAILS the build (true
// only in "error" mode; "warn" is advisory, "off" emits nothing).
func (c ConfigGuardConfig) TypedAccessGuardGates() bool {
	return c.EffectiveEnforceTypedAccess() == EnforceTypedAccessError
}

// LintConfig holds lint-related settings.
//
// There is intentionally NO `contract:` toggle here. Whether the contract
// lint runs is `features.contracts` — the flag the gate actually resolves;
// a second `lint.contract` boolean had no reader at all.
type LintConfig struct {
	Frontend FrontendLintConfig `yaml:"frontend,omitempty"`
	// HandlerFileMaxLOC is the per-file LOC threshold above which the
	// forgeconv-handler-file-size analyzer warns. Counts non-blank, non-
	// comment Go source lines under handlers/<svc>/*.go. A value of 0 (or
	// the field unset) is treated as the built-in default — see
	// [LintConfig.EffectiveHandlerFileMaxLOC] for the canonical value.
	HandlerFileMaxLOC int `yaml:"handler_file_max_loc,omitempty"`

	// Rules is the repo-wide severity dial for forge-owned lint rules:
	// rule ID → "off" | "warn" | "error". It is the COARSEST of the
	// three suppression levels (see internal/linter/suppress) and the
	// one to reach for last, because — like a path glob in
	// .golangci.yml — it silently swallows every future finding for
	// that rule, including the one you would have wanted to see.
	//
	// Prefer a file or line directive when the exemption is local. This
	// map is for rules that genuinely do not apply to the whole project
	// (a CLI has no handlers; an operator has no frontend), where the
	// alternative is annotating every file.
	//
	// The `warn` setting is the load-bearing one: it lets a project
	// adopt a newly-added opinionated rule incrementally instead of
	// choosing between "fails the build today" and "off".
	//
	// Only forge's OWN rules are addressable here. golangci-lint,
	// eslint, stylelint and buf each have their own config file and
	// their own suppression syntax, and forge deliberately does not
	// proxy them — a second place to configure the same linter is a
	// place for the two to disagree.
	//
	// The special key "*" applies to every forge rule; an exact rule ID
	// beats it.
	Rules map[string]string `yaml:"rules,omitempty"`

	// Tracing configures the tracing lane (`forge lint --tracing`).
	Tracing TracingLintConfig `yaml:"tracing,omitempty"`

	// AllowSkip accepts, by name and with a reason, a gating lane that
	// could NOT run. Such a lane fails `forge lint` by default — a check
	// that did not run is not a check that passed — so this is the
	// project-level opt-out, the forge.yaml twin of `forge lint
	// --allow-skip`. An accepted lane is still listed as SKIPPED (allowed)
	// in the verdict, with its reason. It accepts only "could not run": a
	// lane that ran and found problems still fails.
	AllowSkip []LintSkipAllowance `yaml:"allow_skip,omitempty"`
}

// LintSkipAllowance is one lint.allow_skip entry. Read by the lint
// command (internal/cli/lint/lint_skip.go), which refuses an entry with an
// unknown lane or an empty reason.
type LintSkipAllowance struct {
	// Lane is the lane's name as the verdict prints it ("frontend lint")
	// or its flag spelling ("frontend-lint").
	Lane string `yaml:"lane"`
	// Reason says why the lane may not run here. Required.
	Reason string `yaml:"reason"`
}

// TracingLintConfig configures `forge lint --tracing`.
type TracingLintConfig struct {
	// LogWrappers names the project's own context-free logging helpers —
	// a `logging.Info(msg, args...)` that calls slog.Info — so
	// forge-tracing-log-without-ctx flags and rewrites their calls the way
	// it does slog's own.
	LogWrappers []LogWrapperConfig `yaml:"log_wrappers,omitempty"`
}

// LogWrapperConfig is one context-free logging helper and its context-taking
// replacement.
type LogWrapperConfig struct {
	// Call is the helper, as "<import path>.<Func>":
	// github.com/acme/app/internal/logging.Info.
	Call string `yaml:"call"`
	// ContextCall is what a call is rewritten to, as "<import path>.<Func>",
	// taking the context first and then the helper's arguments unchanged
	// (log/slog.InfoContext). Empty means "<Call>Context" in the helper's
	// own package.
	ContextCall string `yaml:"context_call,omitempty"`
}

// ValidateRules reports rule-severity entries whose SEVERITY forge cannot
// interpret ("loud" rather than off / warn / error).
//
// `forge lint` reports each as an error (the lint.rules check,
// internal/cli/lint/lint_rules.go), together with every KEY it cannot honour —
// a rule ID no lane emits, another tool's rule, a lane's could-not-run
// headline. The key half lives with the lanes, because only they know their
// rule IDs; it can be strict because `forge lint` runs under the project's
// pinned forge, so the key set is exactly the one that forge grades.
func (c LintConfig) ValidateRules() []string {
	if len(c.Rules) == 0 {
		return nil
	}
	var problems []string
	for rule, spec := range c.Rules {
		if !suppress.ValidSeverity(spec) {
			problems = append(problems, fmt.Sprintf(
				"lint.rules[%q]: %q is not a severity — use \"off\", \"warn\", or \"error\"",
				rule, spec))
		}
	}
	sort.Strings(problems)
	return problems
}

// EffectiveRules returns the rule-severity map in the form the linters
// consume, with keys lowercased so forge.yaml is not case-sensitive
// about rule IDs.
func (c LintConfig) EffectiveRules() suppress.RuleSeverities {
	if len(c.Rules) == 0 {
		return nil
	}
	out := make(suppress.RuleSeverities, len(c.Rules))
	for rule, spec := range c.Rules {
		out[strings.ToLower(strings.TrimSpace(rule))] = spec
	}
	return out
}

// DefaultHandlerFileMaxLOC is the built-in threshold used by the
// forgeconv-handler-file-size analyzer when the project does not set
// lint.handler_file_max_loc in forge.yaml. Picked at 1000 because that
// roughly tracks "two screens of any modern editor" plus generous
// buffer — files past that point materially harm review velocity and
// almost always benefit from the per-RPC split that `forge scaffold
// handler-file` is intended to support.
const DefaultHandlerFileMaxLOC = 1000

// EffectiveHandlerFileMaxLOC returns the LOC threshold for the
// handler-file-size analyzer, defaulting to [DefaultHandlerFileMaxLOC]
// when the config value is zero or unset.
func (c LintConfig) EffectiveHandlerFileMaxLOC() int {
	if c.HandlerFileMaxLOC <= 0 {
		return DefaultHandlerFileMaxLOC
	}
	return c.HandlerFileMaxLOC
}

// FrontendLintConfig configures the frontend slice of `forge lint`:
// whether the stylelint-backed CSS health checks run, and which
// severity ("error"/"warn"/"off") the `no-important`,
// `no-inline-styles`, and `typecheck` rules use.
type FrontendLintConfig struct {
	CSSHealth      bool   `yaml:"css_health,omitempty"`       // enable stylelint-backed CSS health checks
	NoImportant    string `yaml:"no_important,omitempty"`     // error, warn, off
	NoInlineStyles string `yaml:"no_inline_styles,omitempty"` // error, warn, off
	// Typecheck is the severity of the TypeScript typecheck lane
	// ("error" | "warn" | "off"); see [FrontendLintConfig.EffectiveTypecheck].
	Typecheck string `yaml:"typecheck,omitempty"`
}

// EffectiveTypecheck returns the configured severity for the frontend
// TypeScript typecheck lane, defaulting to "error" when unset or invalid.
//
// The default gates on purpose: a type error is a real defect that the Go
// half of the pipeline can never see, and shipping it is exactly the class
// of break this lane exists to catch. Projects that want the signal
// without the gate set "warn"; "off" removes the lane entirely.
//
// Note this dial governs TYPE ERRORS only. A typecheck that could not RUN
// (deps not installed, no TypeScript resolvable) is always reported as a
// warning — never silently as a pass — and escalates to an error under
// `forge lint --strict`.
func (c FrontendLintConfig) EffectiveTypecheck() string {
	return effectiveSeverity(c.Typecheck, "error")
}

// EffectiveNoImportant returns the configured severity for the
// no-important rule, falling back to "warn" when unset/invalid.
func (c FrontendLintConfig) EffectiveNoImportant() string {
	return effectiveSeverity(c.NoImportant, "warn")
}

// EffectiveNoInlineStyles returns the configured severity for the
// no-inline-styles rule, falling back to "warn" when unset/invalid.
func (c FrontendLintConfig) EffectiveNoInlineStyles() string {
	return effectiveSeverity(c.NoInlineStyles, "warn")
}

// ContractsConfig controls contract enforcement linter behavior.
//
// It carries only the two ESCAPE HATCHES the analyzer reads: which
// packages opt out (`exclude`) and which project-local types the mock
// generator must treat as interfaces (`interface_types`). The old
// severity dials (`strict`, `allow_exported_vars`, `allow_exported_funcs`)
// are gone: the contract rules are unconditional — a package either opts
// out by path or obeys them — and no analyzer ever read those three
// booleans. Whether the lint runs at all is `features.contracts`.
type ContractsConfig struct {
	Exclude []string `yaml:"exclude"` // packages that opt out
	// InterfaceTypes lists additional cross-package interface types (over
	// and above the built-in list in internal/generator/contract) that the
	// mock generator should treat as mockable — i.e. emit "nil" as the
	// fallback zero value instead of the invalid composite literal "T{}".
	//
	// Entries are matched against the rendered Go type expression of a
	// contract method's return value, e.g. "billing.MeterClient" or
	// "myproject.SomeProjectLocalInterface". Use this when a contract
	// method returns a project-local interface that the mock generator
	// would otherwise mistakenly treat as a struct.
	InterfaceTypes []string `yaml:"interface_types"`
}

// IsExcluded returns true if the given package path matches any
// exclude pattern. Delegates to [MatchExclude] — the shared matcher
// used by the contract analyzer and the forgeconv lint surface so all
// three places agree on what "excluded" means. See the doc on
// MatchExclude for the matching rules and the deliberate exit from the
// pre-2026-06 inline implementation (empty-pattern handling +
// slash-normalisation).
func (c ContractsConfig) IsExcluded(pkgPath string) bool {
	return MatchExclude(c.Exclude, pkgPath)
}

// FeaturesConfig is the resolved on/off state of each forge subsystem
// (codegen, orm, deploy, ...).
//
// Every feature DERIVES from what exists in the repository — whether
// db/migrations exists, whether a frontend exists, whether deploy/kcl
// declares a gateway, whether internal/operators exists, whether internal/db
// carries a `//forge:no-orm` marker. See DeriveFeatureDefaults for the rule
// per feature.
//
// A derived default is still only a default. The `features:` block pins one
// on or off whatever the tree says:
//
//	features:
//	  observability: false   # no generated dashboards, even with internal/handlers/
//	  hot_reload: false
//
// That is the switch a structural change needs. Creating internal/handlers/
// makes a CLI-shaped project server-shaped, and observability follows the
// server's sources; a project that adopts forge's handler model without
// wanting forge's dashboards says so here, once, and no later change to the
// tree turns them back on. A pinned value is never normalized away on write,
// even when it matches what the tree derives today: matching today is not
// the same as meaning it tomorrow. A derived feature whose requirement is
// pinned off follows it off (ingress with deploy, orm with codegen); a
// pinned feature whose requirement is off is a load error.
//
// The ORM is the one feature with no key: its switch is `//forge:no-orm` in
// internal/db's doc comment, beside the code it affects, and a second
// spelling here would be a second source of truth.
//
// A FeaturesConfig not produced by the config loader (the zero value, or one
// hand-built in a test) resolves every feature to "enabled" except the
// default-off ones; With overrides a single feature on such a value.
type FeaturesConfig struct {
	Codegen       *bool `yaml:"codegen,omitempty"`
	Migrations    *bool `yaml:"migrations,omitempty"`
	CI            *bool `yaml:"ci,omitempty"`
	Build         *bool `yaml:"build,omitempty"`
	Contracts     *bool `yaml:"contracts,omitempty"`
	Frontend      *bool `yaml:"frontend,omitempty"`
	Observability *bool `yaml:"observability,omitempty"`
	HotReload     *bool `yaml:"hot_reload,omitempty"`
	Deploy        *bool `yaml:"deploy,omitempty"`
	Ingress       *bool `yaml:"ingress,omitempty"`
	Operators     *bool `yaml:"operators,omitempty"`

	// derived is the loader-resolved state of every feature. nil (zero value)
	// falls back to the historical "absent = enabled" semantics.
	derived map[FeatureName]bool
}

// pinned returns the forge.yaml `features:` value for name, and whether the
// block sets it at all.
func (f FeaturesConfig) pinned(name FeatureName) (on, ok bool) {
	var p *bool
	switch name {
	case FeatureCodegen:
		p = f.Codegen
	case FeatureMigrations:
		p = f.Migrations
	case FeatureCI:
		p = f.CI
	case FeatureBuild:
		p = f.Build
	case FeatureContracts:
		p = f.Contracts
	case FeatureFrontend:
		p = f.Frontend
	case FeatureObservability:
		p = f.Observability
	case FeatureHotReload:
		p = f.HotReload
	case FeatureDeploy:
		p = f.Deploy
	case FeatureIngress:
		p = f.Ingress
	case FeatureOperators:
		p = f.Operators
	}
	if p == nil {
		return false, false
	}
	return *p, true
}

// Pin returns a copy with name pinned on or off, as a `features:` entry in
// forge.yaml would. ok is false for a feature with no key (orm, whose switch
// is //forge:no-orm) or an unknown name; the copy is then unchanged.
func (f FeaturesConfig) Pin(name FeatureName, on bool) (_ FeaturesConfig, ok bool) {
	v := on
	switch name {
	case FeatureCodegen:
		f.Codegen = &v
	case FeatureMigrations:
		f.Migrations = &v
	case FeatureCI:
		f.CI = &v
	case FeatureBuild:
		f.Build = &v
	case FeatureContracts:
		f.Contracts = &v
	case FeatureFrontend:
		f.Frontend = &v
	case FeatureObservability:
		f.Observability = &v
	case FeatureHotReload:
		f.HotReload = &v
	case FeatureDeploy:
		f.Deploy = &v
	case FeatureIngress:
		f.Ingress = &v
	case FeatureOperators:
		f.Operators = &v
	default:
		return f, false
	}
	return f, true
}

// Pinned reports whether forge.yaml's `features:` block sets name, rather
// than the tree deriving it.
func (f FeaturesConfig) Pinned(name FeatureName) bool {
	_, ok := f.pinned(name)
	return ok
}

// With returns a copy with one feature forced on or off. The scaffold uses it
// to describe a project that does not exist on disk yet (a CLI or library
// has no codegen); nothing persists it, because the next load derives the
// same answer from the tree the scaffold wrote.
func (f FeaturesConfig) With(name FeatureName, on bool) FeaturesConfig {
	next := make(map[FeatureName]bool, len(f.derived)+1)
	if f.derived == nil {
		for n := range featureDeps {
			next[n] = !defaultOffFeatures[n]
		}
		for _, n := range allFeatureNames {
			next[n] = !defaultOffFeatures[n]
		}
	} else {
		for k, v := range f.derived {
			next[k] = v
		}
	}
	next[name] = on
	f.derived = next
	return f
}

// resolve resolves a feature by name: the loader-derived value when there is
// one, else the historical "absent = enabled" default (zero value
// FeaturesConfig, hand-constructed in tests, no forge.yaml context). All
// public XxxEnabled() accessors are thin wrappers over this.
func (f FeaturesConfig) resolve(name FeatureName) bool {
	if on, ok := f.pinned(name); ok {
		return on
	}
	if f.derived == nil {
		// No derivation context: fall back to "absent = enabled", except for
		// the features that are default-off by construction. ingress and
		// operators have no shape that implies them, so "absent" means "this
		// project does not use it".
		return !defaultOffFeatures[name]
	}
	return f.derived[name]
}

// defaultOffFeatures are the features whose absent state is OFF rather than
// ON. DeriveFeatureDefaults says the same thing for a config that DID go
// through the loader; this covers the one that did not.
var defaultOffFeatures = map[FeatureName]bool{
	FeatureIngress:   true,
	FeatureOperators: true,
}

// EffectiveKind returns the project kind, defaulting to "service".
func (c ProjectConfig) EffectiveKind() string {
	return EffectiveProjectKind(c.Kind)
}

// EffectiveBinary returns the binary mode, defaulting to "per-service"
// so legacy forge.yaml files without the field keep producing the
// canonical cmd/server.go shape.
func (c ProjectConfig) EffectiveBinary() string {
	return EffectiveProjectBinary(c.Binary)
}

// IsBinaryShared reports whether the project uses the shared-binary
// codegen mode (one Go binary, cobra subcommand per service, KCL
// MultiServiceApplication for deploy).
func (c ProjectConfig) IsBinaryShared() bool {
	return c.EffectiveBinary() == ProjectBinaryShared
}

// EffectiveForgeVersion returns the forge version pinned by this project,
// defaulting to "0.0.0" for legacy projects that predate the field.
// Callers can use the "0.0.0" sentinel to detect "no baseline yet" and
// nudge the user toward `forge project upgrade`.
func (c ProjectConfig) EffectiveForgeVersion() string {
	if strings.TrimSpace(c.ForgeVersion) == "" {
		return "0.0.0"
	}
	return c.ForgeVersion
}

// IsCLIKind reports whether the project is a CLI binary (no server scaffolding).
func (c ProjectConfig) IsCLIKind() bool { return c.EffectiveKind() == ProjectKindCLI }

// IsLibraryKind reports whether the project is a pure Go library (no cmd/).
func (c ProjectConfig) IsLibraryKind() bool { return c.EffectiveKind() == ProjectKindLibrary }

// IsServiceKind reports whether the project is a Connect-RPC service.
func (c ProjectConfig) IsServiceKind() bool { return c.EffectiveKind() == ProjectKindService }

// ORMEnabled reports whether the ORM feature is on (default: on).
func (f FeaturesConfig) ORMEnabled() bool { return f.resolve(FeatureORM) }

// CodegenEnabled reports whether codegen is on (default: on).
func (f FeaturesConfig) CodegenEnabled() bool { return f.resolve(FeatureCodegen) }

// MigrationsEnabled reports whether the migrations feature is on (default: on).
func (f FeaturesConfig) MigrationsEnabled() bool { return f.resolve(FeatureMigrations) }

// CIEnabled reports whether the CI feature is on (default: on).
func (f FeaturesConfig) CIEnabled() bool { return f.resolve(FeatureCI) }

// DeployEnabled reports whether the deploy feature is on. Stable flag:
// absent derives from the tree (deploy ⇔ deploy/kcl exists — see
// DeriveFeatureDefaults), explicit `features.deploy: true|false` wins.
// Service scaffolds ship a deploy/kcl tree, so deploy is ON for the
// canonical service shape; a tree without one derives OFF.
func (f FeaturesConfig) DeployEnabled() bool { return f.resolve(FeatureDeploy) }

// ContractsEnabled reports whether contract enforcement is on (default: on).
func (f FeaturesConfig) ContractsEnabled() bool { return f.resolve(FeatureContracts) }

// FrontendEnabled reports whether the frontend feature is on (default: on).
func (f FeaturesConfig) FrontendEnabled() bool { return f.resolve(FeatureFrontend) }

// ObservabilityEnabled reports whether the observability feature is on (default: on).
func (f FeaturesConfig) ObservabilityEnabled() bool { return f.resolve(FeatureObservability) }

// HotReloadEnabled reports whether the hot-reload feature is on (default: on).
func (f FeaturesConfig) HotReloadEnabled() bool { return f.resolve(FeatureHotReload) }

// BuildEnabled reports whether `forge build` is enabled (default: on).
// Direct `forge build` invocations error when off; orchestrators like
// `forge env up` log a skip line and continue.
func (f FeaturesConfig) BuildEnabled() bool { return f.resolve(FeatureBuild) }

// IngressEnabled reports whether Gateway API ingress is wired
// (`features.ingress`; derives to false). When off, forge skips ingress
// codegen, `forge cluster up` skips the Envoy Gateway + GatewayClass
// install, `forge cluster urls` returns nothing, and the audit ingress
// category is suppressed.
func (f FeaturesConfig) IngressEnabled() bool { return f.resolve(FeatureIngress) }

// OperatorsEnabled reports whether controller-runtime operator codegen
// + CRD manifest generation is wired (`features.operators`; derives to
// false). When off, the operator binary codegen + CRD scaffold steps skip
// silently and `forge scaffold operator` errors.
func (f FeaturesConfig) OperatorsEnabled() bool { return f.resolve(FeatureOperators) }

// DisabledFeatureError returns the canonical user-facing error for a
// disabled feature. Centralised so every gate site emits the same
// wording — sub-agents and humans grepping for the string find one
// authoritative format. The name argument is the lowercased feature
// name (e.g. "deploy", "build", "frontend").
func DisabledFeatureError(name string) error {
	return errDisabledFeature{name: name}
}

// errDisabledFeature carries the feature name so callers can match
// programmatically (errors.As) without parsing the string. The Error()
// shape matches forge's existing single-line "feature 'X' is disabled in
// forge.yaml" idiom used by the pre-feature-block gates in deploy.go,
// docs.go and ci.go.
type errDisabledFeature struct {
	name string
}

func (e errDisabledFeature) Error() string {
	return "feature '" + e.name + "' is off for this project. Features are derived from the repository unless forge.yaml's `features:` block pins them — `forge project features` shows why each one is on or off."
}

// FeatureName is the canonical feature key. Stays a string alias so the
// constants below are usable directly anywhere the feature name shows up,
// e.g. in the strings emitted by `forge project audit --json | jq '.features'`.
type FeatureName = string

// Feature name constants — the wire format of `forge project audit --json`
// and `forge project features`.
const (
	FeatureORM           FeatureName = "orm"
	FeatureCodegen       FeatureName = "codegen"
	FeatureMigrations    FeatureName = "migrations"
	FeatureCI            FeatureName = "ci"
	FeatureBuild         FeatureName = "build"
	FeatureContracts     FeatureName = "contracts"
	FeatureFrontend      FeatureName = "frontend"
	FeatureObservability FeatureName = "observability"
	FeatureHotReload     FeatureName = "hot_reload"
	FeatureDeploy        FeatureName = "deploy"
	FeatureIngress       FeatureName = "ingress"
	FeatureOperators     FeatureName = "operators"
)

// allFeatureNames lists every feature in display order.
var allFeatureNames = []FeatureName{
	FeatureCodegen, FeatureORM, FeatureMigrations, FeatureContracts,
	FeatureFrontend, FeatureObservability, FeatureHotReload, FeatureCI,
	FeatureBuild, FeatureDeploy, FeatureIngress, FeatureOperators,
}

// FeatureNames returns every feature name in display order.
func FeatureNames() []FeatureName { return append([]FeatureName(nil), allFeatureNames...) }

// EffectiveFeatures projects the resolved enabled/disabled state of every
// feature into a stable name→bool map, for `forge project audit` and tests.
func (f FeaturesConfig) EffectiveFeatures() map[string]bool {
	out := make(map[string]bool, len(allFeatureNames))
	for _, n := range allFeatureNames {
		out[n] = f.resolve(n)
	}
	return out
}

// APIConfig holds project-level API protocol-skin toggles. Both fields
// default to false, so projects that omit the `api:` block continue to
// expose only the canonical Connect/gRPC handlers without any runtime
// transcoding or generated spec files.
//
// REST=true installs connectrpc.com/vanguard as middleware in front of
// the Connect mux. Vanguard transcodes REST↔Connect at runtime based on
// `google.api.http` annotations on RPCs; the CRUD proto scaffolder also
// emits standard REST-shaped annotations on Get/List/Create/Update/Delete
// RPCs so the default CRUD surface gains REST URLs without hand-editing.
//
// OpenAPI=true is owned by a sibling agent and emits an OpenAPI spec
// alongside the proto compile step. The two fields compose: with both
// on, the generated spec reflects the REST URLs.
type APIConfig struct {
	REST    bool `yaml:"rest,omitempty"`
	OpenAPI bool `yaml:"openapi,omitempty"`

	// ProtoPackage is the proto `package` a SCAFFOLDED service declares
	// (`forge scaffold service`, `forge project new --service`). Either a
	// fixed package every service shares ("controlplane.v1") or a pattern
	// carrying the {service} placeholder, which expands to the snake_case
	// service name ("acme.{service}.v1" → "acme.billing.v1").
	//
	// Absent is the normal case: forge then infers the package from the
	// project's existing service protos (a strict-majority convention) and
	// falls back to "services.{service}.v1" — see
	// generator.ResolveServiceProtoPackage. Set it only to override that
	// inference, e.g. on a project with no services yet. Scaffold-time only:
	// it never rewrites a proto that already exists. go_package is not
	// affected — every service keeps its own gen/services/<svc>/v1 Go
	// package whatever its wire package.
	ProtoPackage string `yaml:"proto_package,omitempty"`
}

// ProtoPackageServicePlaceholder is the token in api.proto_package that
// expands to the scaffolded service's snake_case name.
const ProtoPackageServicePlaceholder = "{service}"

// K8sConfig holds Kubernetes configuration.
type K8sConfig struct {
	KCLDir string `yaml:"kcl_dir"`
}

// ObservabilityConfig seeds the OWNED per-package observe_chain.go seam (the
// in-process component middleware chain the generated decorator routes
// through). It is a SCAFFOLD-TIME default: `forge scaffold` reads it to
// stamp the initial chain; the seam is user-owned afterward, so editing this
// block does not retro-rewrite existing seams — it only changes what newly
// scaffolded packages start with.
type ObservabilityConfig struct {
	// LogLevel is the slog level at which the LogMiddleware records
	// SUCCESSFUL component calls ("debug" | "info" | "warn" | "error").
	// Every success is recorded unless the seam appends
	// observe.WithSuccessSampling to LogMiddleware, which samples them per
	// method. Failures always log at Error regardless. Default "debug"
	// keeps success logging quiet under a production Info handler. An
	// unknown value falls back to the default.
	LogLevel string `yaml:"log_level,omitempty"`
}

// SlogLevelExpr resolves LogLevel to the Go expression for the matching
// slog.Level constant, used to stamp the observe_chain.go seam. Unknown or
// empty values default to slog.LevelDebug (quiet-on-success).
func (o ObservabilityConfig) SlogLevelExpr() string {
	switch strings.ToLower(strings.TrimSpace(o.LogLevel)) {
	case "info":
		return "slog.LevelInfo"
	case "warn", "warning":
		return "slog.LevelWarn"
	case "error":
		return "slog.LevelError"
	default:
		return "slog.LevelDebug"
	}
}
