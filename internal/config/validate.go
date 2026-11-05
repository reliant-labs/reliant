package config

import (
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// configWarningSink is where non-fatal config warnings (deprecated
// top-level keys) are written. It defaults to os.Stderr so the notice
// reaches the user on every load path (forge generate / forge project upgrade /
// any caller of LoadProject) without those callers having to
// thread a warnings slice through. The config package is otherwise
// log-free; this is a single, swappable io.Writer rather than a logger so
// tests can capture it (SetConfigWarningSink).
var configWarningSink io.Writer = os.Stderr

// emittedConfigWarnings dedupes warnings within a single process. A
// single CLI command (e.g. `forge lint`) loads forge.yaml several times
// across its sub-steps; without dedup the same deprecated-key notice
// prints once per load. Keyed on label+line+message so a genuinely
// distinct warning (different file, different key) still surfaces.
var emittedConfigWarnings = map[string]bool{}

// SetConfigWarningSink overrides the destination for non-fatal config
// warnings and returns the previous sink so callers can restore it. Used
// by tests to capture warning output; production code leaves the default
// (os.Stderr). Swapping the sink also resets the per-process dedup set so
// each test starts from a clean slate.
func SetConfigWarningSink(w io.Writer) io.Writer {
	prev := configWarningSink
	if w == nil {
		w = io.Discard
	}
	configWarningSink = w
	emittedConfigWarnings = map[string]bool{}
	return prev
}

// partitionIssues splits a flat issue list into the fatal errors (which
// gate the load via ValidationError) and the non-fatal warnings (which
// are flushed to the warning sink but never gate). Order within each
// bucket is preserved.
func partitionIssues(issues []validationIssue) (errs, warns []validationIssue) {
	for _, iss := range issues {
		if iss.warning {
			warns = append(warns, iss)
		} else {
			errs = append(errs, iss)
		}
	}
	return errs, warns
}

// flushConfigWarnings writes each warning to the warning sink in the
// standard `label:line:col: message Fix: ...` shape (same format the
// fatal ValidationError uses) so a user sees warnings and errors in a
// consistent layout. No-op when there are no warnings.
func flushConfigWarnings(label string, warns []validationIssue) {
	for _, w := range warns {
		// Dedup on the file's BASE NAME, not the label as given: a single
		// command loads the same forge.yaml through several callers, some
		// passing an absolute path and some the bare "forge.yaml", and keying
		// on the raw label made those spellings look like different files —
		// so every retired key printed once per caller.
		dedupKey := fmt.Sprintf("%s:%d:%s", filepath.Base(label), w.line, w.msg)
		if emittedConfigWarnings[dedupKey] {
			continue
		}
		emittedConfigWarnings[dedupKey] = true
		var b strings.Builder
		b.WriteString("⚠️  forge.yaml: ")
		b.WriteString(formatIssueLocation(label, w))
		b.WriteString(": ")
		b.WriteString(w.msg)
		if w.fix != "" {
			fmt.Fprintf(&b, " Fix: %s", w.fix)
		}
		_, _ = fmt.Fprintln(configWarningSink, b.String())
	}
}

// LoadProject is THE forge.yaml loader: it parses a forge.yaml byte stream
// into a ProjectConfig with strict validation, derives the project kind from
// the project's real sources, and applies the shape-derived section defaults.
// Both the CLI loader and the generator's ReadProjectConfig route through it,
// so the load rules live in exactly one place.
//
// Unknown keys (typos, dropped fields) and missing required fields are
// reported in a single error rather than silently succeeding or failing on
// the first issue.
//
// path locates the project on disk: its directory is read to derive the
// project kind, and it prefixes error messages. It is never opened for the
// config bytes themselves — the caller supplies those. A path with no
// on-disk project behind it (byte-only loads) loads as a library.
//
// Behaviour:
//
//  1. The YAML is decoded into a yaml.Node tree, then walked against
//     the ProjectConfig struct shape. Unknown keys are collected with
//     their YAML line number and parent path; a Levenshtein-based
//     suggestion is attached when a known sibling key is within edit
//     distance 2 (or 3 for keys >= 8 chars).
//  2. The same bytes are then decoded into a ProjectConfig via the
//     standard yaml decoder so that scalar-type mismatches (e.g.
//     port: "8080") surface as their own error class.
//  3. Required-field validation runs on the populated struct.
//
// All issues across the three phases are batched into a single
// ValidationError; the caller sees the full list rather than just the
// first failure.
//
// What a project CONTAINS is not part of this: components are read from the
// code that declares them (codegen.DiscoverProjectComponents), never from
// forge.yaml and never from a field on the returned config.
func LoadProject(data []byte, path string) (*ProjectConfig, error) {
	label := path
	if label == "" {
		label = "forge.yaml"
	}

	// Phase 1: walk yaml.Node to find unknown keys with position info.
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: parse error: %w", label, err)
	}
	var root *yaml.Node
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		root = doc.Content[0]
	}
	var issues []validationIssue
	if root != nil && root.Kind == yaml.MappingNode {
		issues = append(issues, walkUnknownKeys(root, "", reflect.TypeFor[ProjectConfig]())...)
	} else if root != nil && root.Kind != 0 {
		issues = append(issues, validationIssue{
			line:   root.Line,
			column: root.Column,
			msg:    "expected a YAML mapping at the top level",
			fix:    "the file must be a YAML mapping (key: value pairs), not a list or scalar.",
		})
	}

	// Phase 2: decode into the typed struct. This catches scalar-type
	// mismatches and any other yaml decoding failures. We do NOT pass
	// KnownFields(true) here because phase 1 already covered that with
	// better suggestions.
	var cfg ProjectConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		// yaml type errors look like:
		//   "yaml: line 7: cannot unmarshal !!str `8080` into int"
		// We surface them verbatim alongside any unknown-key issues.
		for _, line := range splitYAMLErrorLines(err) {
			issues = append(issues, validationIssue{msg: line})
		}
	}

	// Derive the project kind BEFORE shape-derived defaults run (feature
	// derivation reads kind). Kind comes from the project's REAL sources —
	// the KCL deploy tree, the pkg/app composition root, internal/handlers/,
	// the service protos, and cmd/<name>/main.go — read relative to the
	// forge.yaml directory. When there is no on-disk project to read
	// (byte-only loads against a synthetic path) there is nothing to read a
	// shape off, so the config is a bare module: library.
	projectDir := sourceProjectDir(path)
	cfg.projectDir = projectDir
	if projectDir != "" {
		cfg.Kind = deriveProjectKindFromSources(projectDir)
	} else {
		cfg.Kind = ProjectKindLibrary
	}

	// Resolve the frontend inventory HERE, before validation and before
	// feature derivation, so every command that loads this file sees one
	// answer. Feature derivation reads len(cfg.Frontends) to decide
	// features.frontend, so an inventory resolved after it would leave the
	// frontend pipeline steps gated off by a flag computed against an
	// empty list — which is precisely the bug the generate-only mutation
	// had to re-run ApplyDerivedDefaults to paper over.
	ResolveInventoryAtLoad(&cfg, projectDir)

	// Phase 3: required-field validation. The yaml root is threaded
	// through so issues can carry the line:col of the *parent* mapping
	// (or the existing-field's own line, when it's present but invalid).
	// Without this, "module_path is required" reports no location and
	// the model has to grep — model-friendly file:line:col on every
	// issue is the goal of the loader surface.
	issues = append(issues, validateRequired(&cfg, root)...)

	// Phase 4: name-shape validation over the frontends block. This
	// catches Go-package collisions and reserved-word/identifier shapes
	// that would otherwise blow up the generator with a confusing
	// downstream error.

	// Partition non-fatal warnings (deprecated top-level keys) out of the
	// gating error set. Warnings are flushed to the user unconditionally —
	// whether or not the load also has hard errors — so a deprecated key
	// is never lost to a silent rewrite even when other issues abort the
	// load.
	errIssues, warnIssues := partitionIssues(issues)
	flushConfigWarnings(label, warnIssues)
	if len(errIssues) > 0 {
		return nil, &ValidationError{Path: label, Issues: errIssues}
	}

	// Resolve shape-derived defaults: fill absent section blocks with the
	// canonical scaffold defaults for the project kind, and attach the
	// feature-derivation context so absent feature flags resolve from
	// shape (see derive.go). Explicit values are never overridden.
	//
	// The yaml root is threaded through so defaulting is FIELD-level: a
	// section the user partially wrote keeps the defaults of the keys they
	// left out. Without it, writing the `database.migration_safety`
	// allowlist that forge's own migration-safety error recommends left
	// Driver empty, which derived FeatureMigrations off and made `forge
	// lint --migration-safety` exit 0 having checked nothing.
	ApplyDerivedDefaultsFromNode(&cfg, root)

	// Phase 5: feature dependency graph. Now that the feature set is
	// fully resolved (derived defaults + explicit overrides folded in),
	// reject any enabled feature whose dependency is off — a config that
	// would otherwise load clean and then silently no-op or blow up
	// mid-generate. Batched into the same ValidationError so the caller
	// sees every contradiction at once (see feature_graph.go).
	if graphIssues := validateFeatureGraph(&cfg); len(graphIssues) > 0 {
		return nil, &ValidationError{Path: label, Issues: graphIssues}
	}
	return &cfg, nil
}

