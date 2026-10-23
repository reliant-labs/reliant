package generator

import (
	"fmt"
	"go/format"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/templates"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// UpgradeStatus describes the outcome for each managed file.
type UpgradeStatus string

// The per-file outcomes an upgrade can report.
const (
	UpgradeUpToDate     UpgradeStatus = "up-to-date"
	UpgradeUpdated      UpgradeStatus = "updated"
	UpgradeUserModified UpgradeStatus = "user-modified"
	UpgradeSkipped      UpgradeStatus = "skipped"
)

// UpgradeResult holds the outcome for a single managed file.
type UpgradeResult struct {
	Path   string        // relative path in project (e.g. "cmd/server.go")
	Status UpgradeStatus // what happened
	Diff   string        // unified-style diff when file changed
	// Forced is true when this file was (or would be) written only
	// because the force selection covered it — i.e. the user's edits
	// were discarded. Refreshing a pristine render never sets it.
	Forced bool
	// Missing and Local size the delta the same way the advisory lane
	// does (lineDelta): template lines this copy lacks, and lines this
	// copy has that no template line accounts for.
	//
	// They exist so the report can state the SIZE of a difference
	// without printing the difference. A file two lines behind with
	// nothing of its own is a one-command adopt; one carrying forty
	// local lines is a merge done by hand. That gap is the report's
	// ranking, and it cannot be read off a status word.
	Missing int
	Local   int
	// Absent marks a managed file the template ships that this project
	// does not have on disk. Adoption is a pure add — it destroys
	// nothing — which is a different (and cheaper) act than refreshing
	// a file that exists, so the report ranks it separately.
	Absent bool
}

// ForceSelection names which user-modified files an upgrade run is allowed to
// overwrite.
//
// Force is per-path because adopting a template is per-path. A project
// accumulates edits at different times for different reasons: one file was
// customized on purpose, another drifted because its provenance predates the
// self-certifying marker. A single project-wide switch makes adopting the
// second impossible without stomping the first, so the only safe move is to
// run nothing — which is how a project stops upgrading at all.
//
// The zero value forces nothing.
type ForceSelection struct {
	// all overwrites every user-modified managed file (bare --force).
	all bool
	// paths is the explicit set of project-relative paths to overwrite.
	paths map[string]bool
}

// ForceNone returns a selection that overwrites nothing the user has touched.
func ForceNone() ForceSelection { return ForceSelection{} }

// ForceAll returns a selection that overwrites every user-modified managed
// file — the whole-project meaning of a bare --force.
func ForceAll() ForceSelection { return ForceSelection{all: true} }

// ForcePaths returns a selection that overwrites exactly the named
// project-relative paths and nothing else.
func ForcePaths(paths ...string) ForceSelection {
	sel := ForceSelection{paths: make(map[string]bool, len(paths))}
	for _, p := range paths {
		sel.paths[forceKey(p)] = true
	}
	return sel
}

// Allows reports whether relPath may have the user's edits overwritten.
func (f ForceSelection) Allows(relPath string) bool {
	if f.all {
		return true
	}
	return f.paths[forceKey(relPath)]
}

// Names reports whether relPath was named EXPLICITLY — the whole-project
// form does not satisfy it.
//
// The scaffold-once advisory lane (upgrade_advisory.go) gates adoption on
// this rather than on Allows. Those files are the user's from birth, so
// "overwrite everything you have edited" cannot be a statement about them:
// a flag that names nothing carries no intent about a file forge already
// handed over. Naming the path is the intent.
func (f ForceSelection) Names(relPath string) bool { return f.paths[forceKey(relPath)] }

// forceKey is the comparable form of a project-relative path: cleaned and
// slash-separated, the form destPath takes. The user's argument may arrive
// either way on Windows (`forge project upgrade --force cmd\x\cmd\root.go`),
// and both spellings must name the same file.
func forceKey(relPath string) string { return path.Clean(filepath.ToSlash(relPath)) }

// Any reports whether the selection can overwrite anything at all.
func (f ForceSelection) Any() bool { return f.all || len(f.paths) > 0 }

// File ownership tiers.
const (
	// Tier1 files are always overwritten by forge generate and gitignored.
	// These are pure infrastructure, 100% derivable from forge.yaml.
	Tier1 = 1
	// Tier2 files are checksum-protected and committed to git.
	// Overwritten only if the user hasn't modified them.
	Tier2 = 2
)

// managedFile describes a frozen file that upgrade tracks.
//
// enabledFor is the file's own gate: it reports whether this file applies
// to a given project config. A nil predicate means "always included" (the
// backwards-compatible default for files no gate touched). Co-locating the
// gate with the manifest entry replaces the old fileEnabledByFeatures
// path-prefix string-matching switch — the gating model is now declared
// once, on the entry, instead of being doubly modeled (method calls in the
// scaffold lane, path-prefix matching here).
type managedFile struct {
	templateName string // template name in project/ dir (e.g. "cmd-server.go.tmpl")
	destPath     string // relative destination path (e.g. "cmd/server.go")
	templated    bool   // true if template needs data rendering
	tier         int    // 1 = always overwrite (gitignored), 2 = checksum-protected
	// enabledFor gates inclusion of this file for a given project config.
	// nil ⇒ always included.
	enabledFor func(cfg *config.ProjectConfig) bool
	// render, when non-nil, supplies the file's bytes instead of
	// templateName resolving against the project/ template category.
	//
	// It exists for managed files whose template lives in ANOTHER
	// registry: the per-frontend eslint.config.mjs is one template per
	// frontend KIND under internal/templates/frontend/<kind>/, written to
	// one path per FRONTEND. templateName cannot name that (it is not a
	// project/ template) and destPath cannot be fixed at list-build time
	// (it depends on which frontends the project declares), so the entries
	// are generated per frontend with their render attached.
	render func() ([]byte, error)
}

// cfgIsService reports whether the project config is a service-kind project
// (the canonical default when cfg is nil). CLI and library projects don't
// ship the Connect-server stack (cmd/*, pkg/middleware/*, Dockerfile,
// docker-compose, alloy-config), so the service-shape files gate on this.
//
// The SCAFFOLD always emits these files for service-kind (deploy derives on
// for service projects, and even a `features.deploy: false` project keeps
// the tree on disk). upgrade therefore also manages them for every
// service-kind project — gating on the flag would strand opted-out
// scaffolds with un-upgradable Dockerfiles.
func cfgIsService(cfg *config.ProjectConfig) bool {
	kind := config.ProjectKindService
	if cfg != nil {
		kind = cfg.EffectiveKind()
	}
	return kind == config.ProjectKindService
}

// enabledForService gates a file on the project being service-kind.
func enabledForService(cfg *config.ProjectConfig) bool { return cfgIsService(cfg) }

// enabledForObservability gates a file on the project being service-kind
// AND having observability enabled (e.g. deploy/observability/otel-collector.yaml).
func enabledForObservability(cfg *config.ProjectConfig) bool {
	return cfgIsService(cfg) && cfg != nil && cfg.Features.ObservabilityEnabled()
}

// fileEnabledByFeatures reports whether a managed file should be included
// given the current feature flags AND project kind. The decision now lives
// on the manifest entry's enabledFor predicate; a nil predicate means the
// file is always included (backwards-compatible default).
func fileEnabledByFeatures(f managedFile, cfg *config.ProjectConfig) bool {
	if f.enabledFor == nil {
		return true
	}
	return f.enabledFor(cfg)
}

// filterManagedFiles returns only the managed files whose features are enabled.
func filterManagedFiles(files []managedFile, cfg *config.ProjectConfig) []managedFile {
	filtered := make([]managedFile, 0, len(files))
	for _, f := range files {
		if fileEnabledByFeatures(f, cfg) {
			filtered = append(filtered, f)
		}
	}
	return filtered
}

// managedFiles returns the list of frozen files that upgrade manages.
//
// cmd/<bin>/main.go (the composition root) and the per-component cobra
// subcommand files (cmd/<bin>/cmd/{services,workers,operators}/<name>.go) are
// intentionally NOT in this list: they are projections of the discovered
// component rows, owned by the generate pipeline (GenerateCmdGroups), which has
// the inventory that upgrade lacks.
func managedFiles() []managedFile {
	return managedFilesFor(config.ProjectBinaryPerService)
}

// cmdTreePath joins the binary-scoped command-tree segments under
// cmd/<bin>/. The command tree moved out of the flat internal/cli package
// into cmd/<bin>/cmd (devspace idiom), so these destPaths are binary-name
// dependent. binName is the forge.yaml project name; when empty (legacy
// name-less callers) the path falls back to a bare cmd/<seg> form which is
// only used by the path-union backstop, never to write files.
func cmdTreePath(binName string, segs ...string) string {
	parts := []string{"cmd"}
	if binName != "" {
		parts = append(parts, binName)
	}
	parts = append(parts, "cmd")
	parts = append(parts, segs...)
	return filepath.Join(parts...)
}

// managedFilesForCfg is like managedFiles but consults the project
// config to choose the right per-kind / per-binary templates. Callers
// that already have the project config should prefer this so the right
// template is used during forge project upgrade and forge generate's Tier-1
// regeneration sweep.
//
// Kind sensitivity: the Taskfile template differs by kind (service has
// the full task verb set; CLI has cobra-shaped tasks; library is leaner).
// Without this, `forge project upgrade` on a CLI/library project produced a
// 100+ line diff that would have replaced the kind-correct Taskfile
// with the service one — diff was correctly skipped (file was
// "user-modified" from upgrade's perspective) but the dry-run output
// was unparseable.
//
// Binary-name sensitivity: the command tree lives under cmd/<bin>/, so the
// project name selects the cmd-tree destPaths (cmdTreePath).
// Frontend sensitivity: the per-frontend lint config is one managed entry
// per declared frontend (frontendManagedFiles), so it can only be built
// from a config that names them.
func managedFilesForCfg(cfg *config.ProjectConfig) []managedFile {
	binary := config.ProjectBinaryPerService
	kind := config.ProjectKindService
	binName := ""
	if cfg != nil {
		binary = cfg.EffectiveBinary()
		kind = cfg.EffectiveKind()
		binName = cfg.Name
	}
	return append(managedFilesForKindBinary(kind, binary, binName), frontendManagedFiles(cfg)...)
}

// managedFilesFor returns the file plan for an explicit binary mode at
// the canonical service kind. Extracted so callers without a
// *ProjectConfig (e.g. legacy tests) can still get a canonical file
// list. New callers should prefer managedFilesForKindBinary so kind
// branches (Taskfile.{cli,library}.yml.tmpl, etc.) are honored.
func managedFilesFor(binary string) []managedFile {
	return managedFilesForKindBinary(config.ProjectKindService, binary, "")
}

// managedFilesForKindBinary returns the file plan for an explicit kind
// + binary mode. The kind selects the correct Taskfile template
// (service / CLI / library).
//
// cmd/<bin>/main.go is intentionally NOT here: it is the composition root that
// names every group constructor explicitly, so it is a projection of the
// discovered component rows — owned by the generate pipeline (GenerateCmdGroups,
// which has the inventory this upgrade path lacks), exactly like the per-
// component cmd/<bin>/cmd/{services,workers,operators}/<name>.go files.
func managedFilesForKindBinary(kind, binary, binName string) []managedFile {
	taskfileTmpl := "Taskfile.yml.tmpl"
	switch kind {
	case config.ProjectKindCLI:
		taskfileTmpl = "Taskfile.cli.yml.tmpl"
	case config.ProjectKindLibrary:
		taskfileTmpl = "Taskfile.library.yml.tmpl"
	}
	return []managedFile{
		// ── Tier 1: Always overwritten by forge generate, gitignored ──

		// The cmd/<bin>/cmd command tree (devspace idiom: dir-nested by
		// category, one file per command). Service-shape (CLI/library don't
		// ship the Connect-server stack), gated via enabledForService. OTel is
		// owned by serverkit now — there is no generated cmd/otel.go shim.
		// cmd/<bin>/main.go is deliberately absent — see managedFilesForKindBinary.
		//
		// FOUR of the command-tree files are deliberately absent, for the
		// same reason as main.go: they are scaffold-once, USER-OWNED, and
		// written by the codegen pipeline's writeForgeScaffoldOnce.
		//
		//   - serve.go   the serve pipeline. Every statement is a decision:
		//                the fail-closed auth posture, the interceptor order,
		//                the payload caps, the CORS policy, the readiness
		//                set, the teardown.
		//   - server.go  the all-services ServeSpec — what this process
		//                mounts and supervises, and whether an unmounted
		//                declared service fails boot.
		//   - version.go the three ldflags-stamped variables. `-X` targets a
		//                variable by package path, so these MUST live in the
		//                project's tree and renaming one is a build change.
		//   - db.go      migration policy: fail hard on a dirty schema vs
		//                auto-force in staging, whether "nothing pending" is
		//                success, whether `down` is guarded.
		//
		// The invariant steps they used to carry moved into pkg/serverkit
		// (Boot / AutoMigrate / RecordMounted / AddWorkers / RequireComplete /
		// RESTHandler), pkg/cmdkit (PrintVersion) and pkg/migratekit (the
		// migrator, the DSN rewrite, the sentinel folding). That is what makes
		// scaffold-once safe: improvements to them now arrive through a
		// forge/pkg bump instead of a re-render, so owning these files no
		// longer costs the user the upgrade path.
		//
		//   - root.go    the command TREE: its shape, its flags, the Deps
		//                struct threaded through it. Users customize the
		//                binary's cobra root, and hash-guarding it made
		//                every such edit an act of permanent drift. It now
		//                also carries ServiceName and wires `db migrate`
		//                directly — see the root_gen.go retirement note.
		//
		// EVERY ONE of them also needs an UpgradeManagedPaths entry — they
		// were Tier-1 in older projects, so a copy is on disk carrying
		// forge's certification marker that no run writes anymore. See the
		// exclusion list there; without it the stale sweep deletes them.
		//
		// cmd/<bin>/cmd/root_gen.go is RETIRED — there is no generated file in
		// the command tree anymore. It held three things, and none of them
		// earned a regenerated file:
		//
		//   - ServiceName, the forge.yaml project name. Fixed at scaffold;
		//     it moves only in a rename the user is performing by hand
		//     anyway. Now a const in root.go.
		//   - migrationSource/Dir. The derived half — whether MigrationsFS is
		//     referenceable at all — is real, but it is a fact about the db
		//     package, so it lives there now as db.Source() (db/source_gen.go)
		//     and the command tree just calls it.
		//   - generatedCommands, which encoded one bit: does this project have
		//     migrations. root.go is itself rendered with that bit at scaffold
		//     time (as serve.go and db.go already were), so it wires
		//     newDBCmd directly.
		//
		// The on-disk copy is swept by removeRetiredRootGen; the
		// UpgradeManagedPaths entry below keeps the stale sweep from racing it.

		// ── Tier 2: Checksum-protected, committed to git ──

		// buf.yaml is templated against `api.rest` so a PRISTINE copy keeps
		// the googleapis BSR dep in lockstep with the runtime vanguard wrap:
		// `forge project upgrade` auto-updates it as long as the user hasn't touched
		// it. But buf.yaml is ALSO the only home for a project's buf `lint`
		// config — the template body itself documents uncommenting STANDARD
		// exceptions for migrated protos, and `forge lint --suggest-buf-excepts`
		// prints an `except:` snippet for the user to paste in. That hand-edit
		// is SANCTIONED, so buf.yaml is Tier-2 (respect user edits, never
		// stomp), not Tier-1. A pristine buf.yaml still tracks the derived dep;
		// a customized one is left alone (the Tier-2 stomp-guard exemption in
		// generate_tier_migrate.go keeps `forge generate` from aborting on it).
		{templateName: "buf.yaml.tmpl", destPath: "buf.yaml", templated: true, tier: Tier2},

		// Templated config files
		{templateName: taskfileTmpl, destPath: "Taskfile.yml", templated: true, tier: Tier2},
		{templateName: "Dockerfile.tmpl", destPath: "Dockerfile", templated: true, tier: Tier2, enabledFor: enabledForService},
		{templateName: "docker-compose.yml.tmpl", destPath: "docker-compose.yml", templated: true, tier: Tier2, enabledFor: enabledForService},
		{templateName: "idp-steps.yaml.tmpl", destPath: "idp-steps.yaml", templated: true, tier: Tier2, enabledFor: enabledForService},

		// Static config files
		{templateName: "golangci.yml.tmpl", destPath: ".golangci.yml", templated: true, tier: Tier2},
		{templateName: ".gitignore", destPath: ".gitignore", templated: false, tier: Tier2},
		{templateName: ".gitattributes", destPath: ".gitattributes", templated: false, tier: Tier2},

		// Middleware — the thin auth-policy file + its policy-wiring
		// test. Scaffolded once, then owned by the user; committed to
		// git and protected by checksum so `forge project upgrade` leaves user
		// edits alone. The middleware MECHANISMS (auth modes, CORS,
		// security headers, rate limiting, etc.) live in the forge
		// libraries (pkg/authn, pkg/middleware, pkg/observe)
		// — projects scaffolded before the library split keep their old
		// pkg/middleware/*.go copies; those files are user-owned and
		// simply stop being managed here. Adopting the library
		// (pkg/authn, pkg/middleware, pkg/observe) is a hand-migration.
		{templateName: "middleware.go.tmpl", destPath: "pkg/middleware/middleware.go", templated: true, tier: Tier2, enabledFor: enabledForService},
		{templateName: "middleware_test.go", destPath: "pkg/middleware/middleware_test.go", templated: false, tier: Tier2, enabledFor: enabledForService},

		// cmd/<bin>/cmd/commands.go — the user-owned cobra extension point the
		// Tier-1 cmd/<bin>/cmd/root.go consumes (userCommands(deps)).
		// Scaffolded once, then owned by the user; listed here so `forge
		// upgrade` CREATES it and never stomps an edited copy.
		{templateName: "cmd-tree-commands.go.tmpl", destPath: cmdTreePath(binName, "commands.go"), templated: true, tier: Tier2, enabledFor: enabledForService},

		// `<bin> metrics schema` and the package whose declarations it prints.
		// Scaffolded once and owned; listed so `forge upgrade` CREATES them for
		// an older project and never stomps an edited copy. They are a pair:
		// metrics.go imports internal/metrics.
		{templateName: "cmd-tree-metrics.go.tmpl", destPath: cmdTreePath(binName, "metrics.go"), templated: true, tier: Tier2, enabledFor: enabledForService},
		{templateName: "internal-metrics.go.tmpl", destPath: "internal/metrics/metrics.go", templated: true, tier: Tier2, enabledFor: enabledForService},

		// Local observability. Both are Tier 2: constant text the user is
		// expected to edit (add a log pipeline, tune the collector), not a
		// function of anything they declare. Gated on service-kind AND
		// observability being enabled.
		{templateName: "otel-collector.yaml.tmpl", destPath: "deploy/observability/otel-collector.yaml", templated: true, tier: Tier2, enabledFor: enabledForObservability},
		{templateName: "dashboards-README.md.tmpl", destPath: "deploy/observability/dashboards/README.md", templated: true, tier: Tier2, enabledFor: enabledForObservability},
		{templateName: "otel-profiles.yaml.tmpl", destPath: "deploy/observability/otel-profiles.yaml", templated: true, tier: Tier2, enabledFor: enabledForObservability},
		{templateName: "profiles-schema.sql.tmpl", destPath: "deploy/observability/profiles-schema.sql", templated: true, tier: Tier2, enabledFor: enabledForObservability},
	}
}

// UpgradeManagedPaths returns the set of project-relative paths that
// `forge project upgrade` (not `forge generate`) is responsible for emitting.
// Used by `forge generate`'s stale-artifact sweep to exclude these
// paths from the "stale codegen" candidate list: they're tracked in
// `.forge/checksums.json` but only re-rendered by upgrade, so seeing
// them missing from this run's WrittenThisRun set is the expected
// state, not a stale signal.
//
// The set is the union over every (kind, binary) combination. Forge
// only ships a small number of these combinations so the union is
// cheap; computing the union (rather than asking the caller for the
// project's specific kind/binary) keeps the helper signature simple
// and means a kind/binary mismatch in detection doesn't accidentally
// flag a managed file as stale.
//
// FRICTION 2026-06-05 (cp-forge project audit-cleanup agent): `forge generate`
// warned 7 "stale" files — .github/CODEOWNERS, .golangci.yml,
// cmd/main.go, cmd/db.go, cmd/version.go, .github/workflows/e2e.yml,
// .github/pull_request_template.md — all of which are managed by
// `forge project upgrade`. The user worked around it by hand-flipping
// `forked: true` in checksums.json, which silenced the warnings but
// also disconnected the files from the upgrade pipeline. The right
// fix is for the stale-sweep to know about the upgrade-managed set.
//
// Keys are slash-separated, the form of every ownership key the sweep
// compares them to (checksums.ScanMarkers). destPath is a native path —
// cmdTreePath joins with filepath — so on Windows the unnormalized set
// never matched and the sweep flagged these files as stale again. Every
// key goes through add, so the extra list below cannot skip the
// normalization: it once did, and on Windows the sweep marked the
// scaffold-once cmd/cmd/*.go files stale and deleted them.
func UpgradeManagedPaths() map[string]bool {
	out := map[string]bool{}
	add := func(p string) { out[filepath.ToSlash(p)] = true }
	for _, kind := range []string{
		config.ProjectKindService,
		config.ProjectKindCLI,
		config.ProjectKindLibrary,
	} {
		for _, binary := range []string{
			config.ProjectBinaryPerService,
			config.ProjectBinaryShared,
		} {
			for _, f := range managedFilesForKindBinary(kind, binary, "") {
				add(f.destPath)
			}
		}
	}
	// Files emitted by ProjectGenerator outside the managedFiles list —
	// these still belong to the upgrade lane (templates that scaffold
	// once and stay user-owned, or one-shot Tier-2 metadata that
	// `forge generate` never touches). Without the additions below the
	// stale-sweep would re-flag them with the same false positive the
	// FRICTION note above describes. Add new upgrade-owned scaffolds
	// here when surfaces emerge.
	for _, p := range []string{
		// .github/* templates emitted by project_metadata.go's GitHub
		// scaffold pass — Tier-1 in checksums but `forge generate` never
		// re-emits them; `forge project upgrade` does on version bumps.
		".github/CODEOWNERS",
		".github/pull_request_template.md",
		".github/dependabot.yml",
		".github/workflows/e2e.yml",
		// The scaffold-once command-tree files — here for a MIGRATION
		// reason, and they are the dangerous case this whole exclusion list
		// exists for.
		//
		// Each of these used to be Tier-1, so every project scaffolded
		// before its split has a copy on disk carrying forge's "Code
		// generated" + forge:hash markers. Each is now scaffold-once and
		// user-owned, so no `forge generate` run writes it and it never
		// enters WrittenThisRun. Without these entries the stale sweep sees
		// a certified file nobody emitted, calls it stale, and DELETES
		// it — files that, post-split, hold
		// decisions the user may have edited: the serve pipeline, the
		// all-services ServeSpec, the ldflags version stamp, and the
		// migration policy.
		//
		// This is data loss, not a warning: the file is pristine (the user
		// never touched it) in exactly the common case, and pristine is the
		// condition under which the sweep deletes rather than reports.
		//
		// The bare cmd/cmd/<file>.go form is what the name-less union
		// yields; the sweep compares against that same spelling.
		cmdTreePath("", "serve.go"),
		cmdTreePath("", "server.go"),
		cmdTreePath("", "version.go"),
		cmdTreePath("", "db.go"),
		// root.go joined them when the command tree became the user's to
		// shape. It is the most dangerous entry in this list: EVERY existing
		// project has a Tier-1 root.go on disk, all of them pristine (there
		// was no reason to edit a file forge overwrote), and pristine is
		// exactly the condition under which the sweep deletes rather than
		// reports. Without this line the sweep removes the command root
		// from every upgraded project.
		cmdTreePath("", "root.go"),
	} {
		add(p)
	}
	return out
}

// ManagedPathsFor returns the project-relative paths `forge project upgrade`
// manages for THIS project, in manifest order.
//
// UpgradeManagedPaths is the union over every (kind, binary) combination — the
// right question for the stale-artifact sweep, which must not flag a file just
// because detection guessed a different shape. This is the narrower question a
// user-facing path argument needs: "is this a file upgrade can write HERE",
// answered against the project's own kind, binary mode, and binary name.
func ManagedPathsFor(cfg *config.ProjectConfig) []string {
	files := filterManagedFiles(managedFilesForCfg(cfg), cfg)
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.destPath)
	}
	return out
}

