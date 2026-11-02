// File: internal/cli/lint/lint_rollout_table.go
//
// The rollout declaration of every rule lint.rules grades — see
// lint_rollout.go for what the three fields mean and how the project's pin
// reads them.
//
// TestEveryGradableRuleDeclaresItsRollout fails for a rule in gradableRules
// with no row here, so a new lane cannot ship without choosing when it gates.
//
// ── Adding a rule ────────────────────────────────────────────────────────
//
// Add it to `newRules` below:
//
//	{IntroducedIn: "v1.0.0", GatesFrom: "v1.0.1", Rules: []string{"my-new-rule"}},
//
// IntroducedIn is the release the rule will ship in — the one NEXT_VERSION
// names, never a guess at VERSION's patch+1 —, GatesFrom the release after
// that (NEXT_VERSION's patch+1, which is what the release script writes into
// NEXT_VERSION when it cuts; TestRolloutRowsNameTheNextRelease). Tightening an existing rule — raising a
// warning to an error, or making it find what it used to miss — is the same
// act: move its ID out of `shippedRules` into `newRules` with the release that
// tightens it, and its findings warn for one release again.
//
// A rule the project must NOT be given a grace period for (it cannot be
// softened without making the gate meaningless) is still declared; set
// GatesFrom equal to IntroducedIn... which TestRolloutRowsAreWellFormed
// refuses, on purpose: ask the owner first.

package lint

// rolloutGroup is a set of rules that share a declaration.
type rolloutGroup struct {
	IntroducedIn string
	GatesFrom    string
	Fix          string
	Rules        []string
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
		"forgeconv-allocate-port-spacing",
		"forgeconv-computed-field-unwritten",
		"forgeconv-contract-names",
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
}

// newRules joined `forge lint`, or were tightened, after rolloutBaseline:
// they warn in a project pinned below GatesFrom.
var newRules = []rolloutGroup{
	// Tightened: a warning until v0.1.46, a gate since.
	{IntroducedIn: "v1.0.0", GatesFrom: "v1.0.1", Rules: []string{
		"forge-column-markers", // #1042: COMMENT ON TABLE/VIEW markers, and the lane now gates
	}},
	// Tightened: no longer silenced by an svcerr import (#1112).
	{IntroducedIn: "v1.0.0", GatesFrom: "v1.0.1", Rules: []string{
		"forgeconv-no-handler-error-mapping",
	}},

	// forge lint --closed-sets (#1082).
	{IntroducedIn: "v1.0.0", GatesFrom: "v1.0.1", Rules: []string{"forge-closed-set"}},
	// forge lint --db-grants (#1172).
	{IntroducedIn: "v1.0.0", GatesFrom: "v1.0.1", Rules: []string{
		"forge-db-grants-undecided", "forge-db-grants-unverified",
	}},
	// Commit policy: a generated file only tooling reads (the proto
	// descriptor) is never committed.
	{IntroducedIn: "v1.0.10", GatesFrom: "v1.0.11", Fix: "forge generate && git rm -r --cached gen/forge_descriptor", Rules: []string{"tool-derived-file-tracked"}},
	// forge lint --go-markers (#1039).
	{IntroducedIn: "v1.0.0", GatesFrom: "v1.0.1", Rules: []string{"forge-go-markers"}},
	// forge lint --graph (#968, #1017).
	{IntroducedIn: "v1.0.0", GatesFrom: "v1.0.1", Rules: []string{
		"forge-graph-assemble", "forge-graph-audience", "forge-graph-unreachable",
	}},
	// forge lint --slow-tests (#925).
	{IntroducedIn: "v1.0.0", GatesFrom: "v1.0.1", Rules: []string{
		"forge-slow-test-inputs", "forge-slow-test-unmarked",
	}},
	// forge lint --tracing (#993).
	{IntroducedIn: "v1.0.0", GatesFrom: "v1.0.1", Fix: "forge lint --tracing --fix", Rules: []string{
		"forge-tracing-log-without-ctx",
	}},
	{IntroducedIn: "v1.0.0", GatesFrom: "v1.0.1", Rules: []string{
		"forge-tracing-contract-uninstrumented", "forge-tracing-raw-http-client", "forge-tracing-raw-sql-open",
	}},
	// forge lint --secrets (#1131).
	{IntroducedIn: "v1.0.0", GatesFrom: "v1.0.1", Rules: []string{
		"openbao-wildcard-read",
		"secrets-cluster-kind", "secrets-env-not-rendered", "secrets-es-policy", "secrets-escrow-in-cluster",
		"secrets-immutable-pin", "secrets-key-coverage", "secrets-out-of-band-reason", "secrets-single-owner",
		"secrets-unreferenced-key",
	}},
	// Least-knowledge Deps rules (#968, #996, #1173) and the D3 / tx / error rules.
	{IntroducedIn: "v1.0.0", GatesFrom: "v1.0.1", Fix: "forge fix narrow-deps <package-dir>", Rules: []string{
		"forgeconv-deps-surface",
	}},
	{IntroducedIn: "v1.0.0", GatesFrom: "v1.0.1", Rules: []string{
		"forgeconv-activity-declared", // D3: every mutating rpc declares its activity (#936)
		"forgeconv-deps-no-downcast",
		"forgeconv-deps-root-config",
		"forgeconv-no-container-deps",
		"forgeconv-no-getenv",
		"forgeconv-tx-lock-under-serializable", // #1069
	}},
	{IntroducedIn: "v1.0.0", GatesFrom: "v1.0.1", Fix: "forge fix connect-errors", Rules: []string{
		"forgeconv-handler-connect-error", // #1112
	}},

	// The golangci-lint build-tag passes (#1205, #1224): findings in files
	// behind a project build tag, which `forge lint` used to not see at all.
	// The pass has no rule ID of its own; see lint_rollout.go.
	{IntroducedIn: "v1.0.0", GatesFrom: "v1.0.1", Rules: []string{rolloutGolangciBuildTags}},
	// The frontend lockfile lane: package.json ranges the lockfile does not record.
	{IntroducedIn: "v1.0.0", GatesFrom: "v1.0.1", Rules: []string{"forge-frontend-lock-drift"}},
	// A workload whose args select no command of the project binary (the
	// per-service workloads a graph-composed project was scaffolded with).
	{IntroducedIn: "v1.0.3", GatesFrom: "v1.0.4", Rules: []string{ruleComponentDriftNoCommand}},
}

// lintRollouts is the declaration of every rule, by ID.
var lintRollouts = buildRollouts(shippedRules, newRules)

func buildRollouts(groups ...[]rolloutGroup) map[string]ruleRollout {
	out := map[string]ruleRollout{}
	for _, list := range groups {
		for _, g := range list {
			for _, r := range g.Rules {
				out[r] = ruleRollout{IntroducedIn: g.IntroducedIn, GatesFrom: g.GatesFrom, Fix: g.Fix}
			}
		}
	}
	return out
}
