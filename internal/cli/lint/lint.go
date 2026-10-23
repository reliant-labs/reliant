package lint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/contractcheck"
	"github.com/reliant-labs/forge/internal/linter/forgeconv"
	"github.com/reliant-labs/forge/internal/linter/suppress"
	"github.com/reliant-labs/forge/internal/projectstore"
	"github.com/reliant-labs/forge/internal/templates"
)

// lintFlags holds the flag values for the lint command.
type lintFlags struct {
	contract          bool
	migrationSafety   bool
	fix               bool
	noFix             bool
	check             bool
	exportedVars      bool
	conventions       bool
	frontendStores    bool
	scaffolds         bool
	tests             bool
	banners           bool
	suggestExcludes   bool
	suggestBufExcepts bool
	checkWorkarounds  bool
	optionalDepsGuard bool
	configDeps        bool
	columnMarkers     bool
	closedSets        bool
	dbGrants          bool
	crudFixtures      bool
	fixtureDrift      bool
	timeBucketing     bool
	protoMarkers      bool
	goMarkers         bool
	protoOptions      bool
	createNullability bool
	computedFields    bool
	readOnlyFields    bool
	secrets           bool
	guardedFields     bool
	staticExport      bool
	tracing           bool
	vendoredProtos    bool
	configReach       bool
	graph             bool
	slowTests         bool
	generatedDrift    bool
	strict            bool
	skipFrontends     bool
	// allowSkip is --allow-skip: `<lane>[,<lane>…]=<reason>` values
	// accepting that a gating lane may not run (lint_skip.go).
	allowSkip []string
	jsonOut   bool
	// quiet prints only failing lanes, their error findings and the
	// verdict (lint_structured.go).
	quiet bool
	// scope restricts findings to files under these project-relative
	// paths (lint_structured.go).
	scope []string
	// gateJSON is a FILE path: write this run's result as a gate document
	// for `forge gate record` / `forge env deploy --gate`. Not a stdout
	// mode — the human findings and the exit code are unchanged.
	gateJSON string
	// showSuppressed prints the inventory of every declared suppression
	// instead of linting (lint_show_suppressed.go).
	showSuppressed bool
}

// lintLongHelp is `forge lint --help`'s long description.
const lintLongHelp = `Run various linters on the Forge project.

This command will:
- Run standard Go linters (golangci-lint)
- Run proto linters (buf lint)
- Lint and TYPECHECK every frontend the project declares (forge resolves the
  set from forge.yaml — no shell loop over frontends/*/ required). The
  typecheck runs the project's own "typecheck" npm script when it has one,
  and otherwise runs the frontend's TypeScript compiler with --noEmit.
  Severity is lint.frontend.typecheck (error | warn | off) in forge.yaml.
- Optionally run targeted rule sets (--contract, --db, --migration-safety,
  --conventions, --tests). Name several to run them all under one verdict:
  forge lint --conventions --contract

REPORT-ONLY BY DEFAULT. 'forge lint' is a gate, and a gate reports: it writes
NOTHING. A file a fixer would rewrite is printed as the diff the rewrite would
apply and fails the run, so the author sees exactly what to change. (--check
and --no-fix spell the same thing explicitly; --json is always detect-only.)

--fix applies the mechanical fixes, REWRITING FILES IN PLACE, then gates on the
residue: it formats your Go source (never generated files) to the project's
own gate — gofmt, plus goimports' import grouping with the local-prefixes your
golangci-lint config declares, only when that config enables goimports —
rewrites log calls to pass their context and contract constructions to
their traced NewInstrumented (tracing lint), runs each frontend's
own prettier over the src/ files you own, and runs eslint --fix on frontends.
It never rewrites a file the project's own formatter gate accepts, and it
never applies golangci-lint's fixes: staticcheck's and gocritic's rewrites
are code changes (SA9004 changes a constant's type), so they stay findings.
Every Go rewrite is verified — the packages it touched are type-checked, and a
fix that leaves one failing is taken back and reported. Under --scope it
touches only files inside the scope, and naming Go package paths (forge lint
--fix ./internal/x/...) likewise confines the fixers to those packages.

SEVERITY IS ONE DIAL: forge.yaml lint.rules maps a rule ID — the [rule] each
finding prints — to error, warn or off, and it grades every forge lane the same
way in every mode: lint.rules: {forge-config-deps: error} makes the advisory
config-deps lane gate; {forgeconv-handler-file-size: warn} lets a gating rule
be adopted gradually; "*" sets every forge rule. Another tool's linters keep
their own config (.golangci.yml, buf.yaml, eslint), and a key forge does not
grade — a typo, another tool's rule, a lane's could-not-run — is a lint error.

Examples:
  forge lint                     # Report only: print the diffs, write nothing
  forge lint --fix               # Apply the mechanical fixes, then gate
  forge lint --fix ./internal/x/...  # ...only in the packages you are working on
  forge lint --skip-frontends    # Backend-only gate; no Node toolchain needed
  forge lint --contract          # Run contract interface enforcement linter
  forge lint --db                # Run DB entity lint rules
  forge lint --migration-safety  # Run SQL migration safety checks
  forge lint --conventions       # Run forge convention rules on proto files
  forge lint --generated-drift   # Fail if a forge-generated ("DO NOT EDIT")
                                 # file was hand-edited
  forge lint --tests             # Run test-convention rules across backend
                                 # handlers and frontend hooks (warnings only)
  forge lint --config-deps       # Flag scalar Deps fields — a scalar is
                                 # configuration, not a collaborator; prints the
                                 # exact proto block + AppConfig line to write
  forge lint --column-markers    # Fail on a COMMENT ON TABLE/VIEW/COLUMN/
                                 # CONSTRAINT carrying a forge:* marker that
                                 # object does not read
  forge lint --fixture-drift     # Execute every literal fixture statement in
                                 # a scaffolded handlers_crud_test.go against
                                 # the CURRENT schema (shadow postgres,
                                 # rolled back) and report postgres's own
                                 # error per statement it rejects
  forge lint --time-bucketing    # Flag a two-argument date_trunc over a
                                 # TIMESTAMPTZ column — it truncates in the
                                 # SESSION timezone, so every bucketed total
                                 # moves with the deploy host. Pin the zone:
                                 # date_trunc('day', col, 'UTC')
  forge lint --proto-markers     # Flag a .proto comment carrying an
                                 # unrecognized forge:* marker (a misspelled
                                 # one does nothing and warns nowhere)
  forge lint --create-nullability # Fail when a field's optional label
                                 # disagrees between an entity message and
                                 # its Create<Entity>Request (the flattened
                                 # request silently drops write presence)
  forge lint --computed-fields   # FAIL on a forge:computed field that no
                                 # non-generated Go file assigns — nothing
                                 # populates it, so the column default ships
  forge lint --read-only-fields  # FAIL on a forge:read-only column that
                                 # nothing populates — no write path, no
                                 # meaningful DEFAULT, so every row ships as
                                 # the zero with no other symptom at all.
                                 # Both WARN instead ("pending: implement X")
                                 # while the service still holds forge's own
                                 # unwired rpc stubs
  forge lint --secrets           # FAIL on a platform-secrets defect, with
                                 # file:line and its escape
  forge lint --guarded-fields    # Flag a scaffolded edit page whose
                                 # update_mask still writes a column a
                                 # custom rpc guards (forge:guards) — the
                                 # form bypasses the rpc's own rules, and
                                 # scaffold-once means regenerating cannot
                                 # fix it
  forge lint --static-export     # For a Next.js frontend that must be a static
                                 # export (output: static, or bound to a static
                                 # runtime): FAIL on what the export refuses
                                 # ([id] without generateStaticParams, server
                                 # actions, cookies(), next/image, no export)
                                 # and WARN on what it drops (middleware,
                                 # rewrites, POST handlers, no trailingSlash
                                 # on a bucket). CI's next build is the
                                 # authority; this says it first, file:line
  forge lint --tracing           # Report where a request's trace is cut (a
                                 # log call that drops the ctx in scope, a
                                 # raw *http.Client or sql.Open, a contract
                                 # built without its decorator) and rewrite
                                 # each log call to its ...Context form and
                                 # each such New to NewInstrumented
  forge lint --proto-options     # Flag a (forge.v1.*) annotation naming an
                                 # option field forge's descriptors do not
                                 # define — it compiles, and forge reads it
                                 # nowhere
  forge lint --vendored-protos   # Fail when proto/forge/v1/forge.proto has
                                 # drifted from this forge binary's copy
  forge lint --config-reach      # Flag config fields no binary or frontend
                                 # loads
  forge lint --optional-deps-guard  # Flag unguarded derefs of an optional dep
  forge lint --frontend-stores   # Flag Zustand stores holding server data that
                                 # belongs in a generated React Query hook
  forge lint --json              # Machine-readable findings for sub-agents / CI
                                 # (schema in lint_json.go; exit code matches
                                 # text mode; combines with the targeted flags
                                 # above, but not with --fix / --suggest-*)
  forge lint --quiet             # Only what FAILED, then the verdict as the
                                 # last line — safe to pipe through head/tail.
                                 # Combines with any targeted flag above:
                                 #   forge lint --read-only-fields --quiet
  forge lint --show-suppressed   # Every suppression the project declares, with
                                 # file:line and reason. Lints nothing
  forge lint --scope internal/handlers/jobs --scope proto/services/jobs
                                 # Only findings in files under those paths.
                                 # golangci-lint/contract run on the scope's
                                 # packages; whole-project linters (frontend
                                 # lint, graph, component-drift, config-reach) are
                                 # skipped and named in the verdict, so run an
                                 # unscoped 'forge lint' before merging

The advisory rules above are warnings by default — they fail the build only
when lint.rules sets them to error.

Additional maintainer/debug flags exist (forge-repo internals, wiring
audits, suggest-* helpers); run 'forge lint --help-dev' to list them.`