// Tier2ManagedPaths returns the set of project-relative paths whose
// canonical template tier is Tier-2 (scaffold-once, user-owned after the
// first write). It is the source of truth for `forge generate`'s
// tier-migration step (generate_tier_migrate.go in internal/cli): a
// `.forge/checksums.json` entry for one of these paths that still
// carries tier=1 (or the legacy unset tier=0) predates the template's
// reclassification and must be flipped to tier=2 so the file stops
// being drift-guarded and stops surfacing as a "fork".
//
// Two sources:
//
//   - The managed-file registry entries tagged Tier2. A destPath's tier
//     is invariant across the (kind, binary) matrix — only the source
//     template varies — so the union over combinations is safe (same
//     posture as UpgradeManagedPaths).
//   - The one-shot .github scaffolds written once at `forge project new` time
//     (project_ci.go) and never re-emitted by `forge generate`.
//     CODEOWNERS even carries the `yours: scaffolded once ... (starter)`
//     banner; recording them as Tier-1 was a historical accident that
//     made hand-editing your own CODEOWNERS trip the Tier-1 stomp
//     guard. (FRICTION 2026-06-05, cp-forge: users worked around the
//     misclassification by hand-flipping `forked: true`.)
//
// .github/workflows/e2e.yml and .github/dependabot.yml belong to that
// second group. They were excluded from this set on the grounds that
// `forge generate`'s CI step re-renders them — which stopped being true
// when the CI workflows became write-once scaffolds (writeCIScaffold in
// internal/cli/generate_ci.go): every one of them is now written at most
// once and is the user's from birth, carrying no marker at all.
//
// The exclusion outliving that change is what produced the worst shape a
// guard can take. A project scaffolded by an older forge still has the
// Tier-1 forge:hash marker its e2e.yml was born with; editing that file
// is now sanctioned, but the drift scan still read it as a hand-edited
// generated file and aborted `forge generate` WHOLESALE — including runs
// that touched nothing near CI. And because no emitter rewrites the file,
// the drift report had no extension point to name and said so
// ("NO EXTENSION POINT EXISTS"), which is forge telling the user their
// only remaining options are to discard their fix or disown the file
// forever. Both were wrong: the edit was legitimate and the tier was not.
func Tier2ManagedPaths() map[string]bool {
	out := map[string]bool{}
	for _, kind := range []string{
		config.ProjectKindService,
		config.ProjectKindCLI,
		config.ProjectKindLibrary,
	} {
		for _, binary := range []string{
			config.ProjectBinaryPerService,
			config.ProjectBinaryShared,
		} {
			for _, f := range managedFilesForKindBinary(kind, binary, "") {
				if f.tier == Tier2 {
					out[filepath.ToSlash(f.destPath)] = true
				}
			}
		}
	}
	for _, p := range []string{
		".github/CODEOWNERS",
		".github/pull_request_template.md",
		// Every file writeCIScaffold writes. Kept in step with that
		// function — if a workflow ever becomes a real Tier-1 emit target
		// again, it must leave this list in the same change. e2e.yml and
		// dependabot.yml are the two that had already shipped Tier-1
		// markers to real projects; the other four are listed for the same
		// reason and to keep the registry a straight mirror of the writer
		// rather than a list of the failures observed so far.
		".github/workflows/ci.yml",
		".github/workflows/e2e.yml",
		".github/workflows/deploy.yml",
		".github/workflows/build-images.yml",
		".github/workflows/proto-breaking.yml",
		".github/dependabot.yml",
	} {
		out[p] = true
	}
	return out
}

