// File: internal/cli/lint/lint_rollout_table.go
//
// The rollout declaration of every rule lint.rules grades — see
// lint_rollout.go for how the project's pin reads the declaration.
//
// TestEveryGradableRuleDeclaresItsRollout fails for a rule in gradableRules
// with no row here, so a new lane cannot ship without choosing when it gates.
//
// ── Adding a rule ────────────────────────────────────────────────────────
//
// For a new rule introduced in NEXT_VERSION v1.9.0 that needs a warning
// window, add an explicit, reasoned opt-in to `newRules`:
//
//	{IntroducedIn: "v1.9.0", Grace: &rolloutGrace{Until: "v1.10.0", Reason: "Give projects time to fix existing findings"}, Rules: []string{"my-new-rule"}},
//
// Until is exclusive: it becomes the effective GatesFrom used by lint and
// upgrade reports. This exception is for v1.9.0 introductions only; it does
// not reopen any window closed in #2410 or move with NEXT_VERSION.

package lint

import (
	"strings"

	"github.com/reliant-labs/forge/internal/linter/staticexport"
	"github.com/reliant-labs/forge/internal/linter/testwait"
	"github.com/reliant-labs/forge/pkg/dbanalyze"
)

// rolloutGroup is a set of rules that share a declaration.
type rolloutGroup struct {
	IntroducedIn string
	Grace        *rolloutGrace
	Fix          string
	Rules        []string
}

// rolloutGrace explicitly grants a warning window to newly introduced v1.9.0 rules.
// Until is the first release that gates; Reason records why the rule needs grace.
type rolloutGrace struct {
	Until  string
	Reason string
}