func newCmd(_ *factory.Factory) *cobra.Command {
	var flags lintFlags

	cmd := &cobra.Command{
		Use:   "lint [paths...]",
		Short: "Run linters on the project (report-only: writes nothing unless --fix)",
		Long:  lintLongHelp,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLintCommand(cmd, flags, args)
		},
	}

	registerLintFlags(cmd, &flags)

	return cmd
}

// runLintCommand picks the driver for one `forge lint` invocation: the
// suppression inventory, JSON, the structured (--quiet / --scope) renderer,
// a gate document, or the text driver.
func runLintCommand(cmd *cobra.Command, flags lintFlags, args []string) error {
	paths := []string{"./..."}
	if len(args) > 0 {
		paths = args
	}
	if err := validateLintModeFlags(flags, len(args) > 0); err != nil {
		return err
	}
	if flags.showSuppressed {
		return runShowSuppressed(cmd.OutOrStdout(), flags.jsonOut, flags.scope)
	}
	// A full lint (golangci-lint, every frontend's eslint and tsc) is heavy:
	// take a host heavy slot (pkg/hostslot) so it queues behind the other
	// heavy jobs on this machine instead of slowing all of them down.
	release, err := cmdutil.HoldHeavySlot(cmd.Context(), cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	defer release()
	switch {
	case flags.jsonOut:
		return runLintJSON(cmd.Context(), flags, paths)
	case flags.suggestExcludes:
		_, cfg, err := loadLintConfig()
		if err != nil {
			return err
		}
		return runSuggestExcludes(cfg)
	case flags.suggestBufExcepts:
		return runSuggestBufExcepts(cmd.Context())
	}
	// Every other mode — the full sweep, the targeted `--<linter>` lanes,
	// --quiet, --scope and --gate-json — renders the one outcome engine, so
	// lint.rules, --scope and the gap policy mean the same thing in each
	// (lint_structured.go, lint_rules.go).
	return runLintStructured(cmd.Context(), flags, paths)
}

// registerLintFlags declares every `forge lint` flag and the visible /
// hidden split between them.
//
// Split out of newCmd because that function is a long help text plus this
// list, and the two grow independently: leaving them together kept newCmd at
// its funlen budget, so the next person adding a lane had to choose between
// their flag and a lint failure that is not about their change.
//
// collapsing it into a table would hide the visible/hidden split and the
// per-flag notes about cobra's backtick parsing, which are the only reason
// several of these usage strings are written the way they are.
//
//nolint:funlen // One declaration per flag, and the list IS the surface:
func registerLintFlags(cmd *cobra.Command, flags *lintFlags) {
	cmd.Flags().BoolVar(&flags.contract, "contract", false, "Run contract interface enforcement linter")
	cmd.Flags().BoolVar(&flags.exportedVars, "exported-vars", false, "Run exported vars linter")
	cmd.Flags().BoolVar(&flags.migrationSafety, "migration-safety", false, "Run SQL migration safety checks")
	cmd.Flags().BoolVar(&flags.conventions, "conventions", false, "Run forge convention rules on proto files")
	// See the note on --optional-deps-guard: no backticks in a usage string,
	// or cobra renders the quoted span as the flag's argument placeholder.
	cmd.Flags().BoolVar(&flags.generatedDrift, "generated-drift", false, "Fail when a forge-generated (\"Code generated by forge. DO NOT EDIT.\") file was edited after forge wrote it")
	cmd.Flags().BoolVar(&flags.frontendStores, "frontend-stores", false, "Flag Zustand stores that import generated Connect clients (warnings only)")
	cmd.Flags().BoolVar(&flags.scaffolds, "scaffolds", false, "Enforce forge ownership boundary (FORGE_SCAFFOLD markers + _gen file headers)")
	cmd.Flags().BoolVar(&flags.tests, "tests", false, "Run test-convention rules (handler-tests-use-tdd + frontend-hook-tests; warnings only)")
	cmd.Flags().BoolVar(&flags.banners, "banners", false, "Verify forge templates carry the right Tier-1 / Tier-2 lifecycle banner (warnings only; no-op outside the forge repo)")
	cmd.Flags().BoolVar(&flags.suggestExcludes, "suggest-excludes", false, "Print a YAML snippet of contracts.exclude candidates (heuristic-based; nothing is mutated)")
	cmd.Flags().BoolVar(&flags.suggestBufExcepts, "suggest-buf-excepts", false, "Run buf lint and print a buf.yaml lint.except snippet for STANDARD rules that fired on more than 3 .proto files (nothing is mutated)")
	cmd.Flags().BoolVar(&flags.checkWorkarounds, "check-workarounds", false, "Flag cross-lane workarounds (cast<X>Repo helpers, testing_extras.go, undeclared cmd/<name>.go) — warnings only")
	// No backticks in this usage string: cobra reads the first backticked
	// span as the flag's argument-name placeholder, so `// forge:optional-dep`
	// rendered as `--optional-deps-guard // forge:optional-dep` in --help.
	// Harmless while the flag was hidden; visible now that it is not.
	cmd.Flags().BoolVar(&flags.optionalDepsGuard, "optional-deps-guard", false, "Flag unguarded derefs of // forge:optional-dep Deps fields (warnings by default, lint.rules forge-optional-deps-guard: error gates them; suppress a confirmed-safe site with // forge:optional-checked on the deref line)")
	cmd.Flags().BoolVar(&flags.configDeps, "config-deps", false, "Flag scalar Deps fields — scalars are configuration; declare a <Component>Config block in proto/<project>/config/v1 and take it as a typed field (warnings by default, lint.rules forge-config-deps: error gates them)")
	cmd.Flags().BoolVar(&flags.columnMarkers, "column-markers", false, "Fail on a COMMENT ON TABLE/VIEW/COLUMN/CONSTRAINT carrying a forge: marker that object does not read")
	cmd.Flags().BoolVar(&flags.closedSets, "closed-sets", false, "Fail when a CHECK declared forge:closed-set <set> does not list exactly that Go set, and print the migration that fixes it")
	cmd.Flags().BoolVar(&flags.dbGrants, "db-grants", false, "Fail on a credential-shaped column (token, password, key, ciphertext, hash…) that carries neither forge:secret nor forge:readable <reason>: the read-only forge_ro role is refused it until someone decides")
	cmd.Flags().BoolVar(&flags.fixtureDrift, "fixture-drift", false, "Execute every literal fixture statement in a scaffolded handlers_crud_test.go against the current schema — migrations applied to a shadow postgres, each statement run in a rolled-back transaction — and report postgres's own error for each one it rejects: a GENERATED column, a UNIQUE collision, a dangling foreign key, a CHECK a later migration added (warnings only)")
	// --crud-fixtures checked one failure class (a dangling foreign key) by
	// reading migration text; --fixture-drift now executes the statements, so
	// postgres reports that class along with every other. Kept as a hidden
	// alias so an existing script keeps running the check it asked for.
	cmd.Flags().BoolVar(&flags.crudFixtures, "crud-fixtures", false, "Alias of --fixture-drift")
	_ = cmd.Flags().MarkDeprecated("crud-fixtures", "use --fixture-drift, which now executes each fixture statement against the shadow schema and reports foreign-key violations along with every other rejection")
	cmd.Flags().BoolVar(&flags.timeBucketing, "time-bucketing", false, "Flag a two-argument date_trunc over a TIMESTAMPTZ column — postgres truncates it in the SESSION timezone, which the driver sets from the client host, so every bucketed total is attributed to the wrong day by an amount that changes with the deploy host. Pin the zone: date_trunc('day', col, 'UTC') (warnings only)")
	cmd.Flags().BoolVar(&flags.goMarkers, "go-markers", false, "Flag //forge: comments in .go files that forge does not read — an unknown marker name, or a known marker in a spelling its parser ignores (“//forge:exclude-contract — why” instead of “: why”); each is an error naming file:line and the expected form")
	cmd.Flags().BoolVar(&flags.protoMarkers, "proto-markers", false, "Flag .proto comments containing forge: that match no known proto marker — a misspelled marker is inert and warns nowhere (warnings only)")
	cmd.Flags().BoolVar(&flags.createNullability, "create-nullability", false, "Fail when a field's optional label disagrees between an entity message and its Create<Entity>Request — the flattened request drops write presence silently")
	cmd.Flags().BoolVar(&flags.computedFields, "computed-fields", false, "Flag a forge:computed field that no non-generated Go file assigns — nothing populates it, so the insert takes the column default. FAILS the build, except while the service still holds forge-scaffolded unwired rpc stubs (then a warning naming them)")
	cmd.Flags().BoolVar(&flags.readOnlyFields, "read-only-fields", false, "Flag a forge:read-only field whose column nothing populates — no non-generated Go file assigns it, no meaningful DEFAULT, not GENERATED — so every row ships as the type's zero with no error anywhere. FAILS the build, except while the service still holds forge-scaffolded unwired rpc stubs (then a warning naming them)")
	cmd.Flags().BoolVar(&flags.secrets, "secrets", false, "Judge the platform secrets model per env, with file:line: no ClusterSecretStore/ClusterExternalSecret/PushSecret, ExternalSecret policy (Orphan or Merge, Retain, namespaced store, explicit keys), every Secret a workload reads is produced by exactly one declaration that lists the key, an immutable store name is pinned at 1, an out_of_band_reason that explains, no reasonless OpenBao wildcard read, and (warning) an OpenBao escrowed inside its own cluster. FAILS the build on an error; every message names its escape")
	cmd.Flags().BoolVar(&flags.guardedFields, "guarded-fields", false, "Flag a scaffolded edit page whose update_mask still names a column declared 'forge:guards' — saving the form writes it raw and bypasses the rpc that owns it, and pages are scaffold-once so 'forge generate' cannot repair them (warnings only)")
	cmd.Flags().BoolVar(&flags.staticExport, "static-export", false, "For each Next.js frontend that must build to a static export (forge.yaml output: static, or bound to forge.OnHosted / OnBucket / OnFirebase in any env), report with file:line what the export cannot serve. FAILS on what next build refuses (a dynamic route without generateStaticParams, server actions, next/headers, next/image without images.unoptimized, a build that is not an export); WARNS on what it silently drops (middleware, non-GET route handlers, ungated rewrites/redirects/headers, a bucket-bound export without trailingSlash)")
	cmd.Flags().BoolVar(&flags.protoOptions, "proto-options", false, "Flag (forge.v1.*) annotation fields this forge binary's descriptors do not define — a retired or misspelled option field compiles under buf and is read by nothing (warnings only)")
	cmd.Flags().BoolVar(&flags.vendoredProtos, "vendored-protos", false, "Fail when a vendored proto (proto/forge/v1/forge.proto) differs from the copy embedded in this forge binary — forge's upgrade path does not track these copies, so drift is otherwise invisible")
	cmd.Flags().BoolVar(&flags.graph, "graph", false, "Check the composition graph (internal/app) as a whole: every component and provider is reached by some root, and every listener serves only services of its chain's audience. FAILS the build on an error (graph mode only)")
	cmd.Flags().BoolVar(&flags.configReach, "config-reach", false, "Flag config fields that no binary and no frontend loads — with per-binary configs, an unbound config message generates but is never loaded (warnings only)")
	cmd.Flags().BoolVar(&flags.slowTests, "slow-tests", false, "Fail on a Go test that does slow work without being marked slow — execs go/forge/kcl/node/npm/git/docker/…, drives containers, or dials an external host, with no testkit.Slow(t, why) or testing.Short() guard on its path — so it runs in the blocking -short tier, and a file its child process reads is invisible to the test cache")
	cmd.Flags().BoolVar(&flags.strict, "strict", false, "Escalate advisory findings to errors so they fail the build / CI: RPCs missing a (forge.v1.method) auth-posture annotation, contract refusals in a tree with no forge.yaml, and an advisory lane's own could-not-run report (the typed-config guardrail when golangci-lint never reported). Not needed for a gating lane that could NOT run: that always fails, unless --allow-skip accepts it")
	// No backticks (see --optional-deps-guard): cobra would take the quoted
	// span as the argument placeholder.
	cmd.Flags().StringArrayVar(&flags.allowSkip, "allow-skip", nil, "Accept that a gating lane may NOT run here, by name and with a reason: LANE[,LANE...]=REASON (repeatable), e.g. --allow-skip 'frontend-typecheck=frontends typecheck in the web job'. A gating lane that could not run (a tool missing, a frontend with no node_modules) fails forge lint otherwise; an accepted one is listed as SKIPPED (allowed) in the verdict. forge.yaml lint.allow_skip is the project-level form. Accepts only could-not-run: a lane that ran and found problems still fails")
	cmd.Flags().BoolVar(&flags.tracing, "tracing", false, "Report where a request's trace is cut: a log call made with a context in scope but not passed it (no trace_id), an *http.Client or database pool opened without forge's traced constructors, a contract package constructed without its generated decorator. With --fix, rewrites every log call to its ...Context form and every undecorated contract construction to NewInstrumented; forge.yaml lint.tracing.log_wrappers adds the project's own logging helpers")
	cmd.Flags().BoolVar(&flags.skipFrontends, "skip-frontends", false, "Skip the whole frontend lane (eslint/stylelint + TypeScript typecheck + static-export) for a backend-only gate that needs no Node toolchain")
	cmd.Flags().BoolVar(&flags.fix, "fix", false, "Apply the mechanical fixes in place (Go formatting, the tracing log rewrite, frontend prettier, eslint --fix), each Go rewrite type-checked and taken back if it breaks its package. Never golangci-lint fixes. Off by default: lint is a gate and reports")
	cmd.Flags().BoolVar(&flags.check, "check", false, "Report only: write nothing, print the diff each fix would apply, and exit non-zero if any is pending. The default; beats --fix (also spelled --no-fix)")
	cmd.Flags().BoolVar(&flags.noFix, "no-fix", false, "Same as --check: write nothing, print the diff each fix would apply, and fail if any is pending")
	cmd.Flags().BoolVar(&flags.jsonOut, "json", false, "Output findings as JSON (see lint_json.go header for the schema; exit code matches text mode)")
	cmd.Flags().BoolVarP(&flags.quiet, "quiet", "q", false, "Print only what failed: a one-line summary per failing linter, its error findings, and the verdict as the LAST line (a clean run is one line). With --fix, fixes are applied silently; combines with the targeted flags and --scope")
	cmd.Flags().StringSliceVar(&flags.scope, "scope", nil, "Report only findings in files under `PATH` (repeatable or comma-separated, project-relative). Go linters run on PATH's packages only; file-anchored linters report only findings under PATH; whole-project linters (frontend lint, graph, component-drift, config-reach) cannot be scoped, are skipped, and are named in the verdict. With --fix, only files under PATH are rewritten")
	// No backticks: cobra would render the first quoted span as the flag's
	// argument placeholder (see --optional-deps-guard).
	cmd.Flags().BoolVar(&flags.showSuppressed, "show-suppressed", false, "List every suppression the project declares, with file:line, what it covers and the stated reason: forge.yaml lint.rules (off/warn) and contracts.exclude, //forge:exclude-contract markers, forge:lint-disable* and nolint directives, and the migration and optional-dep pragmas. Read-only: runs no linter and exits 0; combines with --json and --scope")
	cmd.Flags().StringVar(&flags.gateJSON, "gate-json", "", "Also write this run's result to `FILE` as a gate document, for `forge gate record` or `forge env deploy --gate`. A FILE, not a stdout mode: the findings and the exit code are unchanged.")

	// User-vs-maintainer surface split: the flags below are fully
	// functional but hidden from --help (visible via --help-dev). The
	// visible set is pinned by TestLintHelpSurface — a new flag must
	// consciously pick a side. See cmdutil.HideDevFlags for the mechanism.
	//
	// The rule of thumb: hide a flag when it is meaningless outside the
	// forge repo, or when it is a one-shot you run during setup/migration
	// and never again. A continuous rule about the USER's own code stays
	// visible however advisory it is — config-deps and optional-deps-guard
	// were both hidden as "audits" and were consequently undiscoverable,
	// despite config-deps finding 15 real issues on the flagship app with
	// paste-ready remediation and no false positives on a clean scaffold.
	hideDevFlags(cmd,
		"exported-vars",       // bundled into --contract; separate flag is for --json explicitness
		"scaffolds",           // forge ownership-boundary enforcement
		"banners",             // no-op outside the forge repo
		"suggest-excludes",    // one-shot migration/setup helper
		"suggest-buf-excepts", // one-shot migration/setup helper
		"check-workarounds",   // parallel-lane agent-workflow audit
	)
}

// lintRunOptions carries the whole-pipeline inputs from the flag layer to
// the --json sweep (collectAllLintersJSON). It exists so a new
// pipeline-wide switch is one field rather than another positional bool.
type lintRunOptions struct {
	// reportFormat adds the report-only format lane to a detect-only
	// collection (--json), so its findings and exit code match the text mode.
	reportFormat  bool
	strict        bool
	skipFrontends bool
	// allowSkip is the parsed --allow-skip + lint.allow_skip (lint_skip.go).
	allowSkip skipAllowances
	paths     []string
	cfg       *config.ProjectConfig
	// scope restricts findings to files under the --scope paths; nil is
	// the whole project (lint_structured.go).
	scope *lintScope
}

// loadLintConfig loads the project store and its config for the lint
// command. A missing forge.yaml is fine (returns a nil store and nil cfg);
// a parse/read error fails hard with the shared "failed to load project
// config" message so users don't silently lint with the wrong
// configuration. Callers that only need the config discard the store.
func loadLintConfig() (*projectstore.Store, *config.ProjectConfig, error) {
	store, err := loadProjectStore()
	if err != nil && !errors.Is(err, ErrProjectConfigNotFound) {
		return nil, nil, fmt.Errorf("failed to load project config: %w", err)
	}
	var cfg *config.ProjectConfig
	if store != nil {
		cfg = store.Config()
	}
	return store, cfg, nil
}

// contractExcludesFromConfig returns the contracts.exclude list from the
// project config, or nil when no config is loaded.
func contractExcludesFromConfig(cfg *config.ProjectConfig) []string {
	if cfg == nil {
		return nil
	}
	return cfg.Contracts.Exclude
}

// collectConventionFindings gathers every forge-convention finding
// without printing — the shared engine behind runConventionLint (text)
// and `forge lint --json`. Returns the combined findings, any
// informational skip notes ("No proto/ directory found — …"), and
// hasAny=false when none of the lintable trees (proto/, internal/,
// handlers/, workers/, operators/) exist at all.
func collectConventionFindings(opts forgeconv.LintOptions) (forgeconv.Result, []string, bool, error) {
	combined := forgeconv.Result{}
	var notes []string
	hasProto := false

	if _, err := os.Stat("proto"); err == nil {
		hasProto = true
		res, err := forgeconv.LintProtoTreeOpts("proto", opts)
		if err != nil {
			return combined, notes, false, fmt.Errorf("forge convention lint (proto) failed: %w", err)
		}
		// forgeconv reports proto paths relative to proto/ (its suppression
		// engine works in that frame), while every other finding here is
		// project-relative. Re-anchor them so the whole report is one frame:
		// a "services/x/v1/x.proto" path is not openable from the project
		// root, and `forge lint --scope proto/services/x` could not match it.
		for i := range res.Findings {
			if f := res.Findings[i].File; f != "" && !filepath.IsAbs(f) {
				res.Findings[i].File = filepath.ToSlash(filepath.Join("proto", f))
			}
		}
		combined.Findings = append(combined.Findings, res.Findings...)
	} else {
		notes = append(notes, "No proto/ directory found — skipping proto convention lint")
	}

	// Findings from the Go-tree rules below, whose File is relative to the
	// project root. They pass through the shared suppression engine once,
	// at the end, so every rule here honours `forge:lint-disable` the same
	// way (the proto rules already do, inside forgeconv, because their
	// paths are relative to proto/).
	var goFindings []forgeconv.Finding

	// Internal-package contract shape, plus the dep-interface rule and
	// the `// forge:outbound-io` convention `--type=adapter` stamps. All
	// three rules live in internal/contractcheck and ship through one
	// Inspect call so the engine controls ordering and de-dup; the
	// per-rule severity / gating discipline is preserved (contract-names
	// is an error; the other two are warnings). contract-names reports as
	// a warning where there is no forge.yaml (codegenUnavailable): its
	// error severity exists to protect a bootstrap that is never generated
	// there.
	//
	// Runs whether or not proto/ exists — CLI/library projects without
	// proto can still ship internal/ packages whose bootstrap codegen
	// would silently break on a wrong-named contract.
	hasInternal := false
	if _, err := os.Stat("internal"); err == nil {
		hasInternal = true
		store, cfgErr := loadProjectStore()
		if cfgErr != nil && !errors.Is(cfgErr, ErrProjectConfigNotFound) {
			return combined, notes, false, fmt.Errorf("failed to load project config for contract-shape lint: %w", cfgErr)
		}
		var cfg *config.ProjectConfig
		if store != nil {
			cfg = store.Config()
		}
		// The convention lint is not (yet) ctx-aware; the engine's
		// inter-rule cancellation hook is a forward-looking concern.
		// Using context.Background() preserves today's behavior; threading
		// the cobra cmd.Context() through is a separate cleanup.
		fs, err := contractcheck.Inspect(context.Background(), ".", contractcheck.Options{
			Excludes:           contractExcludesFromConfig(cfg),
			CodegenUnavailable: codegenUnavailable(cfg, opts.Strict),
		})
		if err != nil {
			return combined, notes, false, fmt.Errorf("forge convention lint (contract-shape) failed: %w", err)
		}
		goFindings = append(goFindings, fs...)
	}

	// Handler-tree analyzers — only run when handlers/ exists. The
	// no-handler-error-mapping rule catches per-service `toConnectError`
	// / `mapServiceError` re-rolls; canonical replacement is
	// `svcerr.Wrap(err)` from forge/pkg/svcerr. Warnings only — false-
	// positive risk is real (some projects have legitimate custom mapping
	// for project-specific sentinels), and the build should not gate on a
	// hand-rolled helper that hasn't been migrated yet.
	hasHandlers := false
	if _, err := os.Stat("internal/handlers"); err == nil {
		hasHandlers = true
		res, err := forgeconv.LintHandlerErrorMapping(".")
		if err != nil {
			return combined, notes, false, fmt.Errorf("forge convention lint (handler error mapping) failed: %w", err)
		}
		goFindings = append(goFindings, res.Findings...)

		// Handler-file size — warns when any handlers/<svc>/*.go grows
		// past lint.handler_file_max_loc (default 1000). The threshold
		// is project-configurable via forge.yaml. Warnings only — the
		// nudge points at the future `forge scaffold handler-file` split
		// subcommand rather than blocking on file size.
		store, cfgErr := loadProjectStore()
		if cfgErr != nil && !errors.Is(cfgErr, ErrProjectConfigNotFound) {
			return combined, notes, false, fmt.Errorf("failed to load project config for handler-file-size lint: %w", cfgErr)
		}
		threshold := config.DefaultHandlerFileMaxLOC
		if store != nil {
			threshold = store.Lint().EffectiveHandlerFileMaxLOC()
		}
		sizeRes, err := forgeconv.LintHandlerFileSize(".", threshold)
		if err != nil {
			return combined, notes, false, fmt.Errorf("forge convention lint (handler file size) failed: %w", err)
		}
		goFindings = append(goFindings, sizeRes.Findings...)
	}

	// Component-tree analyzers — also run on workers/ and operators/
	// (which have the same Deps-struct shape as handlers/). The
	// optional-dep-marker-position rule catches misplaced
	// `// forge:optional-dep` markers (typo'd onto a struct rather
	// than a field, or onto a non-Deps type) — the marker has no
	// effect when it's not on a Deps field, and silent failure is
	// exactly the kind of bug a lint rule earns its keep on.
	hasComponentTree := hasHandlers
	for _, sub := range []string{"internal/workers", "internal/operators"} {
		if _, err := os.Stat(sub); err == nil {
			hasComponentTree = true
		}
	}
	if hasComponentTree {
		res, err := forgeconv.LintOptionalDepMarkerPosition(".")
		if err != nil {
			return combined, notes, false, fmt.Errorf("forge convention lint (optional-dep marker position) failed: %w", err)
		}
		goFindings = append(goFindings, res.Findings...)
	}
	// Least knowledge (composition graph §2): what every component's Deps
	// declares against what the package uses through it — wide foreign
	// interfaces, containers, downcasts, environment reads, the root config.
	// The design's severities (§9.2); --strict makes them all errors but the
	// root config's, which it only reports.
	if _, err := os.Stat("internal"); err == nil {
		res, err := forgeconv.LintLeastKnowledge(".", opts)
		if err != nil {
			return combined, notes, false, fmt.Errorf("forge convention lint (least knowledge) failed: %w", err)
		}
		goFindings = append(goFindings, res.Findings...)
	}
	// SQL written for the wrong placeholder dialect: `$N` in a
	// bun-formatted query (NewRaw, the builders, db.Bun().ExecContext), or
	// a stray `?` bun will bind — in a comment, a string literal, or the
	// jsonb ?/?|/?& operators. Every Go file: hand SQL lives in
	// _repo_ext.go seams, workers and handlers alike.
	bunRes, err := forgeconv.LintBunPlaceholders(".")
	if err != nil {
		return combined, notes, false, fmt.Errorf("forge convention lint (bun placeholders) failed: %w", err)
	}
	goFindings = append(goFindings, bunRes.Findings...)
	// A SERIALIZABLE transaction that takes a lock (FOR UPDATE, an advisory
	// lock, LOCK TABLE): the waiter wakes into a stale snapshot and fails
	// 40001. Warning; an error under --strict.
	txLockRes, err := forgeconv.LintTxLockUnderSerializable(".", opts)
	if err != nil {
		return combined, notes, false, fmt.Errorf("forge convention lint (tx lock under serializable) failed: %w", err)
	}
	goFindings = append(goFindings, txLockRes.Findings...)

	applied := suppress.ApplyFiles("", goFindings, os.ReadFile)
	combined.Findings = append(combined.Findings, applied.Kept...)
	combined.Findings = append(combined.Findings, applied.Violations...)

	// Frontend forge-owned-dotenv hygiene — a committed .env.local / .env*
	// under a frontend that hard-codes a forge-owned variable
	// (*_MOCK_API / *_API_URL / *_OTEL_ENDPOINT / *_ENVIRONMENT) fights the
	// KCL-injected value. WARN here in `forge lint`; the SAME analyzer runs
	// at ERROR on the build/deploy path (deploy.go's gateFrontendEnvFiles),
	// so a forge-owned dotenv can never reach a shipped build.
	hasFrontends := false
	if feDirs := frontendDirsForLint(); len(feDirs) > 0 {
		hasFrontends = true
		res := forgeconv.LintFrontendEnvFiles(".", feDirs, forgeconv.SeverityWarning)
		combined.Findings = append(combined.Findings, res.Findings...)

		// Raw process.env / import.meta.env reads in frontend source —
		// the frontend mirror of the backend's os.Getenv guardrail. The
		// analyzer is silent on any frontend that has not adopted
		// proto-declared config (no src/lib/config_gen.ts), so this
		// cannot light up a project that has nowhere else to read from;
		// see internal/linter/forgeconv/frontend_process_env.go.
		//
		// WARN by default, for the same reason the backend's
		// enforce_typed_access defaults to warn: adoption is
		// incremental, and a frontend mid-migration must not go red the
		// day it declares its first config field. A project that has
		// finished migrating gates it with
		// `lint.rules: {forgeconv-frontend-process-env: error}` — the
		// existing repo-wide dial, not a second knob.
		//
		// The analyzer emits at that default and `lint.rules` re-grades
		// through the shared engine, rather than the severity being
		// resolved here, so `off` (drop the finding), `warn`, `error`
		// and the `*` wildcard all behave exactly as they do for every
		// other forge rule — no second implementation of the same
		// three-state dial to drift out of step.
		peRes := forgeconv.LintFrontendProcessEnv(".", feDirs, forgeconv.SeverityWarning)
		combined.Findings = append(combined.Findings, peRes.Findings...)
	}

	// If none of proto/, internal/, handlers/, workers/, operators/, or a
	// frontend dir exist, there's nothing to lint.
	hasAny := hasProto || hasInternal || hasComponentTree || hasFrontends
	return combined, notes, hasAny, nil
}

// frontendDirsForLint resolves the frontend source dirs the forge-owned
// dotenv rule scans, relative to the project root (cwd). It prefers the
// declared frontends (forge.yaml frontends[].path — honors custom /
// sibling paths), and falls back to a shallow scan of the conventional
// `frontends/` directory when no config is loadable. Returns nil when the
// project declares no frontend and has no frontends/ dir.
func frontendDirsForLint() []string {
	if store, err := loadProjectStore(); err == nil && store != nil {
		if cfg := store.Config(); cfg != nil && len(cfg.Frontends) > 0 {
			var dirs []string
			for _, fe := range cfg.Frontends {
				// A frontend with no directory in this repository (a
				// cross-repo source pin, a sibling-checkout path) has no
				// dotenv file here to lint.
				dir, ok := fe.Dir(".")
				if !ok {
					continue
				}
				dirs = append(dirs, dir)
			}
			return dirs
		}
	}
	entries, err := os.ReadDir("frontends")
	if err != nil {
		return nil
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join("frontends", e.Name()))
		}
	}
	return dirs
}