// buildTemplateData constructs the upgrade-lane render payload from a
// project config. It is a thin alias for forUpgrade (project_template_data.go),
// kept so existing call sites and tests read naturally in the upgrade lane.
//
// projectDir (when non-empty) is used to read the project's go.mod `go`
// directive so upgrade doesn't silently retarget the project to the host's
// Go version. When projectDir is empty or go.mod can't be parsed, we fall
// back to the host's detected version.
func buildTemplateData(cfg *config.ProjectConfig, projectDir string) projectTemplateData {
	return forUpgrade(cfg, projectDir)
}

// renderManagedFile renders a managed file's template content.
func renderManagedFile(f managedFile, data projectTemplateData) ([]byte, error) {
	var content []byte
	var err error
	switch {
	case f.render != nil:
		content, err = f.render()
	case f.templated:
		content, err = templates.ProjectTemplates().Render(f.templateName, data)
	default:
		content, err = templates.ProjectTemplates().Get(f.templateName)
	}
	if err != nil {
		return nil, err
	}
	// gofmt Go renders. The generate pipeline runs goimports over
	// everything it writes, but the upgrade lane historically wrote raw
	// template output — so conditional templates (cmd-server.go.tmpl's
	// ConfigFields-gated struct literal) produced misaligned code that
	// diffed against the on-disk gofmt'd file and surfaced as phantom
	// "would update"/fork noise. format.Source can't reproduce
	// goimports' import-group reordering, but it eliminates the
	// alignment class entirely. Unformattable output (template bug)
	// falls through unformatted rather than failing the render.
	if strings.HasSuffix(f.destPath, ".go") {
		if formatted, ferr := format.Source(content); ferr == nil {
			content = formatted
		}
	}
	// Canonicalize trailing newline. gofmt-formatted Go files (and most
	// editor-on-save defaults across yaml/json/md) end with exactly one
	// `\n`. Templates checked into the repo sometimes don't, which made
	// drift detection report user-modified for files the user never
	// touched — they just got a `\n` appended on their first editor save.
	// Normalize at render time so byte-equal comparison and the on-disk
	// write both end with a single newline.
	return ensureTrailingNewline(content), nil
}