// shippedRules have gated since they shipped. The versions are the first
// release each was found in, from comparing the rule IDs in the tagged sources
// (v0.1.40 is the oldest release the table was audited against, so it reads
// "at or before").
var shippedRules = []rolloutGroup{
	{IntroducedIn: "v0.1.40", Rules: []string{
		"banner-tier1-missing-generated-header",
		"banner-tier2-missing-scaffold-header",
		"banner-unclassified",
		"component-drift/orphan-entry",
		"component-drift/undeclared-component",
		"cross-tier-derived-symbol",
		"destructive-change",
		"duplicate-migration-version",
		"forge-config-deps",
		"forge-config-unreachable",
		"forge-generated-file-edited",
		"forge-generated-file-unverified",
		"forge-optional-deps-guard",
		"forge-proto-markers",
		"forge-proto-options",
		"forge-time-bucket-session-tz",
		"forge-vendored-proto-drift",
		"forgeconv-computed-field-unwritten",
		"forgeconv-create-request-nullability",
		"forgeconv-deps-are-interfaces",
		"forgeconv-frontend-env-forge-owned",
		"forgeconv-frontend-hook-test-shape",
		"forgeconv-frontend-hook-tests",
		"forgeconv-frontend-process-env",
		"forgeconv-frontend-stores-no-server-data",
		"forgeconv-guarded-field-written",
		"forgeconv-handler-file-size",
		"forgeconv-handler-tests-use-tdd",
		"forgeconv-internal-package-contract-names",
		"forgeconv-internal-package-missing-contract",
		"forgeconv-one-service-per-file",
		"forgeconv-optional-dep-marker-position",
		"forgeconv-outbound-io-no-rpc",
		"forgeconv-read-only-field-unwritten",
		"forgeconv-service-dir-consistency",
		"forgeconv-unwrapped-domain-error",
		"gen-missing-header",
		"gen-missing-source",
		"generated-code-ignored",
		"machine-local-state-tracked",
		"no-dotenv",
		"no-down-migration",
		"non-timestamp-migration-version",
		"scaffold-not-customized",
		"set-not-null-without-backfill",
		"shellbuild-tokens/retired-token",
		"typed-config-guardrail",
		"unsafe-add-not-null-column",
		"volatile-default",
		"workaround-cmd-not-in-binaries",
		"workaround-testing-extras",
	}},
	{IntroducedIn: "v0.1.44", Rules: []string{
		"forge-fixture-rejected",
		"forge-fixture-unverified",
		"forgeconv-config-service-name",
		"forgeconv-list-filter-optional",
		"hosted-image-base/host-bearing-image",
		"migration-version-below-merged-head",
		"pdb-blocks-drain",
		// forge lint --static-export
		"static-export-config-routes",
		"static-export-dynamic-config",
		"static-export-dynamic-segment",
		"static-export-middleware",
		"static-export-next-headers",
		"static-export-next-image",
		"static-export-not-exported",
		"static-export-route-handler",
		"static-export-server-action",
		"static-export-trailing-slash",
	}},
	{IntroducedIn: "v0.1.45", Rules: []string{
		"enum-value-used-before-commit",
		"invalid-adopts-directive",
		"non-transactional-statement",
	}},
	{IntroducedIn: "v0.1.46", Rules: []string{
		"flux-force-on-data",
		"flux-job-ttl",
		"flux-job-unhashed",
		"forgeconv-bun-dollar-placeholder",
		"forgeconv-bun-literal-question-mark",
		"identity-share-with",
		"migration-compat-conflict",
		"migration-compat-reason",
		"migration-compat-undeclared",
	}},

	// The rules below joined `forge lint` (or were tightened) after warn-first
	// existed, and each warned for a grace window. #2410 closed those
	// windows, so these rows no longer soften findings at any pin.

	// Tightened: a warning until v0.1.46, a gate since.
	{IntroducedIn: "v1.0.0", Rules: []string{
		"forge-column-markers", // #1042: COMMENT ON TABLE/VIEW markers, and the lane now gates
	}},
	// Tightened: no longer silenced by an svcerr import (#1112).
	{IntroducedIn: "v1.0.0", Rules: []string{
		"forgeconv-no-handler-error-mapping",
	}},

	// forge lint --closed-sets (#1082).
	{IntroducedIn: "v1.0.0", Rules: []string{"forge-closed-set"}},
	// forge lint --db-grants (#1172).
	{IntroducedIn: "v1.0.0", Rules: []string{
		"forge-db-grants-undecided", "forge-db-grants-unverified",
	}},
	// Commit policy: a generated file only tooling reads (the proto
	// descriptor) is never committed.
	{IntroducedIn: "v1.0.10", Rules: []string{"tool-derived-file-tracked"}},
	// forge lint --go-markers (#1039).
	{IntroducedIn: "v1.0.0", Rules: []string{"forge-go-markers"}},
	// forge lint --graph (#968, #1017).
	{IntroducedIn: "v1.0.0", Rules: []string{
		"forge-graph-assemble", "forge-graph-audience", "forge-graph-unreachable",
	}},
	// forge lint --slow-tests (#925).
	{IntroducedIn: "v1.0.0", Rules: []string{
		"forge-slow-test-inputs", "forge-slow-test-unmarked",
	}},
	// forge lint --tracing (#993).
	{IntroducedIn: "v1.0.0", Rules: []string{
		"forge-tracing-log-without-ctx",
	}},
	{IntroducedIn: "v1.0.0", Rules: []string{
		"forge-tracing-contract-uninstrumented", "forge-tracing-raw-http-client", "forge-tracing-raw-sql-open",
	}},
	// forge lint --secrets (#1131).
	{IntroducedIn: "v1.0.0", Rules: []string{
		"openbao-wildcard-read",
		"secrets-cluster-kind", "secrets-env-not-rendered", "secrets-es-policy", "secrets-escrow-in-cluster",
		"secrets-immutable-pin", "secrets-key-coverage", "secrets-out-of-band-reason", "secrets-single-owner",
		"secrets-unreferenced-key",
	}},
	// Least-knowledge Deps rules (#968, #996, #1173) and the D3 / tx / error rules.
	{IntroducedIn: "v1.0.0", Rules: []string{
		"forgeconv-deps-surface",
	}},
	{IntroducedIn: "v1.0.0", Rules: []string{
		"forgeconv-activity-declared", // D3: every mutating rpc declares its activity (#936)
		"forgeconv-deps-no-downcast",
		"forgeconv-deps-root-config",
		"forgeconv-no-container-deps",
		"forgeconv-no-getenv",
		"forgeconv-tx-lock-under-serializable", // #1069
	}},
	{IntroducedIn: "v1.0.0", Rules: []string{
		"forgeconv-handler-connect-error", // #1112
	}},

	// The golangci-lint build-tag passes (#1205, #1224): findings in files
	// behind a project build tag, which `forge lint` used to not see at all.
	// The pass has no rule ID of its own; see lint_rollout.go.
	{IntroducedIn: "v1.0.0", Rules: []string{rolloutGolangciBuildTags}},
	// The frontend lockfile lane: package.json ranges the lockfile does not record.
	{IntroducedIn: "v1.0.0", Rules: []string{"forge-frontend-lock-drift"}},
	// A workload whose args select no command of the project binary (the
	// per-service workloads a graph-composed project was scaffolded with).
	{IntroducedIn: "v1.0.3", Rules: []string{ruleComponentDriftNoCommand}},
	// A login broker that mounts public sign-up in every environment. The
	// lane is advisory (its findings are warnings at any pin); the row is
	// what lets lint.rules raise it.
	{IntroducedIn: "v1.0.16", Rules: []string{ruleOpenSignup}},
	// forgeconv-allocate-port-spacing: graded since v0.1.40 but no lane ran
	// it until the conventions lane did (bug 13245924), so it warns first.
	{IntroducedIn: "v1.1.0", Rules: []string{"forgeconv-allocate-port-spacing"}},
	// Commit policy: the birth ledger and the other committed .forge/ state
	// must not be gitignored (a `.forge/` line voids the scaffold's
	// re-includes, and deleted scaffolds come back in every fresh clone).
	{IntroducedIn: "v1.1.0", Rules: []string{"forge-state-ignored"}},
	// An owner-scoping wrapper whose owner source disagrees with its
	// siblings' (growth: appended Lists scoped tenant_id by claims.UserID).
	{IntroducedIn: "v1.1.0", Rules: []string{"forgeconv-crud-owner-resolver"}},
	// Identity v2: a declared sign-in method or sign-up mode the login
	// broker does not serve, which would be converged and silently ignored.
	{IntroducedIn: "v1.1.0", Rules: []string{ruleIdentityMethods}},
	// A forge:identity-subject column the read-only role cannot read
	// (forge:secret, or a credential-shaped name with no forge:readable): the
	// console never sees it, so the identity link is never drawn.
	{IntroducedIn: "v1.2.0", Rules: []string{ruleIdentitySubjectUnreadable}},
	// A deployed sign-up whose address nobody proves (email delivery "none",
	// or auth_self_signup outside development): its email is a contact
	// field no proof ever claims (forge#2121).
	{IntroducedIn: "v1.8.0", Rules: []string{ruleUnprovenSignup}},
	// Defaulting to true enables unproven sign-up in every env that omits it.
	{IntroducedIn: "v1.9.0", Rules: []string{ruleUnprovenSignupDefault}},
	// forge lint --test-waits: a test that waits on the wall clock where it
	// should wait on an event — the shapes that flake on a loaded machine.
	{IntroducedIn: "v1.5.0", Rules: []string{
		testwait.RuleFixedSleep, testwait.RuleTightDeadline, testwait.RuleWallClockBound,
	}},
	// A product event property whose key names personal data (email, name,
	// ip, url, path, token). Advisory; --strict raises it.
	{IntroducedIn: "v1.5.0", Rules: []string{"forgeconv-product-event-pii"}},
	// forge db analyze: what a migration measurably does on a real
	// postgres — rewrites, ACCESS EXCLUSIVE across a scan, validation scans
	// under write-blocking locks, blocking index builds, timeouts.
	{IntroducedIn: "v1.7.0", Rules: dbanalyze.MigrationRuleIDs()},
	// forge lint --proto-format: a .proto `buf format` would rewrite — the
	// scaffolded pre-commit hook's own check, which lint used to pass while
	// the hook refused the commit.
	{IntroducedIn: "v1.8.0", Rules: []string{ruleProtoFormat}},
	// A handler file implementing more than one custom rpc. Its findings
	// stay advisory at any pin; the row lets lint.rules raise them.
	{IntroducedIn: "v1.9.0", Rules: []string{"forgeconv-one-rpc-per-file"}},
	// forge lint --migration-safety: a plain CREATE INDEX on a table that
	// may hold rows (NS FG-35), the static twin of db analyze's
	// db-analyze-blocking-index-build. A warning at any pin (it cannot see
	// the table's size); the row lets lint.rules raise it.
	{IntroducedIn: "v1.9.0", Rules: []string{"blocking-index-build"}},
	// A reason-required Go marker (forge:auth-unscoped-ok, …) written with
	// no reason, which its recognizer silently ignored (FG-32). Advisory at
	// any pin; the row lets lint.rules raise it.
	{IntroducedIn: "v1.9.0", Rules: []string{ruleGoMarkersReasonMissing}},
}