// errFeatureDisabled is what an EXPLICITLY REQUESTED lint returns when its
// feature is off.
//
// These arms used to print one line and return nil, which exits 0. A CI job
// whose entire purpose is `forge lint --migration-safety` then went green
// having checked nothing, and the green was indistinguishable from a clean
// tree — the same hazard migrationlint's Result.Skipped exists to prevent one
// layer down. It was reachable by accident: a partially-written `database:`
// block left the driver empty and derived migrations off (fixed at the loader
// in internal/config/derive_fill.go), so a user following forge's own
// escape-hatch advice silently disabled the check they were working around.
//
// An UNFLAGGED `forge lint` still skips the lane — the user asked for whatever
// applies. Naming the flag is what makes silence wrong: they asked for this
// lane specifically, and it did not run.
// featureTurnOn says what in the repo switches a lint lane's feature on. Features
// are derived from what exists, so the remedy is a change to the repo, never a
// forge.yaml key.
func featureTurnOn(feature string) string {
	switch feature {
	case "migrations":
		return "migrations turn on when db/migrations/ exists in a service project"
	case "contracts":
		return "contracts are on for every project kind; this config forced them off"
	}
	return feature + " derives from what exists in the repo"
}

// hasWorkspaceGoMod reports whether the go command will run in workspace
// mode: GOWORK names a file, or (GOWORK unset) the current working directory
// or any parent up to the filesystem root contains a go.work file. This
// signals that the project relies on Go workspace mode to wire local module
// checkouts (e.g. forge's own forge/ + forge/pkg/ pair), and we must not
// override GOWORK when running analyzer subprocesses.
//
// GOWORK is consulted first because it is how cmd/go decides too. A
// go.work kept OUTSIDE the tree (GOWORK=/tmp/x/go.work, the way to bridge a
// pinned project to an unreleased forge checkout without writing into it) is
// workspace mode, and adding -mod=mod to it makes every package load fail
// with "-mod may only be set to readonly or vendor when in workspace mode".
func hasWorkspaceGoMod() bool {
	switch gowork := os.Getenv("GOWORK"); gowork {
	case "off":
		return false
	case "":
	default:
		return true
	}
	dir, err := os.Getwd()
	if err != nil {
		return false
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// appendEnvIfUnset appends key=value to env only if key is not already set.
func appendEnvIfUnset(env []string, key, value string) []string {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return env
		}
	}
	return append(env, prefix+value)
}