// ensureTrailingNewline appends exactly one trailing `\n` to text content,
// trimming any extras. Empty inputs are left empty.
func ensureTrailingNewline(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	end := len(b)
	for end > 0 && b[end-1] == '\n' {
		end--
	}
	out := make([]byte, end+1)
	copy(out, b[:end])
	out[end] = '\n'
	return out
}

// managedLineDelta sizes a managed file's difference from its template the
// same way the advisory lane does, with the certification marker removed
// first.
//
// The marker is the one line an on-disk managed file has that no render
// ever produces. Counting it would give every single managed file a
// permanent `1 line yours alone`, which is precisely the signal the report
// ranks on — so a file with no edits at all would sort as customized.
func managedLineDelta(onDisk, template []byte) (missing, local int) {
	return lineDelta(checksums.StripMarker(onDisk), checksums.StripMarker(template))
}

// simpleDiff produces a minimal unified-style diff showing changed lines.
func simpleDiff(path string, old, new []byte) string {
	oldLines := strings.Split(string(old), "\n")
	newLines := strings.Split(string(new), "\n")

	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("--- a/%s\n", path))
	buf.WriteString(fmt.Sprintf("+++ b/%s\n", path))

	const contextLines = 3

	// Find changed regions
	type change struct {
		lineOld int
		lineNew int
	}
	var changes []change

	i, j := 0, 0
	for i < len(oldLines) && j < len(newLines) {
		if oldLines[i] != newLines[j] {
			changes = append(changes, change{i, j})
		}
		i++
		j++
	}
	for ; i < len(oldLines); i++ {
		changes = append(changes, change{i, -1})
	}
	for ; j < len(newLines); j++ {
		changes = append(changes, change{-1, j})
	}

	if len(changes) == 0 {
		return ""
	}

	// Group changes into hunks with context
	type hunkRange struct {
		startOld, endOld int
		startNew, endNew int
	}
	var hunks []hunkRange

	for _, c := range changes {
		oLine := c.lineOld
		if oLine < 0 {
			oLine = len(oldLines)
		}
		nLine := c.lineNew
		if nLine < 0 {
			nLine = len(newLines)
		}

		startO := oLine - contextLines
		if startO < 0 {
			startO = 0
		}
		endO := oLine + contextLines + 1
		if endO > len(oldLines) {
			endO = len(oldLines)
		}
		startN := nLine - contextLines
		if startN < 0 {
			startN = 0
		}
		endN := nLine + contextLines + 1
		if endN > len(newLines) {
			endN = len(newLines)
		}

		if len(hunks) > 0 {
			last := &hunks[len(hunks)-1]
			if startO <= last.endOld || startN <= last.endNew {
				if endO > last.endOld {
					last.endOld = endO
				}
				if endN > last.endNew {
					last.endNew = endN
				}
				continue
			}
		}
		hunks = append(hunks, hunkRange{startO, endO, startN, endN})
	}

	for _, h := range hunks {
		oldCount := h.endOld - h.startOld
		newCount := h.endNew - h.startNew
		buf.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", h.startOld+1, oldCount, h.startNew+1, newCount))

		// Use a simple approach: show removed lines then added lines with context
		oi, ni := h.startOld, h.startNew
		for oi < h.endOld || ni < h.endNew {
			if oi < h.endOld && ni < h.endNew && oi < len(oldLines) && ni < len(newLines) && oldLines[oi] == newLines[ni] {
				buf.WriteString(" " + oldLines[oi] + "\n")
				oi++
				ni++
			} else if oi < h.endOld && oi < len(oldLines) {
				buf.WriteString("-" + oldLines[oi] + "\n")
				oi++
			} else if ni < h.endNew && ni < len(newLines) {
				buf.WriteString("+" + newLines[ni] + "\n")
				ni++
			} else {
				break
			}
		}
	}

	return buf.String()
}