// ValidationError aggregates all forge.yaml validation issues into a
// single error so callers see the full picture instead of fail-fast on
// the first problem. Implements error.
type ValidationError struct {
	Path   string
	Issues []validationIssue
}

func (e *ValidationError) Error() string {
	if len(e.Issues) == 1 {
		var b strings.Builder
		b.WriteString(formatIssueLocation(e.Path, e.Issues[0]))
		b.WriteString(": ")
		b.WriteString(e.Issues[0].msg)
		if e.Issues[0].fix != "" {
			fmt.Fprintf(&b, " Fix: %s", e.Issues[0].fix)
		}
		return b.String()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s has %d validation issue", e.Path, len(e.Issues))
	if len(e.Issues) != 1 {
		b.WriteString("s")
	}
	b.WriteString(":\n")
	for _, iss := range e.Issues {
		b.WriteString("  ")
		b.WriteString(formatIssueLocation(e.Path, iss))
		b.WriteString(": ")
		b.WriteString(iss.msg)
		if iss.fix != "" {
			fmt.Fprintf(&b, " Fix: %s", iss.fix)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatIssueLocation renders the per-issue position in standard
// compiler/editor format: `path:line:col` when both line and column are
// known, `path:line` for line-only, `path` when neither. Matches what
// every editor, LSP client, and `cc`/`go vet`-style tool already
// understands — a model reading the error can immediately open the
// right line, no grep round-trip required.
func formatIssueLocation(path string, iss validationIssue) string {
	switch {
	case iss.line > 0 && iss.column > 0:
		return fmt.Sprintf("%s:%d:%d", path, iss.line, iss.column)
	case iss.line > 0:
		return fmt.Sprintf("%s:%d", path, iss.line)
	default:
		return path
	}
}

type validationIssue struct {
	line   int    // YAML line number (1-based); 0 if unknown.
	column int    // YAML column (1-based); 0 if unknown.
	msg    string // primary message ("unknown key 'auht' — did you mean 'auth'?")
	fix    string // "Fix: rename to 'auth' or remove if unused."
	// warning marks a non-fatal notice. The zero value (false) is an
	// error: it gates the load via ValidationError. Warnings are
	// partitioned out in loadStrict — they never gate, but they are
	// surfaced to the user (see flushConfigWarnings) so silently-dropped
	// config doesn't vanish without a trace.
	warning bool
}

// refusedSchemaKeys are retired forge.yaml keys that FAIL the load, with their
// runbook, instead of warning like removedSchemaKeys.
//
// A removed key normally warns: forge wrote it, and a forge.yaml forge
// authored must keep loading across a schema removal. A key lands here
// instead when silently ignoring it would change what forge DOES — a stale
// value that used to steer a build keeps sitting in the file, the build stops
// honouring it, and nothing says so. A registry is the case in point: a
// project whose images went to `docker.registry` would start pushing to the
// registry its env declares (or stop pushing), with a warning scrolled past
// on a CI log as the only trace.
var refusedSchemaKeys = map[string]string{
	// An image registry is declared in the env's KCL, and nowhere else: no
	// flag, no forge-owned `-D`, no environment variable, no forge.yaml key.
	"docker.registry": "delete the key and declare the registry in each env's KCL — " +
		"`registry = \"<registry>\"` on the env's forge.ClusterTarget (or forge.ControlPlane for a " +
		"hosted env) in deploy/kcl/<env>/main.k. `forge env build <env> --push` pushes there.",
	"deploy.registry": "delete the key: the generated CI workflows name no registry. Declare it in " +
		"each env's KCL — `registry = \"<registry>\"` on the env's forge.ClusterTarget (or " +
		"forge.ControlPlane for a hosted env) in deploy/kcl/<env>/main.k.",

	// features.orm: the ORM's switch is `//forge:no-orm` in internal/db, beside
	// the code it affects. Every other feature can be pinned in `features:`.
	// A stale `orm: false` that merely went unread would start generating an
	// ORM over a hand-written DB layer — so the key is refused, not warned
	// about.
	"features.orm": "delete the key — the ORM is switched off by `//forge:no-orm: <why>` in the doc comment of " +
		"the internal/db package, beside the code it affects. The other features can be pinned here " +
		"(`observability: false`, `hot_reload: false`, ...); `forge project features` shows each one and why.",
	"stack": "delete the block — the frontend inventory is derived from frontends/<name> on disk and from the " +
		"KCL forge.Frontend declarations; there is no framework setting.",
	"frontends": "delete the block — frontends are derived: a directory under frontends/ is detected by its own " +
		"next.config / vite.config / app.json, and a KCL `forge.Frontend {...}` in deploy/kcl/<env>/main.k declares " +
		"the rest. Port, dev_runner, base_path, path or source, and `routes` (the CRUD-page allowlist; `[\"none\"]` for " +
		"no generated pages) are fields of that forge.Frontend.",
	"frontend": "delete the block — the pnpm-workspaces layout is detected from pnpm-workspace.yaml at the project " +
		"root (`forge project new --frontend-workspaces` writes it).",
	"smoke": "delete the block — declare the check on the workload that owns the endpoint, in its KCL: " +
		"`flow_checks = [forge.FlowCheck {path = \"/flow-health\", description = \"...\"}]`. " +
		"`forge env smoke <env>` resolves the URL from that workload's route or port in the env.",
	"database.driver": "delete the key — the driver is derived: a service project with db/migrations uses " +
		"postgres, one without has no database.",
	"database.migrations_dir": "delete the key — migrations live in db/migrations.",
	"database.seed": "delete the block — dev seeding uses fixed defaults (20 rows per table, auto-seed on, " +
		"scoped to the tables behind CRUD entities). `forge env up --no-seed` skips it for one run.",
	"database.migration_safety.allowed_destructive": "delete the key and mark each intentional migration in the " +
		"file itself with a `-- forge:allow-destructive` SQL comment. The exemption then sits next to the SQL it excuses.",
}

// removedSchemaKeys maps a normalized key path of a forge.yaml key that
// was deliberately removed from the schema (as opposed to a typo) to the
// one-line "what to do instead" guidance emitted as the issue's Fix:
// hint. When validation hits one of these, that hint replaces the generic
// "unknown key — did you mean ...?", which would otherwise mislead an
// agent into renaming the key rather than migrating it.
//
// A hint is written for someone who has to edit this file right now, so
// it is imperative, self-contained, and names the skill documenting the
// migration where one exists. It states the CURRENT model — never why an
// old key stopped working. Which internal rework retired a key, and
// whether it was load-bearing before it went, are facts about forge's
// own history: they belong in the per-entry comments below (and in the
// audit trail), not in a message a user reads.
//
// Path normalization: slice indices are collapsed to "[]" (e.g.
// "services[3].dev_target" matches the "services[].dev_target" entry),
// so one entry covers every element of a list. Top-level keys use the
// bare key name. Keys nested under a user-defined map segment (e.g. a
// component's `ports.<name>`) cannot be matched here — a removed key
// must be a fixed schema path, not one below a user-chosen map key.
//
// Audit trail (git history of config.go):
//   - k8s.provider: removed in 01bd491 ("remove dead BinaryConfig.Kind
//     and K8sConfig.Provider fields"). Never load-bearing; per-env
//     cluster choice lives in KCL `forge.K8sCluster` blocks.
//   - binaries[].kind: removed in the same commit. The cron/oneshot
//     kinds were reserved-but-unimplemented; every binary is
//     long-running today.
//   - services[].dev_target: added in cd25640, reverted in 16921aa.
//     Host/cluster placement moved to the per-env `deploy:` field on
//     the KCL `forge.Service` schema.
//   - environments (top level): removed in the KCL-canonical cleanup
//     (8d3e185) — handled separately by deprecatedTopLevelKeys below
//     because mid-migration projects must still LOAD; it is reported as
//     a non-fatal WARNING (not silently skipped) so the user migrates it
//     before the next forge.yaml rewrite drops it.
var removedSchemaKeys = map[string]string{
	// The auth VALIDATOR is no longer config — it is owned code. Picking a
	// validator (JWT/Clerk/Auth0/custom) is a code-wiring choice; the
	// per-DEPLOYMENT values it reads are typed config fields like every
	// other per-environment value. Each service scaffolds an editable
	// internal/app/auth.go whose SetupAuth() the generated cmd serve calls.
	"auth.provider": "delete the key — the validator is picked in code now. Edit " +
		"internal/app/auth.go's SetupAuth() (default: JWT); pin jwt_issuer / " +
		"jwt_audience / jwt_jwks_url in deploy/kcl/<env>/config.k and jwt_secret " +
		"in the env's secret provider.",
	"auth.jwt": "delete the block — jwt_issuer / jwt_audience / jwt_jwks_url are " +
		"typed config fields (proto/<project>/config/v1/config.proto), pinned per env in " +
		"deploy/kcl/<env>/config.k and read by internal/app/auth.go's SetupAuth().",
	"auth.api_key": "delete the block — auth is owned code now. Build an API-key " +
		"validator in internal/app/auth.go's SetupAuth() if you need one.",
	// The whole `auth:` block went with its last field. It survived the
	// auth-mechanism-to-code move as an empty struct, so a bare `auth:`
	// parsed clean and configured nothing.
	"auth": "delete the block — authentication is owned code. Edit " +
		"internal/app/auth.go's SetupAuth() to pick the validator; pin " +
		"jwt_issuer / jwt_audience / jwt_jwks_url in deploy/kcl/<env>/config.k " +
		"and jwt_secret in the env's secret provider.",
	// version: never read by anything. The binary's version is stamped at
	// link time from a KCL GoBuild.ldflags `-X` entry, which is per-env and
	// therefore cannot live in a project-global file.
	"version": "delete the key — stamp the binary version from a KCL " +
		"GoBuild.ldflags `-X` entry in deploy/kcl/<env>/main.k, which is where " +
		"per-environment build facts live.",
	// hot_reload lived at the top level AND under features:. The top-level
	// spelling is gone; features.hot_reload pins it.
	"hot_reload": "delete the key — hot reload derives from a server's sources; pin it with " +
		"`features: {hot_reload: false}` instead.",
	// ci.go_version was written into the workflow template data and read by
	// no template: every setup-go step pins with `go-version-file: go.mod`,
	// so the key changed nothing and a project that set it got a CI Go
	// version it had not asked for, with no warning.
	"ci.go_version": "delete the key — CI reads the toolchain from go.mod " +
		"(`go-version-file: go.mod` in every setup-go step), so set the version " +
		"in go.mod and CI follows.",
	// ci.extra_jobs was a declared "user extension point" that the workflow
	// generator never read: the template ranged over a struct field nothing
	// populated, so every declared job silently vanished.
	"ci.extra_jobs": "delete the block and add the job directly to .github/workflows/ci.yml — " +
		"that file is scaffold-once and yours to edit; forge never re-renders it.",
	// lint.contract had no reader. The contract lint is always on.
	"lint.contract": "delete the key — the contract lint is always on; list the package under " +
		"`contracts.exclude` to exempt just that package.",
	// The contract severity dials had no reader either: the contract rules
	// are unconditional and the only real escape hatch is contracts.exclude.
	"contracts.strict": "delete the key — the contract rules are unconditional. Exempt one package " +
		"via `contracts.exclude`.",
	"contracts.allow_exported_vars": "delete the key — exempt the package via `contracts.exclude`, or opt it out " +
		"in code with a `// forge:exclude-contract` package-doc directive.",
	"contracts.allow_exported_funcs": "delete the key — exempt the package via `contracts.exclude`, or opt it out " +
		"in code with a `// forge:exclude-contract` package-doc directive.",
	// The pack subsystem was RETIRED wholesale. What packs used to install is
	// now split across owned scaffold + libraries: frontend components
	// (the auth UI) are owned scaffold you edit directly, and auth /
	// audit are code plus the forge/pkg/auth, forge/pkg/apikey, forge/pkg/audit
	// libraries. There is nothing to re-install and nothing to migrate — the
	// key simply drops. `features.packs` (the feature that gated the subsystem)
	// went with it.
	"packs": "delete the key — packs are no longer supported. Frontend components are owned " +
		"scaffold (`forge skill load frontend`); auth and audit are code + libraries " +
		"(`forge skill load auth`).",
	"pack_overrides": "delete the block — there are no packs to override. " +
		"Frontend components are owned scaffold and auth/audit are code + libraries (`forge skill load auth`).",
	"k8s.provider": "remove the key — per-environment cluster choice now lives in KCL " +
		"`forge.K8sCluster` blocks under deploy/kcl/.",
	// deploy.provider was never read: the CI provider lives in `ci.provider`
	// (generate_ci.go reads cfg.CI.Provider). Removed in the forge.yaml
	// schema cleanup (FORGE_SHAPE_REDESIGN §4 — deploy is pipeline-control
	// only; provider belongs to ci).
	"deploy.provider": "delete the key — the CI provider is set via `ci.provider` (github is the default).",
	// kind: never lived in forge.yaml. Project kind DERIVES from the
	// project's real sources — the KCL deploy tree, the service registry, the
	// service handlers, and cmd/ binaries. There is no manifest.
	"kind": "delete the key — project kind derives from the real sources: the KCL deploy " +
		"tree (deploy/kcl/), the service registry (pkg/app/services.go), the service handlers " +
		"(internal/handlers/), or a cmd/ binary. To keep a feature that shape turns on off " +
		"(observability, hot_reload, ...), pin it in a `features:` block.",
	// components/services/binaries: forge.yaml is GLOBAL-only. Per-service
	// components are DISCOVERED from real sources (proto descriptor for
	// services, owned code for workers/operators/binaries), never a manifest.
	"components": "delete the key — components are discovered from the real sources (proto " +
		"services, internal/handlers/, cmd/ binaries), not authored in forge.yaml or a manifest.",
	"services": "delete the key — services are discovered from the proto descriptor + " +
		"internal/handlers/; add one with `forge scaffold service <name>`.",
	// packages: was never a codegen input — the bootstrap/injector pass has
	// always walked internal/*/contract.go, so a project whose block was
	// deleted still got every package wired. It steered only the reporters
	// (`forge project map`, `forge project audit`, the architecture doc),
	// which made a stale entry name a package that does not exist while
	// hiding one that does.
	"packages": "delete the key — an internal package is declared by internal/<name>/contract.go, " +
		"and its outbound-boundary claim by the `//forge:outbound-io` marker in its own source; " +
		"add one with `forge scaffold package <name> [--type adapter]`.",
	"binaries": "delete the key — binaries are discovered from their cmd/<name>/main.go; add one with `forge scaffold binary <name>`.",
	// test: backed the orphaned `forge test --env=<env>` port-forward flow,
	// superseded by the `forge env up <env> && go test` two-command loop.
	// The per-env recipe (forwards + env + command) was removed; drive e2e
	// suites with a plain `go test` after `forge env up`.
	"test": "delete the key — bring the env up with `forge env up <env>` and run the " +
		"suite with a plain `go test` (port-forward and env-vars are no longer declared in forge.yaml).",
	"components[].type": "delete `type:` and set `kind:` instead (go_service → server).",
	"binaries[].kind": "remove the key — every `forge scaffold binary` entry is long-running; " +
		"there are no binary kinds.",
	"services[].dev_target": "move host/cluster placement to the per-env `deploy:` field on the KCL " +
		"`forge.Service` schema (`forge.HostDeploy | forge.K8sCluster | forge.External | forge.Compose | forge.BuildOnly`).",
	// serve/served_by shipped only on an unreleased branch (never adopted
	// downstream) before being replaced by registration-in-code: what a
	// binary serves is the row list in pkg/app/services.go, not a yaml
	// knob.
	"components[].serve": "delete the key — what a binary serves is code: to stop serving a " +
		"service from this binary, delete its serviceRow line in pkg/app/services.go and leave a " +
		"comment naming the binary that serves it; see the `services` skill (Types-Only Services).",
	"components[].served_by": "delete the key — document the serving binary as a comment next to the " +
		"deleted serviceRow line in pkg/app/services.go; see the `services` skill " +
		"(Types-Only Services).",
	// stack.{backend,database,proto,deploy,ci} were "forward-looking
	// declarations" that no codegen path ever read — they DUPLICATED the
	// canonical sources. Removed in the forge.yaml schema cleanup
	// (FORGE_SHAPE_REDESIGN §4). Only `stack.frontend` survives. Each old
	// sub-block points the user at the real source of truth.
	// down_files_allowed_until grandfathered pre-policy down migrations as a
	// warning. There is nothing to grandfather: forge never runs a down file,
	// so deleting one is always safe, and every down file is a lint error.
	// `forge project upgrade` stamped this key, so it must warn, not fail.
	"database.migration_safety.down_files_allowed_until": "delete the key and delete the down migrations it " +
		"grandfathered — forge never runs them, so removing them is always safe; `forge lint` errors on every down file.",
	// deploy graduated from experimental to a stable kind-derived flag in
	// the front-door rework; projects scaffolded in the experimental
	// window still carry the old nesting.
	// ingress and operators graduated out of experimental: both are
	// prod-critical, and warning on every invocation about a project's own
	// production configuration bought nobody safety. `forge generate`
	// rewrites these automatically (stepGraduateExperimental); the warning
	// is for the commands that only read.
	// external_builds was deleted, not graduated: it had already been
	// reduced to an inert key nothing consulted (fr-da9a6614fb).
	// forge does not manage documentation. `forge docs generate` and the
	// ADR scaffolding `forge new` wrote are gone, so both switches gate
	// nothing; a project's docs/ directory is entirely its own.
	"docs": "delete the block — forge no longer generates documentation (`forge docs` was removed). " +
		"Anything already under docs/ is yours and is left alone.",
}

// sliceIndexRe matches "[<digits>]" path segments so removed-key lookup
// can collapse "services[3]" to "services[]".
var sliceIndexRe = regexp.MustCompile(`\[\d+\]`)

// normalizeKeyPath collapses slice indices in a dotted key path so it
// can be looked up in removedSchemaKeys.
func normalizeKeyPath(p string) string {
	return sliceIndexRe.ReplaceAllString(p, "[]")
}

// deprecatedTopLevelKeys maps a top-level forge.yaml key that was once
// part of the schema (but has since been removed) to the migration
// guidance shown when it is encountered. These keys are NOT errors: a
// project mid-migration must still LOAD. But they are also NOT silently
// dropped — NormalizeForWrite re-serializes forge.yaml without them, so
// the next rewrite would lose the user's real config (e.g. per-env log
// levels under `environments:`) with zero trace. We emit a warning so
// the loss is visible and the user is pointed at the migration skill.
//
// Currently:
//   - `environments`: removed in the deploy-target-architecture
//     migration. Per-env deploy info (cluster/namespace/registry/
//     domain) now lives in KCL `forge.K8sCluster` blocks; per-env
//     app config lives in sibling `config.<env>.yaml` files.
var deprecatedTopLevelKeys = map[string]string{
	"environments": "this key is no longer part of the forge.yaml schema and will be DROPPED on the next " +
		"forge.yaml rewrite (forge generate / forge project upgrade re-serialize the file). Migrate per-env config " +
		"before you lose it: per-env deploy info moves to KCL `forge.K8sCluster` blocks and per-env app config " +
		"moves to sibling `config.<env>.yaml` files next to forge.yaml.",
}

// walkUnknownKeys recursively descends a yaml.Node mapping against the
// reflected Go type. Unknown keys produce issues with line numbers and
// suggestions; known keys recurse if they map to nested struct or slice
// types.
func walkUnknownKeys(node *yaml.Node, path string, t reflect.Type) []validationIssue {
	var out []validationIssue
	if t == nil {
		return nil
	}
	// Unwrap pointer.
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	// BuildContext owns its scalar-or-GitSource union decoding, including
	// strict key and pin validation; its internal fields are not YAML keys.
	if t == reflect.TypeFor[BuildContext]() {
		return nil
	}
	// We only descend into struct mappings here. Map[string]X with a
	// declared key type just accepts anything (user-chosen keys), so
	// no unknown-key warning at that layer.
	if t.Kind() != reflect.Struct {
		return nil
	}
	known := yamlKeysOf(t)
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		valNode := node.Content[i+1]
		if keyNode.Kind != yaml.ScalarNode {
			continue
		}
		key := keyNode.Value
		// Deprecated keys at the top level do NOT fail validation
		// (projects mid-migration must still load), but they are NOT
		// silently dropped either: the next forge.yaml rewrite would
		// lose the config without a trace. Emit a non-gating warning
		// that names the key and points at the migration skill.
		if path == "" {
			if hint, ok := deprecatedTopLevelKeys[key]; ok {
				out = append(out, validationIssue{
					line:    keyNode.Line,
					column:  keyNode.Column,
					msg:     fmt.Sprintf("%q is a deprecated top-level key", key),
					fix:     hint,
					warning: true,
				})
				continue
			}
		}
		field, ok := known[key]
		if !ok {
			full := qualifiedKey(path, key)
			// Removed keys come FIRST: a key that used to be in the
			// schema gets its specific migration message, never a
			// Levenshtein "did you mean" (which would suggest renaming
			// instead of migrating — the exact trap an agent reading
			// the error would fall into).
			//
			// A removed key is one forge ITSELF wrote a version ago, so it
			// is a WARNING, not a hard fail (fr-57edf33aca): a forge.yaml
			// forge authored must keep loading across a schema removal —
			// every config-loading command hard-failing on forge's own
			// retired key strands the project until a human edits the file.
			// The warning still carries the migration hint, and the next
			// forge.yaml rewrite (NormalizeForWrite) drops the dead key, so
			// the deprecation is visible and self-healing. Genuine typos
			// (NOT in removedSchemaKeys) stay fatal below — that distinction
			// is the whole point of the map.
			if fix, refused := refusedSchemaKeys[normalizeKeyPath(full)]; refused {
				out = append(out, validationIssue{
					line:   keyNode.Line,
					column: keyNode.Column,
					msg:    fmt.Sprintf("%q is no longer a forge.yaml key", full),
					fix:    fix,
				})
				continue
			}
			if fix, removed := removedSchemaKeys[normalizeKeyPath(full)]; removed {
				out = append(out, validationIssue{
					line:    keyNode.Line,
					column:  keyNode.Column,
					msg:     fmt.Sprintf("%q is no longer a forge.yaml key", full),
					fix:     fix,
					warning: true,
				})
				continue
			}
			msg := fmt.Sprintf("unknown key %q", full)
			fix := "rename or remove this key."
			if suggestion := closestMatch(key, knownNames(known)); suggestion != "" {
				msg += fmt.Sprintf(" — did you mean %q?", suggestion)
				fix = fmt.Sprintf("rename to %q or remove if unused.", suggestion)
			}
			out = append(out, validationIssue{line: keyNode.Line, column: keyNode.Column, msg: msg, fix: fix})
			continue
		}
		// Recurse into nested structs and slices of structs.
		ft := field.Type
		switch ft.Kind() {
		case reflect.Struct:
			if valNode.Kind == yaml.MappingNode {
				childPath := joinPath(path, key)
				out = append(out, walkUnknownKeys(valNode, childPath, ft)...)
			}
		case reflect.Slice:
			elem := ft.Elem()
			if elem.Kind() == reflect.Struct && valNode.Kind == yaml.SequenceNode {
				for idx, item := range valNode.Content {
					if item.Kind == yaml.MappingNode {
						childPath := fmt.Sprintf("%s[%d]", joinPath(path, key), idx)
						out = append(out, walkUnknownKeys(item, childPath, elem)...)
					}
				}
			}
		case reflect.Pointer:
			if ft.Elem().Kind() == reflect.Struct && valNode.Kind == yaml.MappingNode {
				childPath := joinPath(path, key)
				out = append(out, walkUnknownKeys(valNode, childPath, ft.Elem())...)
			}
		case reflect.Map:
			// Map[string]Struct: descend into each entry's value, where
			// the key is user-defined (e.g. a component's port names) so
			// we can't validate the key itself.
			if ft.Elem().Kind() == reflect.Struct && valNode.Kind == yaml.MappingNode {
				for j := 0; j+1 < len(valNode.Content); j += 2 {
					entryKey := valNode.Content[j]
					entryVal := valNode.Content[j+1]
					if entryVal.Kind == yaml.MappingNode {
						childPath := fmt.Sprintf("%s.%s", joinPath(path, key), entryKey.Value)
						out = append(out, walkUnknownKeys(entryVal, childPath, ft.Elem())...)
					}
				}
			}
		}
	}
	return out
}

// yamlKeysOf returns a map from yaml-tag-name -> reflect.StructField for
// every field declared on t. Embedded structs are flattened so their
// keys appear at the parent level (yaml.v3 default behaviour).
func yamlKeysOf(t reflect.Type) map[string]reflect.StructField {
	out := make(map[string]reflect.StructField)
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("yaml")
		if tag == "" || tag == "-" {
			if f.Anonymous {
				maps.Copy(out, yamlKeysOf(f.Type))
			}
			continue
		}
		name := strings.SplitN(tag, ",", 2)[0]
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		out[name] = f
	}
	return out
}

func knownNames(m map[string]reflect.StructField) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func joinPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

func qualifiedKey(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

// closestMatch returns the closest entry in candidates to needle by
// Levenshtein distance, or "" if no candidate is close enough. Threshold
// scales with needle length: short keys (< 8 chars) require <= 2,
// longer keys allow <= 3.
func closestMatch(needle string, candidates []string) string {
	if needle == "" || len(candidates) == 0 {
		return ""
	}
	threshold := 2
	if len(needle) >= 8 {
		threshold = 3
	}
	best := ""
	bestDist := threshold + 1
	for _, c := range candidates {
		d := levenshtein(strings.ToLower(needle), strings.ToLower(c))
		if d < bestDist {
			bestDist = d
			best = c
		}
	}
	if bestDist <= threshold {
		return best
	}
	return ""
}

// levenshtein returns the edit distance (insert/delete/substitute, all
// cost 1) between a and b. Implementation uses a single-row DP buffer
// for O(min(len)) memory.
func levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min(curr[j-1]+1, min(prev[j]+1, prev[j-1]+cost))
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}

// splitYAMLErrorLines turns a yaml decoding error into one issue per
// underlying problem. yaml.v3's TypeError aggregates issues with newlines
// in its message, while plain errors have a single line.
func splitYAMLErrorLines(err error) []string {
	if err == nil {
		return nil
	}
	msg := err.Error()
	// yaml.v3 prefixes TypeError messages with "yaml: unmarshal errors:\n  ".
	msg = strings.TrimPrefix(msg, "yaml: unmarshal errors:\n")
	parts := strings.Split(msg, "\n")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		// Skip the "field X not found" lines — phase 1 covered those
		// with better suggestions.
		if p == "" || strings.Contains(p, " not found in type ") {
			continue
		}
		// Trim the leading "yaml: " prefix when present so the message
		// reads cleanly under our path-prefixed format.
		p = strings.TrimPrefix(p, "yaml: ")
		out = append(out, p)
	}
	return out
}

// validateRequired checks that fields the project cannot meaningfully
// be missing are present. The list intentionally stays small — every
// required field here corresponds to a real downstream breakage when
// absent (broken go.mod, empty deploy, ambiguous codegen target).
func validateRequired(cfg *ProjectConfig, root *yaml.Node) []validationIssue {
	var out []validationIssue
	out = append(out, validateProjectFields(cfg, root)...)
	out = append(out, validateConfigGuard(cfg, root)...)
	out = append(out, validateDevStack(cfg, root)...)
	out = append(out, validateAPIProtoPackage(cfg, root)...)
	out = append(out, validateGenerateHooks(cfg, root)...)
	out = append(out, validateUpgradeConfig(cfg, root)...)
	return out
}

// protoPackageSettingRE accepts a dotted proto package whose segments may be
// the {service} placeholder — the shape api.proto_package must have.
var protoPackageSettingRE = regexp.MustCompile(`^(?:[A-Za-z_]\w*|\{service\})(?:\.(?:[A-Za-z_]\w*|\{service\}))*$`)

// validateAPIProtoPackage rejects an api.proto_package that cannot be a proto
// package. Caught here rather than at scaffold time because the failure
// otherwise surfaces as a buf parse error in a file forge just wrote, far
// from the line that caused it.
func validateAPIProtoPackage(cfg *ProjectConfig, root *yaml.Node) []validationIssue {
	setting := strings.TrimSpace(cfg.API.ProtoPackage)
	if setting == "" || protoPackageSettingRE.MatchString(setting) {
		return nil
	}
	line, col := findNodePos(root, []string{"api", "proto_package"})
	return []validationIssue{{
		line:   line,
		column: col,
		msg:    fmt.Sprintf("api.proto_package value %q is not a proto package", cfg.API.ProtoPackage),
		fix: "use dot-separated identifiers, optionally with the {service} placeholder: " +
			"\"controlplane.v1\" (every service shares it) or \"acme.{service}.v1\" (one per service). " +
			"Delete the key to infer it from the existing service protos.",
	}}
}

// validateDevStack checks the `dev_stack:` bounds. Absent (or zero) is valid
// and resolves to the defaults; a NEGATIVE value is rejected rather than
// clamped, because it can only be a mistake and silently treating it as the
// default would hide the typo in the one file the author went to edit.
//
// max_stacks: 1 is legal and meaningful — the default stack alone, no
// worktree parallelism.
func validateDevStack(cfg *ProjectConfig, root *yaml.Node) []validationIssue {
	var out []validationIssue

	if cfg.DevStack.MaxStacks < 0 {
		line, col := findNodePos(root, []string{"dev_stack", "max_stacks"})
		out = append(out, validationIssue{
			line:   line,
			column: col,
			msg:    fmt.Sprintf("dev_stack.max_stacks value %d is invalid", cfg.DevStack.MaxStacks),
			fix: fmt.Sprintf("use a positive count of parallel dev stacks (absent defaults to %d, "+
				"which is the default stack plus %d linked worktrees).", DefaultMaxStacks, DefaultMaxStacks-1),
		})
	}

	if cfg.DevStack.BlockSize < 0 {
		line, col := findNodePos(root, []string{"dev_stack", "block_size"})
		out = append(out, validationIssue{
			line:   line,
			column: col,
			msg:    fmt.Sprintf("dev_stack.block_size value %d is invalid", cfg.DevStack.BlockSize),
			fix: fmt.Sprintf("use a positive port-number distance between adjacent stacks "+
				"(absent defaults to %d).", DefaultBlockSize),
		})
	}

	return out
}

// validateConfigGuard checks the `config:` section's enumerated
// enforce_typed_access value. Empty is valid (resolves to "warn"); any
// non-empty value outside {off, warn, error} (case/alias-normalized) is a
// fatal, clearly-explained error rather than a silently-ignored typo.
func validateConfigGuard(cfg *ProjectConfig, root *yaml.Node) []validationIssue {
	var out []validationIssue

	if raw := strings.TrimSpace(cfg.Config.EnforceTypedAccess); raw != "" {
		switch strings.ToLower(raw) {
		case EnforceTypedAccessOff, EnforceTypedAccessWarn, EnforceTypedAccessError, "warning":
			// valid
		default:
			line, col := findNodePos(root, []string{"config", "enforce_typed_access"})
			out = append(out, validationIssue{
				line:   line,
				column: col,
				msg:    fmt.Sprintf("config.enforce_typed_access value %q is invalid", cfg.Config.EnforceTypedAccess),
				fix:    "use one of: off, warn, error (absent defaults to warn).",
			})
		}
	}

	return out
}

// validateProjectFields checks the top-level project identity fields
// (name, module_path) that the project cannot meaningfully be missing.
func validateProjectFields(cfg *ProjectConfig, root *yaml.Node) []validationIssue {
	var out []validationIssue

	// rootPos is the fallback location for "this required field is
	// missing entirely from the file" — we point at the top-level
	// mapping (line 1, col 1) so the model knows it's a forge.yaml-wide
	// concern, not a nested-block one.
	var rootLine, rootCol int
	if root != nil {
		rootLine, rootCol = root.Line, root.Column
	}

	if strings.TrimSpace(cfg.Name) == "" {
		out = append(out, validationIssue{
			line:   rootLine,
			column: rootCol,
			msg:    "'name' is required but missing or empty",
			fix:    "add 'name: <project-name>' near the top of forge.yaml.",
		})
	}
	// A static site has no Go module, so it has no module path to declare
	// (HasGoModule). The moment it gains a go.mod the field is required again.
	if strings.TrimSpace(cfg.ModulePath) == "" && cfg.HasGoModule() {
		out = append(out, validationIssue{
			line:   rootLine,
			column: rootCol,
			msg:    "'module_path' is required but missing or empty",
			fix:    "add 'module_path: github.com/<org>/<project>' near the top of forge.yaml.",
		})
	} else if strings.TrimSpace(cfg.ModulePath) != "" && !looksLikeGoModulePath(cfg.ModulePath) {
		// Existing-but-invalid: point at the actual `module_path:` line.
		line, col := findNodePos(root, []string{"module_path"})
		out = append(out, validationIssue{
			line:   line,
			column: col,
			msg:    fmt.Sprintf("'module_path' value %q does not look like a Go module path", cfg.ModulePath),
			fix:    "use a path like 'github.com/<org>/<project>' (must contain a slash, no spaces).",
		})
	}
	// kind is not a forge.yaml field — it is DERIVED from the project's real
	// sources (deriveProjectKindFromSources) before validateRequired runs, so
	// it is always one of the valid values and needs no validation here.

	if reason := validateORMPackage(cfg.Database.ORM.Package); reason != "" {
		line, col := findNodePos(root, []string{"database", "orm", "package"})
		out = append(out, validationIssue{
			line:   line,
			column: col,
			msg:    fmt.Sprintf("'database.orm.package' value %q %s", cfg.Database.ORM.Package, reason),
			fix:    "use a project-relative Go package directory such as 'internal/store', or delete the key for the default internal/db.",
		})
	}

	if v := strings.TrimSpace(cfg.Database.PostgresVersion); v != "" && !postgresVersionRE.MatchString(strings.TrimPrefix(v, "v")) {
		line, col := findNodePos(root, []string{"database", "postgres_version"})
		out = append(out, validationIssue{
			line:   line,
			column: col,
			msg:    fmt.Sprintf("'database.postgres_version' value %q is not a postgres major or release", cfg.Database.PostgresVersion),
			fix:    "use the major production runs, quoted: postgres_version: \"16\" (or an exact release such as \"16.9.0\").",
		})
	}

	return out
}

// postgresVersionRE is the shape of database.postgres_version. Which majors
// can actually boot is pgtest's to say (pgtest.ResolveVersion), when it does.
var postgresVersionRE = regexp.MustCompile(`^\d+(\.\d+){0,2}$`)

// ormPackageSegmentRE is one directory of database.orm.package: what a Go
// import path element and, for the last one, a package name can be.
var ormPackageSegmentRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// validateORMPackage returns why pkg is not a usable ORM package
// directory, "" when it is (or is unset).
func validateORMPackage(pkg string) string {
	pkg = strings.TrimSpace(pkg)
	if pkg == "" {
		return ""
	}
	if strings.HasPrefix(pkg, "/") || strings.Contains(pkg, "\\") {
		return "must be a project-relative, slash-separated directory"
	}
	for _, seg := range strings.Split(strings.Trim(pkg, "/"), "/") {
		if !ormPackageSegmentRE.MatchString(seg) {
			return fmt.Sprintf("has the element %q; each must be a lower-case Go package name (letters, digits, underscore)", seg)
		}
	}
	switch last := path.Base(strings.Trim(pkg, "/")); last {
	case "main", "internal", "testdata", "vendor":
		return fmt.Sprintf("ends in %q, which cannot name the ORM's Go package", last)
	}
	return ""
}

// basePathSegmentRE matches one path segment of a frontend base_path:
// letters, digits, dot, underscore, hyphen. Deliberately narrower than
// what URLs technically allow — the value is spliced verbatim into
// next.config.ts (basePath / assetPrefix) and into generated TypeScript
// string literals, so "no fancy chars" is the safety contract.
var basePathSegmentRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidateBasePath checks the shape of a non-empty frontend base_path
// value. Returns (reason, false) on failure, ("", true) when valid.
//
// Valid:   "/admin", "/internal/admin", "/v2.1_beta"
// Invalid: "admin" (no leading slash), "/admin/" (trailing slash),
//
//	"/" (root mount — omit the field instead), "/ad min", "/a%2Fb".
func ValidateBasePath(bp string) (string, bool) {
	if !strings.HasPrefix(bp, "/") {
		return `must start with "/"`, false
	}
	if bp == "/" {
		return `bare "/" means root mounting — omit base_path instead`, false
	}
	if strings.HasSuffix(bp, "/") {
		return `must not end with "/"`, false
	}
	for _, seg := range strings.Split(bp[1:], "/") {
		if seg == "" {
			return "must not contain empty segments (\"//\")", false
		}
		if !basePathSegmentRE.MatchString(seg) {
			return fmt.Sprintf("segment %q contains characters outside [A-Za-z0-9._-]", seg), false
		}
	}
	return "", true
}

// findNodePos walks a YAML mapping/sequence tree along a dot/index path
// and returns the line/col of the resolved node. Path segments are
// either bare keys (e.g. "module_path") or sequence indices in literal
// `[N]` form (e.g. "[0]") — same shape used in qualifiedKey output so
// callers can construct paths once and reuse them across issue messages
// and position lookups. Returns (0, 0) when the path doesn't resolve;
// callers fall back to the root position (or omit position entirely)
// in that case.
func findNodePos(node *yaml.Node, segments []string) (int, int) {
	if node == nil {
		return 0, 0
	}
	cur := node
	for _, seg := range segments {
		if cur == nil {
			return 0, 0
		}
		if strings.HasPrefix(seg, "[") && strings.HasSuffix(seg, "]") {
			// Sequence index.
			if cur.Kind != yaml.SequenceNode {
				return 0, 0
			}
			idx := 0
			if _, err := fmt.Sscanf(seg, "[%d]", &idx); err != nil {
				return 0, 0
			}
			if idx < 0 || idx >= len(cur.Content) {
				return 0, 0
			}
			cur = cur.Content[idx]
			continue
		}
		// Mapping key lookup.
		if cur.Kind != yaml.MappingNode {
			return 0, 0
		}
		var matched *yaml.Node
		for i := 0; i+1 < len(cur.Content); i += 2 {
			if cur.Content[i].Kind == yaml.ScalarNode && cur.Content[i].Value == seg {
				matched = cur.Content[i+1]
				break
			}
		}
		if matched == nil {
			return 0, 0
		}
		cur = matched
	}
	if cur == nil {
		return 0, 0
	}
	return cur.Line, cur.Column
}

// looksLikeGoModulePath does a cheap shape check so we catch obvious
// typos (e.g. a stray period only) without trying to be a full Go
// modules validator. The Go module path rule we enforce: contains at
// least one slash and no whitespace.
func looksLikeGoModulePath(s string) bool {
	if strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	if !strings.Contains(s, "/") {
		return false
	}
	return true
}