// ensureEnvDefault sets key=defaultValue if the key is missing or set to an
// empty string. If the key already has a non-empty value it is left unchanged.
func ensureEnvDefault(env []string, key, defaultValue string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			// Key exists — only override when the value is empty.
			if e == prefix {
				env[i] = prefix + defaultValue
			}
			return env
		}
	}
	// Key not present at all — add it.
	return append(env, prefix+defaultValue)
}

// lintVerdict is what a run proved, in lanes — the input to the final line
// of every `forge lint` mode (text, --quiet, --scope), so the modes cannot
// disagree about how a verdict is worded.
type lintVerdict struct {
	// ran are the gating lanes that executed, failed ones included.
	ran []string
	// skipped are gating lanes that did not apply (feature off,
	// --skip-frontends, no Go package under --scope).
	skipped []string
	// unavailable are gating lanes that were supposed to execute and could
	// not. Each fails the run (lint_skip.go).
	unavailable []laneGap
	// advisoryUnavailable are advisory lanes that could not run: named,
	// never counted as passed, but not a failure — their findings could
	// not fail the run either.
	advisoryUnavailable []string
	// allowed are lanes that could not run and were accepted by name
	// (--allow-skip / lint.allow_skip): SKIPPED (allowed).
	allowed []laneGap
	// failed are the lanes that ran and fail the build.
	failed []string
	// unscoped are whole-project lanes a --scope run did not run.
	unscoped []string
	// scope is the --scope value, "" for a whole-project run.
	scope string
	// hiddenWarnings counts findings --quiet did not print.
	hiddenWarnings int
}