// RegenerateInfraFiles regenerates all Tier 1 (always-overwrite) infrastructure
// files. Called by forge generate to keep infrastructure in sync with templates.
//
// The cmd/<bin>/cmd command tree (root.go, serve.go, server.go, version.go,
// db.go) is part of this Tier-1 set, keyed off the project name so the
// per-binary destPaths resolve. cmd/<bin>/main.go is NOT — it is the
// composition root, owned by the generate pipeline (GenerateCmdGroups), which
// has the component inventory this path lacks.
func RegenerateInfraFiles(projectDir string, cfg *config.ProjectConfig) error {
	return RegenerateInfraFilesTracked(projectDir, cfg, nil)
}

// RegenerateInfraFilesTracked is RegenerateInfraFiles routed through the
// checksums chokepoint. With a non-nil cs every Tier-1 infra write:
//
//   - honors disowned entries (the user ran `forge project disown`: the write
//     is skipped while the file exists — the raw os.WriteFile path this
//     replaces violated the "forge never regenerates user-owned files"
//     contract for cmd/*.go and friends);
//   - records the render hash + WrittenThisRun so the stale sweep and
//     the next run's drift guard see an accurate manifest;
//   - tags the entry Tier-1 (these files ARE regenerated every run).
//
// force=true preserves the historical always-overwrite semantics for
// forge-owned files: the Tier-1 stomp guard ran earlier in the pipeline,
// so any surviving drift was already adjudicated (--force / disown).
//
// A nil cs falls back to untracked writes (legacy callers).
func RegenerateInfraFilesTracked(projectDir string, cfg *config.ProjectConfig, cs *FileChecksums) error {
	data := buildTemplateData(cfg, projectDir)
	filtered := filterManagedFiles(managedFilesForCfg(cfg), cfg)
	for _, f := range filtered {
		if f.tier != Tier1 {
			continue
		}
		content, err := renderManagedFile(f, data)
		if err != nil {
			return fmt.Errorf("render %s: %w", f.destPath, err)
		}
		if _, err := checksums.WriteGeneratedFileTier1(projectDir, f.destPath, content, cs, true); err != nil {
			return fmt.Errorf("write %s: %w", f.destPath, err)
		}
	}
	// The Tier-1 cmd/<bin>/cmd/root.go just (re)rendered above references the
	// user-owned userCommands() extension point (newRootCmd consumes it).
	// Ensure cmd/<bin>/cmd/commands.go exists (write-once; never overwrites) so
	// a tree whose root.go gained the reference this run still compiles — the
	// codegen pipeline's stepCmdSubcommands does the same, but this path also
	// runs for service projects with features.codegen=false.
	binName := ""
	if cfg != nil {
		binName = cfg.Name
	}
	wantRoot := cmdTreePath(binName, "root.go")
	for _, f := range filtered {
		if f.destPath == wantRoot {
			if err := codegen.GenerateCmdCommands(projectDir, binName); err != nil {
				return fmt.Errorf("scaffold cmd/%s/cmd/commands.go: %w", binName, err)
			}
			break
		}
	}
	return nil
}

