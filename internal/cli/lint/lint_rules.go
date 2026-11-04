// File: internal/cli/lint/lint_rules.go
//
// `lint.rules` — forge.yaml's rule → severity dial — applied ONCE, by the
// outcome engine, to every lane.
//
// ── Why one place ─────────────────────────────────────────────────────────
//
// The dial used to be applied lane by lane: the conventions, tracing, secrets
// and graph collectors each called EffectiveRules().ApplyAll on their own
// findings, and every other lane ignored it. So `lint.rules:
// {forge-config-deps: error}` was accepted by the config loader and did
// nothing — config-deps and optional-deps-guard were advisory by
// construction, and a project could not make them gate. The same silence
// covered every lane nobody had remembered to wire.
//
// gradeLane is now the only code that reads lint.rules for a verdict. The
// outcome engine (collectLintOutcomesFrom, collectLaneOutcome) calls it on
// every lane's findings before scoping and gap policy, and every output mode
// — text, --quiet, --scope, --json, --gate-json — renders those outcomes. A
// lane cannot opt out of it, and a new lane gets it for free.
//
// ── What it grades ────────────────────────────────────────────────────────
//
// Only forge's own rules: the IDs in gradableRules. A finding from another
// tool (golangci-lint's linters, buf, eslint), a lane's "could not run"
// headline (`*-unavailable`, governed by lint.allow_skip) and the engine's own
// bookkeeping (skipped, external, format) keep the severity their lane gave
// them — `lint.rules: {"*": warn}` must not turn a missing toolchain or a
// golangci error into a pass.
//
// ── A key it cannot honour is an error ─────────────────────────────────────
//
// lintRulesOutcome turns every lint.rules key that names no gradable rule
// into an error finding: a misspelling, a rule another dial owns
// (forge-frontend-typecheck is lint.frontend.typecheck), another tool's
// linter, or a bookkeeping rule. The config loader used to accept them and
// lint did nothing, so the project believed a rule was graded that was not.
// That a forge.yaml is shared across forge versions no longer argues for
// tolerance: `forge lint` runs under the project's pinned forge (a skewed
// binary hands over to it), so the key set is exactly the pinned forge's.

package lint

import (
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/commitpolicy"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/contractcheck"
	"github.com/reliant-labs/forge/internal/lint/secrets"
	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/internal/linter/migrationlint"
	"github.com/reliant-labs/forge/internal/linter/slowtest"
	"github.com/reliant-labs/forge/internal/linter/staticexport"
	"github.com/reliant-labs/forge/internal/linter/suppress"
	"github.com/reliant-labs/forge/internal/linter/tracinglint"
	"github.com/reliant-labs/forge/pkg/release"
)

// ruleLintRules is the rule of a lint.rules entry forge cannot honour.
const ruleLintRules = "forge-lint-rules"

// lintRulesLane names the lint.rules check in the verdict.
const lintRulesLane = "lint.rules"