// render prints the success line, or returns the failure — which main
// prints as `Error: …`, the last line of the run either way.
//
// The rule: a verdict may only claim what the run proved. A failure names
// the lanes that failed, so the last line alone says what to fix. A gating
// lane that could NOT run is a failure too, named with its reason and the
// command that fixes it — its absence is the one thing a green last line
// would hide. Zero gating linters executed means the run proved nothing and
// must fail; a partial run must name the lanes it skipped, the skips it was
// told to accept, and the advisory lanes that could not run, rather than
// round up to "All linters passed!"; and a scoped run says it proved only
// its slice.
func (v lintVerdict) render(w io.Writer) error {
	if len(v.failed) > 0 || len(v.unavailable) > 0 {
		var what, fix []string
		if len(v.failed) > 0 {
			what = append(what, fmt.Sprintf("%d gating linter(s) failed: %s", len(v.failed), strings.Join(v.failed, ", ")))
			fix = append(fix, "address the findings above under each ❌ lane")
		}
		if len(v.unavailable) > 0 {
			what = append(what, fmt.Sprintf("%d lane(s) could NOT run: %s", len(v.unavailable), describeGaps(v.unavailable)))
			for _, g := range v.unavailable {
				fix = append(fix, g.lane+": "+g.fix)
			}
			fix = append(fix, acceptSkipHint(v.unavailable))
		}
		fix = append(fix, "re-run 'forge lint' to confirm")
		return cliutil.UserErr("forge lint", strings.Join(what, "; ")+v.suffix(), "", strings.Join(fix, "; "))
	}

	if len(v.ran) == 0 {
		var detail []string
		if len(v.advisoryUnavailable) > 0 {
			detail = append(detail, "could not run: "+strings.Join(v.advisoryUnavailable, ", "))
		}
		if len(v.allowed) > 0 {
			detail = append(detail, "SKIPPED (allowed): "+describeAllowed(v.allowed))
		}
		if len(v.skipped) > 0 {
			detail = append(detail, "skipped: "+strings.Join(v.skipped, ", "))
		}
		if len(detail) == 0 {
			detail = append(detail, "no gating linter was applicable here")
		}
		return cliutil.UserErr("forge lint",
			"no gating linter ran — this run proved nothing"+v.suffix(),
			strings.Join(detail, "; "),
			"install the missing tools ('forge tools install golangci-lint buf') and re-run from the project root")
	}

	// Everything the headline must not round up over: lanes that could not
	// run but do not fail the run (advisory, or accepted by name), then
	// lanes that did not apply.
	var extras []string
	if len(v.advisoryUnavailable) > 0 {
		extras = append(extras, fmt.Sprintf("%d could NOT run (%s) — advisory, so it does not fail the run",
			len(v.advisoryUnavailable), strings.Join(v.advisoryUnavailable, ", ")))
	}
	if len(v.allowed) > 0 {
		extras = append(extras, fmt.Sprintf("%d SKIPPED (allowed): %s", len(v.allowed), describeAllowed(v.allowed)))
	}
	if len(v.skipped) > 0 {
		extras = append(extras, fmt.Sprintf("%d skipped (%s)", len(v.skipped), strings.Join(v.skipped, ", ")))
	}

	// An advisory lane that could not run demotes the headline: it is a
	// hole in the coverage, even though it cannot fail the run.
	if len(v.advisoryUnavailable) > 0 {
		// The hint goes ABOVE the verdict so the verdict stays the last line.
		fmt.Fprintln(w, "   ↳ a lane that could not run is not a lane that passed — fix the cause above, or pass --strict to fail on it")
		fmt.Fprintf(w, "⚠️  %d gating linter(s) passed; %s%s\n", len(v.ran), strings.Join(extras, "; "), v.suffix())
		return nil
	}
	if len(extras) > 0 {
		fmt.Fprintf(w, "✅ %d gating linter(s) passed; %s%s\n", len(v.ran), strings.Join(extras, "; "), v.suffix())
		return nil
	}
	if v.scope != "" {
		fmt.Fprintf(w, "✅ %d gating linter(s) passed%s\n", len(v.ran), v.suffix())
		return nil
	}
	fmt.Fprintf(w, "✅ All %d gating linters passed%s.\n", len(v.ran), v.suffix())
	return nil
}