// hasLegacyMiddlewareLayout reports whether the project's
// pkg/middleware still has the pre-library-split shape: legacy
// mechanism files present (auth.go / claims.go are the sentinels —
// every old scaffold had both) and no thin middleware.go yet. Upgrade
// must not emit the thin policy pair into such a package — the symbol
// sets collide.
func hasLegacyMiddlewareLayout(projectDir string) bool {
	if _, err := os.Stat(filepath.Join(projectDir, "pkg", "middleware", "middleware.go")); err == nil {
		return false // already on the thin layout
	}
	for _, sentinel := range []string{"auth.go", "claims.go"} {
		if _, err := os.Stat(filepath.Join(projectDir, "pkg", "middleware", sentinel)); err == nil {
			return true
		}
	}
	return false
}

// Upgrade checks all managed (frozen) files against the current templates
// and optionally applies updates.
//
// When checkOnly is true, no files are written — it only reports what would change.
// When force is true, EVERY user-modified file is overwritten. Callers that
// want to adopt specific files should use UpgradeSelection.
func Upgrade(projectDir string, cfg *config.ProjectConfig, force bool, checkOnly bool) ([]UpgradeResult, error) {
	selection := ForceNone()
	if force {
		selection = ForceAll()
	}
	return UpgradeSelection(projectDir, cfg, selection, checkOnly)
}