// newRules holds new v1.9.0 rules with explicit, owner-approved grace.
var newRules = []rolloutGroup{
	// forge lint --telemetry: declared telemetry (forge.Telemetry) judged for
	// what it collects. Only the uncollected dashboard series is an error;
	// the other three are advisory at any pin, and the row is what lets
	// lint.rules raise them.
	{IntroducedIn: "v1.9.0", Grace: &rolloutGrace{Until: "v1.10.0", Reason: "A project that already declares telemetry may have dashboards naming series nothing collects; warn for a release before telemetry-dashboard-metric-uncollected gates"}, Rules: []string{
		ruleTelemetryScrapeUnbounded, ruleTelemetryMetricUncollected, ruleTelemetryRateOnGauge, ruleTelemetryHighCardinality,
	}},
	{
		IntroducedIn: "v1.9.0",
		Grace:        &rolloutGrace{Until: "v1.10.0", Reason: "CI that relied on implicit installs needs time to add an explicit install step"},
		Fix:          "forge lint --install",
		Rules:        []string{ruleFrontendNoInstall},
	},
	{
		IntroducedIn: "v1.9.0",
		Grace:        &rolloutGrace{Until: "v1.10.0", Reason: "Give existing Vite sites time to align build output and base paths with their deployment"},
		Rules:        []string{staticexport.RuleViteOutDir, staticexport.RuleViteBase},
	},
	// The vulnerability gate made real (rollout_grade.go): an env's declared
	// `vuln_gate = enforce` is judged as audit until it gates — at the cut,
	// at a self-hosted promote and in the vuln-sbom gate step — and a
	// declared `provenance_sbom = enforce` refuses a promotion whose
	// artifacts have no SBOM. Both are the approved SBOM design's warn-first
	// rollout (forge#2468 §1.8).
	{
		IntroducedIn: "v1.9.0",
		Grace:        &rolloutGrace{Until: "v1.10.0", Reason: "An enforce declared before forge evaluated it blocked nothing; it warns \"would block\" for one release before it refuses a promotion"},
		Rules:        []string{RuleVulnGate, RuleSBOMRequired},
	},
	{
		IntroducedIn: "v1.9.0",
		Grace:        &rolloutGrace{Until: "v1.10.0", Reason: "A Next.js frontend that turns on cacheComponents (create-next-app's default) needs time to drop it before the lane gates"},
		Rules:        []string{staticexport.RulePartialPrerender},
	},
	{
		IntroducedIn: "v1.9.0",
		Grace:        &rolloutGrace{Until: "v1.10.0", Reason: "Give projects time to review and index newly diagnosed query shapes"},
		Rules:        dbanalyze.QueryRuleIDs(),
	},
}