// suffix renders the scope and --quiet qualifiers every verdict line carries.
func (v lintVerdict) suffix() string {
	var b strings.Builder
	if v.scope != "" {
		fmt.Fprintf(&b, " — scope %s only", v.scope)
		if len(v.unscoped) > 0 {
			fmt.Fprintf(&b, "; not scoped, so not run: %s (whole-project — run an unscoped `forge lint` before merging)",
				strings.Join(v.unscoped, ", "))
		}
	}
	if v.hiddenWarnings > 0 {
		fmt.Fprintf(&b, "; %s not shown (--quiet)", plural(v.hiddenWarnings, "advisory finding"))
	}
	return b.String()
}

// golangciRunArgs is the ONE place a `golangci-lint run` argv is assembled,
// so every lane that shells golangci-lint takes golangci-lint's machine-global
// lock ($TMPDIR/golangci-lint.lock) the same way.
//
// Inside a heavy slot of its own (cmdutil.HoldsOwnHeavySlot; `forge lint`
// takes one) it passes --allow-parallel-runners, which drops the lock. The
// lock exists because two full golangci-lint runs at once can exhaust a small
// machine's memory, and the slot already bounds that: slots are sized at
// GiBPerSlot of memory each (pkg/hostslot). Keeping the lock as well made
// every golangci-lint on the machine run one at a time while each queued run
// held a slot it could not use — measured on a 16-CPU Mac, three scoped lints
// started together finished in 5s, 10s and 13s under the lock and in 4s each
// without it.
//
// Outside a slot (the limit is off, the slot was inherited from a parent that
// may run several children in it, or the limiter is broken) it passes
// --allow-serial-runners: keep the lock and drop only its deadline. Without
// either flag golangci-lint gives the lock five seconds and then exits 3 with
// "parallel golangci-lint is running", which on a shared box fails `forge
// lint` whenever any other agent, editor LSP or CI job is linting ANY project.
// The flag is passed even though the scaffolded .golangci.yml sets
// `allow-serial-runners: true`, because that config line reaches only
// projects scaffolded or upgraded after it landed. A project that sets
// allow-parallel-runners in its config keeps it: golangci-lint consults that
// before the serial flag.
func golangciRunArgs(extra []string, paths []string) []string {
	lock := "--allow-serial-runners"
	if cmdutil.HoldsOwnHeavySlot() {
		lock = "--allow-parallel-runners"
	}
	args := append([]string{"run", lock}, extra...)
	return append(args, paths...)
}