// gradableRules returns every rule ID lint.rules grades: each forge-owned
// rule any lane emits, grouped by lane. TestEveryEmittedRuleIsGradable keeps
// it complete against the rule IDs the lane code declares.
func gradableRules() []string {
	rules := []string{
		// typed-config guardrail
		ruleTypedAccessGuard,
		// migration safety
		migrationlint.RuleCompatConflict, migrationlint.RuleCompatReason, migrationlint.RuleCompatUndeclared,
		migrationlint.RuleDuplicateVersion, migrationlint.RuleEnumValueUsedBeforeCommit,
		migrationlint.RuleInvalidAdoption, migrationlint.RuleNoDownMigration,
		migrationlint.RuleNonTimestampVersion, migrationlint.RuleNonTransactionalStatement,
		migrationlint.RuleVersionBelowMergedHead,
		"destructive-change", "set-not-null-without-backfill", "unsafe-add-not-null-column", "volatile-default",
		// forge conventions (proto, Go tree, frontend env)
		"forgeconv-activity-declared", "forgeconv-allocate-port-spacing",
		"forgeconv-bun-dollar-placeholder", "forgeconv-bun-literal-question-mark",
		"forgeconv-config-service-name", "forgeconv-contract-names",
		"forgeconv-deps-no-downcast", "forgeconv-deps-root-config", "forgeconv-deps-surface",
		"forgeconv-frontend-env-forge-owned", "forgeconv-frontend-process-env",
		"forgeconv-handler-connect-error", "forgeconv-handler-file-size",
		"forgeconv-list-filter-optional", "forgeconv-no-container-deps", "forgeconv-no-getenv",
		"forgeconv-no-handler-error-mapping", "forgeconv-one-service-per-file",
		"forgeconv-optional-dep-marker-position", "forgeconv-service-dir-consistency",
		"forgeconv-tx-lock-under-serializable", "forgeconv-unwrapped-domain-error",
		string(contractcheck.RuleInternalPackageContractNames), string(contractcheck.RuleDepsAreInterfaces),
		string(contractcheck.RuleOutboundIONoRPC), string(contractcheck.RuleInternalPackageMissingContract),
		// hook-test rows against their hooks (forge conventions lane)
		"forgeconv-frontend-hook-test-shape",
		// test conventions
		"forgeconv-handler-tests-use-tdd", "forgeconv-frontend-hook-tests",
		// frontend stores
		"forgeconv-frontend-stores-no-server-data",
		// scaffold ownership
		"scaffold-not-customized", "gen-missing-header", "gen-missing-source", "cross-tier-derived-symbol",
		// generated-file drift
		generatedDriftRule, generatedUnverifiedRule,
		// banners (forge repo only)
		"banner-tier1-missing-generated-header", "banner-tier2-missing-scaffold-header", "banner-unclassified",
		// Deps shape
		"forge-optional-deps-guard", "forge-config-deps",
		// composition graph
		codegen.GraphRuleUnreachable, codegen.GraphRuleAudience, codegen.GraphRuleAssemble,
		// component drift
		"component-drift/orphan-entry", "component-drift/undeclared-component", ruleComponentDriftNoCommand,
		// deploy
		"hosted-image-base/host-bearing-image", pdbRuleID,
		release.RuleJobTTL, release.RuleJobUnhashed, release.RuleForceData,
		"identity-share-with", "shellbuild-tokens/retired-token", "no-dotenv",
		// secrets
		secrets.RuleClusterKind, secrets.RuleESPolicy, secrets.RuleKeyCoverage, secrets.RuleUnreferenced,
		secrets.RuleImmutablePin, secrets.RuleOutOfBandReason, secrets.RuleWildcardRead, secrets.RuleEscrow,
		secrets.RuleSingleOwner, ruleSecretsNotRendered,
		// schema markers
		"forge-column-markers", "forge-closed-set", dbGrantsRule, dbGrantsUnverifiedRule,
		"forge-proto-markers", "forge-proto-options", ruleGoMarkers, "forge-vendored-proto-drift",
		// write policy
		ruleCreateRequestNullability, computedFieldRuleID, readOnlyFieldRuleID, "forgeconv-guarded-field-written",
		// fixtures, SQL
		fixtureDriftRule, fixtureDriftUnverifiedRule, timeBucketRuleID,
		// tracing
		tracinglint.RuleLogWithoutCtx, tracinglint.RuleRawHTTPClient, tracinglint.RuleRawSQLOpen,
		tracinglint.RuleContractUninstrumented,
		// commit policy
		commitpolicy.RuleGeneratedIgnored, commitpolicy.RuleLocalStateTracked, commitpolicy.RuleToolDerivedTracked,
		// frontend lockfile
		ruleFrontendLockDrift,
		// cross-lane workarounds
		"workaround-testing-extras", "workaround-cmd-not-in-binaries",
		// config reach
		"forge-config-unreachable",
		// slow tests
		slowtest.RuleUnmarked, slowtest.RuleInputs,
		// golangci-lint's build-tag passes (rolled out as one unit)
		rolloutGolangciBuildTags,
	}
	rules = append(rules, staticexport.RuleIDs()...)
	return rules
}

// ownedElsewhere are rule IDs a lane emits that lint.rules deliberately does
// not grade, each with the dial that does. Naming one in lint.rules is an
// error that points there.
var ownedElsewhere = map[string]string{
	ruleFrontendTypecheck:            "the TypeScript typecheck lane has its own dial: lint.frontend.typecheck (error | warn | off)",
	"contract":                       "contract findings are governed by contracts.exclude and //forge:exclude-contract",
	"buf":                            "buf's rules are configured in buf.yaml (lint.use / lint.except)",
	suppress.RuleMissingReason:       "a suppression must say why; add the reason to the directive instead",
	"external":                       "golangci-lint's and eslint's own linters are configured in .golangci.yml and the frontend's eslint config",
	"skipped":                        "a skip note is not a rule",
	ruleRanNote:                      "a lane's note of what it checked is not a rule",
	"feature-disabled":               "turn the feature on in forge.yaml, or drop the lane from the command",
	formatLaneName:                   "formatting is fixed by `forge lint --fix`; it cannot be dialled",
	ruleLintRules:                    "a lint.rules entry forge cannot honour is always an error",
	"format-unavailable":             "a lane that could not run is governed by lint.allow_skip (or --allow-skip)",
	ruleGolangciUnavailable:          "a lane that could not run is governed by lint.allow_skip (or --allow-skip)",
	ruleBufLintUnavailable:           "a lane that could not run is governed by lint.allow_skip (or --allow-skip)",
	ruleFrontendLintUnavailable:      "a lane that could not run is governed by lint.allow_skip (or --allow-skip)",
	ruleFrontendTypecheckUnavailable: "a lane that could not run is governed by lint.allow_skip (or --allow-skip)",
	ruleTypedAccessGuardUnavailable:  "a lane that could not run is governed by lint.allow_skip (or --allow-skip)",
}