// rolloutLinkFixture is a grace row linked into a test build of the forge
// binary, "<rule>@<introduced_in>@<grace_until>", so the warn-first path is
// proven through the real binary on a rule whose window the test controls
// (TestE2ENewRuleWarnsUntilPinReachesGatesFrom). Only the linker sets it
// (-ldflags -X); no flag, env var or file reaches it, and it is empty in
// every release build.
var rolloutLinkFixture string

// lintRollouts is the declaration of every rule, by ID.
var lintRollouts = buildRollouts(shippedRules, newRules, linkedFixtureRows(rolloutLinkFixture))

// linkedFixtureRows is the grace row rolloutLinkFixture names, if any.
func linkedFixtureRows(fixture string) []rolloutGroup {
	parts := strings.Split(fixture, "@")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil
	}
	return []rolloutGroup{{
		IntroducedIn: parts[1],
		Grace:        &rolloutGrace{Until: parts[2], Reason: "linked e2e fixture"},
		Rules:        []string{parts[0]},
	}}
}

func buildRollouts(groups ...[]rolloutGroup) map[string]ruleRollout {
	out := map[string]ruleRollout{}
	for _, list := range groups {
		for _, g := range list {
			var gatesFrom string
			if g.Grace != nil {
				gatesFrom = g.Grace.Until
			}
			for _, r := range g.Rules {
				out[r] = ruleRollout{IntroducedIn: g.IntroducedIn, GatesFrom: gatesFrom, Fix: g.Fix}
			}
		}
	}
	return out
}