// golangciGateArgs is the gating golangci-lint invocation. Never --fix, not
// even under `forge lint --fix`: golangci-lint applies every enabled linter's
// suggested rewrite, and staticcheck's and gocritic's are code changes —
// SA9004's gave reliant's untyped const block a protoreflect.FullName type and
// the package stopped compiling. What gofmt/goimports would fix, forge's own
// format pre-pass already fixes; everything else stays a finding for the
// author. rc is taken so the mode is visibly not consulted.
func golangciGateArgs(rc *lintRunCtx) []string {
	return golangciRunArgs(nil, rc.goPackages())
}

// typedAccessGuardUnavailableHint is the remediation for a guardrail
// invocation that never reported.
//
// It no longer leads with golangci-lint's machine-global lock. forge passes
// --allow-serial-runners or, inside its own heavy slot, --allow-parallel-runners
// on every invocation (golangciRunArgs), so a contended lock never fails the
// run after five seconds. What is
// left is a golangci-lint that could not start at all — most often an old
// binary that predates the flag, or a config it cannot load — and the
// output printed just above this line names which.
const typedAccessGuardUnavailableHint = "read the golangci-lint output above — it exited before reporting. The usual " +
	"causes are a golangci-lint too old for this project's .golangci.yml (the version CI pins is in " +
	".github/workflows) or a config it cannot load. A lock held by another golangci-lint on this machine is " +
	"NOT the cause: forge runs it with --allow-serial-runners (or --allow-parallel-runners inside its own heavy slot), so a " +
	"contended lock waits its turn instead of failing"

// bufMissingReason completes "buf lint did NOT run: …".
const bufMissingReason = "buf is not on PATH, so the buf lint lane cannot run"

// bufMissingFix is the command that installs the buf forge pins.
func bufMissingFix() string {
	return fmt.Sprintf("run `forge tools install buf` (it installs buf %s), or `go install %s@v%s` with its GOBIN on PATH, then re-run `forge lint`",
		templates.BufVersion, bufModulePath, templates.BufVersion)
}

// bufModulePath is the package `go install` builds buf from.
const bufModulePath = "github.com/bufbuild/buf/cmd/buf"

type packageJSONScripts struct {
	Scripts map[string]string `json:"scripts"`
}

func readPackageScripts(pkgJSON string) (map[string]string, error) {
	data, err := os.ReadFile(pkgJSON)
	if err != nil {
		return nil, err
	}

	var pkg packageJSONScripts
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, err
	}
	return pkg.Scripts, nil
}

func hasPackageScript(scripts map[string]string, name string) bool {
	_, ok := scripts[name]
	return ok
}