// UpgradeSelection is Upgrade with a per-path force selection: only the files
// the selection covers have user edits overwritten, everything else follows
// the ordinary rules (pristine renders auto-update, user-modified files are
// reported and skipped).
//
// A disowned file is never written regardless of the selection — ownership
// transfer outranks force, which is the whole reason `forge project disown` is
// the durable answer to "this file is mine now".
func UpgradeSelection(projectDir string, cfg *config.ProjectConfig, force ForceSelection, checkOnly bool) ([]UpgradeResult, error) {
	data := buildTemplateData(cfg, projectDir)

	cs, err := LoadChecksums(projectDir)
	if err != nil {
		return nil, fmt.Errorf("load checksums: %w", err)
	}

	var results []UpgradeResult

	// Pre-library-split projects still carry the old pkg/middleware
	// mechanism files (auth.go, claims.go, …). Those declare the same
	// symbols as the thin policy pair (Claims, NewAuthInterceptor, …),
	// so dropping middleware.go next to them would stop
	// the package compiling. Their copies are user-owned and keep
	// working; converging on the library is a user-driven hand-migration,
	// never an upgrade side effect.
	legacyMiddleware := hasLegacyMiddlewareLayout(projectDir)

	for _, f := range filterManagedFiles(managedFilesForCfg(cfg), cfg) {
		if legacyMiddleware && strings.HasPrefix(f.destPath, "pkg/middleware/") {
			results = append(results, UpgradeResult{
				Path:   f.destPath,
				Status: UpgradeSkipped,
			})
			continue
		}
		// Disowned entries are user-owned: upgrade never touches them
		// while the file exists. A missing file falls through — deletion
		// is the documented re-adoption path, and upgrade re-emitting it
		// is the same contract as `forge generate`.
		if cs.IsDisowned(f.destPath) {
			if _, statErr := os.Stat(filepath.Join(projectDir, f.destPath)); statErr == nil {
				results = append(results, UpgradeResult{
					Path:   f.destPath,
					Status: UpgradeSkipped,
				})
				continue
			}
		}

		// Render the expected content from the current template
		expected, err := renderManagedFile(f, data)
		if err != nil {
			return nil, fmt.Errorf("render template %s: %w", f.templateName, err)
		}

		// Read the existing file on disk
		diskPath := filepath.Join(projectDir, f.destPath)
		existing, err := os.ReadFile(diskPath)
		if err != nil {
			if os.IsNotExist(err) {
				// File doesn't exist — treat as needing update.
				//
				// Sized as the pure add it is: the whole template is
				// "missing" here. Leaving these at zero made a new
				// managed file report a delta of no lines, which reads
				// as "nothing to see" for the one case where adoption
				// is unambiguously safe.
				result := UpgradeResult{
					Path:    f.destPath,
					Status:  UpgradeSkipped,
					Diff:    simpleDiff(f.destPath, nil, expected),
					Absent:  true,
					Missing: len(lineBag(expected)),
				}
				if !checkOnly {
					if writeErr := writeManagedFile(projectDir, f.destPath, expected, cs); writeErr != nil {
						return nil, fmt.Errorf("write %s: %w", f.destPath, writeErr)
					}
					result.Status = UpgradeUpdated
				} else {
					result.Status = UpgradeUpdated // would be updated
				}
				results = append(results, result)
				continue
			}
			return nil, fmt.Errorf("read %s: %w", f.destPath, err)
		}

		// Compare rendered template with what's on disk. The on-disk
		// copy carries an embedded forge:hash marker the raw render
		// doesn't — compare marker-excluded body hashes.
		if checksums.BodyHash(existing) == checksums.BodyHash(expected) {
			results = append(results, UpgradeResult{
				Path:   f.destPath,
				Status: UpgradeUpToDate,
			})
			continue
		}

		// Tier 1 files are always overwritten (they're gitignored)
		if f.tier == Tier1 {
			missing, local := managedLineDelta(existing, expected)
			result := UpgradeResult{
				Path:    f.destPath,
				Status:  UpgradeUpdated,
				Diff:    simpleDiff(f.destPath, existing, expected),
				Missing: missing,
				Local:   local,
			}
			if !checkOnly {
				if writeErr := writeManagedFile(projectDir, f.destPath, expected, cs); writeErr != nil {
					return nil, fmt.Errorf("write %s: %w", f.destPath, writeErr)
				}
			}
			results = append(results, result)
			continue
		}

		// Tier 2: File differs — check if user has modified it.
		//
		// The file is self-certifying: a VERIFYING embedded forge:hash
		// marker proves the on-disk bytes are an unedited forge render
		// of some vintage, so the template delta is stale codegen —
		// auto-updateable without --force. A marker that fails
		// verification (or no marker at all, for pre-marker projects)
		// means user-modified. Comment-incapable formats consult the
		// scoped .forge/hashes.json record instead.
		diff := simpleDiff(f.destPath, existing, expected)
		missing, local := managedLineDelta(existing, expected)
		matchesKnownRender := checksums.Verify(existing) == checksums.Pristine
		if !checksums.Stampable(f.destPath) && cs != nil {
			recorded, tracked := cs.Unstampable[filepath.ToSlash(f.destPath)]
			matchesKnownRender = tracked && checksums.BodyHash(existing) == recorded
		}

		if matchesKnownRender {
			// File matches stored checksum or a prior render → user
			// hasn't modified it → safe to auto-update.
			result := UpgradeResult{
				Path:    f.destPath,
				Status:  UpgradeUpdated,
				Diff:    diff,
				Missing: missing,
				Local:   local,
			}
			if !checkOnly {
				if writeErr := writeManagedFile(projectDir, f.destPath, expected, cs); writeErr != nil {
					return nil, fmt.Errorf("write %s: %w", f.destPath, writeErr)
				}
			}
			results = append(results, result)
			continue
		}

		// User modified the file (or no checksum exists)
		if force.Allows(f.destPath) {
			result := UpgradeResult{
				Path:    f.destPath,
				Status:  UpgradeUpdated,
				Diff:    diff,
				Forced:  true,
				Missing: missing,
				Local:   local,
			}
			if !checkOnly {
				if writeErr := writeManagedFile(projectDir, f.destPath, expected, cs); writeErr != nil {
					return nil, fmt.Errorf("write %s: %w", f.destPath, writeErr)
				}
			}
			results = append(results, result)
		} else {
			results = append(results, UpgradeResult{
				Path:    f.destPath,
				Status:  UpgradeUserModified,
				Diff:    diff,
				Missing: missing,
				Local:   local,
			})
		}
	}

	// Save updated checksums (unless dry-run)
	if !checkOnly {
		if err := SaveChecksums(projectDir, cs); err != nil {
			return nil, fmt.Errorf("save checksums: %w", err)
		}
	}

	return results, nil
}

// writeManagedFile writes a managed file through the certification
// chokepoint: stampable formats get the embedded forge:hash marker;
// comment-incapable ones get a scoped .forge/hashes.json record.
func writeManagedFile(root, relPath string, content []byte, cs *FileChecksums) error {
	if stamped, ok := checksums.Stamp(relPath, content); ok {
		content = stamped
	} else if cs != nil {
		if cs.Unstampable == nil {
			cs.Unstampable = map[string]string{}
		}
		cs.Unstampable[filepath.ToSlash(relPath)] = checksums.BodyHash(content)
	}
	fullPath := filepath.Join(root, relPath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return err
	}
	if err := atomicfile.WriteFile(fullPath, content, 0644); err != nil {
		return err
	}
	checksums.MarkWrittenThisRun(relPath)
	// A write through the upgrade chokepoint means forge owns the result
	// again — the only way a disowned entry reaches here is the deletion
	// re-adoption path (Upgrade skips disowned entries whose file
	// exists), so clear the ownership-transfer record.
	if cs != nil {
		delete(cs.Disowned, relPath)
	}
	return nil
}