// gradableRuleSet is gradableRules as a lookup.
func gradableRuleSet() map[string]bool {
	set := map[string]bool{}
	for _, r := range gradableRules() {
		set[r] = true
	}
	return set
}

// effectiveRules is the project's lint.rules, or nil without a config.
func effectiveRules(cfg *config.ProjectConfig) suppress.RuleSeverities {
	if cfg == nil {
		return nil
	}
	return cfg.Lint.EffectiveRules()
}

// gradeLane applies lint.rules to one lane's findings and re-derives whether
// the lane gates. It is the only place lint.rules decides a verdict.
//
// Only gradable rules are touched (see the file header). The verdict moves
// with the findings it was made of:
//
//   - a finding raised to error gates the lane, advisory or not — that is
//     what `forge-config-deps: error` asks for;
//   - a lane whose error findings were all lowered or dropped no longer gates
//     on them;
//   - a lane that gated for a reason no finding carries (it failed without
//     one) still gates.
func gradeLane(rules suppress.RuleSeverities, fs []lintJSONFinding, gated bool) ([]lintJSONFinding, bool) {
	if len(rules) == 0 || len(fs) == 0 {
		return fs, gated
	}
	gradable := gradableRuleSet()
	out := make([]lintJSONFinding, 0, len(fs))
	errorsBefore, raised := false, false
	for _, f := range fs {
		errorsBefore = errorsBefore || f.Severity == lintSevError
		if !gradable[f.Rule] {
			out = append(out, f)
			continue
		}
		adjusted, keep := rules.Override(finding.Finding{Rule: f.Rule, Severity: finding.Severity(f.Severity)})
		if !keep {
			continue
		}
		if adjusted.Severity == finding.SeverityError && f.Severity != lintSevError {
			raised = true
		}
		f.Severity = string(adjusted.Severity)
		out = append(out, f)
	}
	errorsAfter := anyErrorFinding(out)
	return out, raised || (gated && (errorsAfter || !errorsBefore))
}

// lintRulesOutcome is the lint.rules check as a lane outcome: one error
// finding per entry forge cannot honour — a severity it does not know, or a
// key that names no rule lint.rules grades. Unattributed (no file), so a
// scoped run reports it too: a broken dial changes the verdict of every
// slice. A config with nothing wrong yields an outcome that did not run, so
// the verdict does not count a check that found nothing to check.
func lintRulesOutcome(cfg *config.ProjectConfig) lintStepOutcome {
	o := lintStepOutcome{name: lintRulesLane, gates: true}
	if cfg == nil || len(cfg.Lint.Rules) == 0 {
		return o
	}
	for _, problem := range cfg.Lint.ValidateRules() {
		o.findings = append(o.findings, lintJSONFinding{
			Severity: lintSevError, Rule: ruleLintRules,
			Message: "forge.yaml " + problem,
		})
	}
	gradable := gradableRuleSet()
	vocabulary := make([]string, 0, len(gradable))
	for r := range gradable {
		vocabulary = append(vocabulary, r)
	}
	sort.Strings(vocabulary)
	keys := make([]string, 0, len(cfg.Lint.Rules))
	for k := range cfg.Lint.Rules {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		rule := strings.ToLower(strings.TrimSpace(key))
		if rule == suppress.Wildcard || gradable[rule] {
			continue
		}
		f := lintJSONFinding{Severity: lintSevError, Rule: ruleLintRules}
		switch why, ok := ownedElsewhere[rule]; {
		case ok:
			f.Message = fmt.Sprintf("forge.yaml lint.rules[%q]: lint.rules does not grade this rule, so the entry does nothing", key)
			f.FixHint = why + "; delete the entry"
		case isGapRule(rule):
			f.Message = fmt.Sprintf("forge.yaml lint.rules[%q]: a lane's could-not-run headline is not a rule lint.rules grades, so the entry does nothing", key)
			f.FixHint = "accept a lane that may not run with lint.allow_skip (or --allow-skip); delete the entry"
		default:
			f.Message = fmt.Sprintf("forge.yaml lint.rules[%q]: no forge lint rule has this ID, so the entry does nothing", key)
			f.FixHint = "lint.rules grades forge's own rules — the [rule] each finding prints. Another tool's linters " +
				"are configured in that tool's config (.golangci.yml, buf.yaml, eslint)"
			if near := codegen.ClosestFrom(rule, vocabulary); near != "" {
				f.FixHint = fmt.Sprintf("did you mean %q? ", near) + f.FixHint
			}
		}
		o.findings = append(o.findings, f)
	}
	o.ran = len(o.findings) > 0
	o.gated = o.ran
	return o
}
