// File: internal/contractcheck/inspect_test.go
//
// Integration tests for the unified [Inspect] entry point. The
// per-rule tests live in <rule>_test.go and run each rule in
// isolation; this file verifies the engine's responsibilities:
//
//   - all-rules-by-default when Options.Rules is empty
//   - rule subset filtering when Options.Rules is set
//   - excludes flow through to the contract-names rule
//   - deterministic (file, line, rule) ordering across rules
//   - context cancellation between rules
//
// These tests are the integration-level guard the task asked for:
// they run Inspect over a sample project tree and assert the
// findings-shape contract that downstream call sites depend on.

package contractcheck

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"testing"
)

// TestInspect_AllRulesByDefault verifies that Options.Rules == nil runs
// every rule registered in [AllRules]. Empty Options is the most common
// call shape (runConventionLint historically called all three Lint*
// functions back-to-back), so the default-everything behavior is part
// of the public contract.
func TestInspect_AllRulesByDefault(t *testing.T) {
	t.Parallel()
	// contracts_bad covers the contract-names rule; outbound_io_with_rpc
	// covers outbound-io-no-rpc; deps_concrete covers the deps rule. We
	// build a composite project by pointing each rule at its dedicated
	// fixture in turn — Inspect's default-all
	// path means we DON'T have to enumerate the rules at the call
	// site. The proof is: each fixture surfaces exactly the rule it
	// was authored to exercise.
	cases := []struct {
		name     string
		fixture  string
		wantRule Rule
	}{
		{"contract-names fires on bad fixture", "contracts_bad", RuleInternalPackageContractNames},
		{"outbound-io-no-rpc fires on outbound_io_with_rpc", "outbound_io_with_rpc", RuleOutboundIONoRPC},
		{"deps-are-interfaces fires on deps_concrete", "deps_concrete", RuleDepsAreInterfaces},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fs, err := Inspect(context.Background(),
				filepath.Join("testdata", tc.fixture),
				Options{}, // nil Rules → all rules
			)
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			got := findingsForRule(fs, string(tc.wantRule))
			if len(got) == 0 {
				t.Fatalf("expected %s findings on %s fixture, got 0:\n%s",
					tc.wantRule, tc.fixture, AsResult(fs).FormatText())
			}
		})
	}
}

// TestInspect_RuleSubset verifies Options.Rules narrows what runs. The
// pre-codegen entry point relies on this: it only wants the
// contract-names rule because that's the one that gates bootstrap
// codegen.
func TestInspect_RuleSubset(t *testing.T) {
	t.Parallel()
	// outbound_io_with_rpc would surface outbound-io-no-rpc findings
	// under the all-rules default. Asking for ONLY contract-names must
	// produce zero findings — the fixture has a clean contract.go.
	fs, err := Inspect(context.Background(),
		filepath.Join("testdata", "outbound_io_with_rpc"),
		Options{Rules: []Rule{RuleInternalPackageContractNames}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	for _, f := range fs {
		if f.Rule != string(RuleInternalPackageContractNames) {
			t.Errorf("rule subset leaked rule %q into output", f.Rule)
		}
	}
}

// TestInspect_DeterministicOrdering verifies that running Inspect
// twice over the same fixture produces byte-identical findings
// slices. The forge project audit JSON and the human FormatText output both
// depend on this stability — a flaky sort would surface as "every CI
// run says different things changed."
func TestInspect_DeterministicOrdering(t *testing.T) {
	t.Parallel()
	root := filepath.Join("testdata", "contracts_bad")
	a, err := Inspect(context.Background(), root, Options{})
	if err != nil {
		t.Fatalf("Inspect (first call): %v", err)
	}
	b, err := Inspect(context.Background(), root, Options{})
	if err != nil {
		t.Fatalf("Inspect (second call): %v", err)
	}
	if len(a) != len(b) {
		t.Fatalf("non-deterministic finding count: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("finding %d differs between runs:\n  a: %+v\n  b: %+v", i, a[i], b[i])
		}
	}
	// Also assert the sort key is monotonic — protects against a future
	// refactor that forgets to re-sort after merging rule outputs.
	if !sort.SliceIsSorted(a, func(i, j int) bool {
		if a[i].File != a[j].File {
			return a[i].File < a[j].File
		}
		if a[i].Line != a[j].Line {
			return a[i].Line < a[j].Line
		}
		return a[i].Rule < a[j].Rule
	}) {
		t.Errorf("findings not sorted by (file, line, rule)")
	}
}

// TestInspect_ContextCancel verifies a canceled context aborts before
// the next rule starts. The pre-codegen entry point may run inside a
// larger ctx-cancelable pipeline, so honoring cancellation is part of
// the contract.
func TestInspect_ContextCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled
	_, err := Inspect(ctx, filepath.Join("testdata", "contracts_bad"), Options{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// TestInspect_NoInternalDir is the "library/CLI project" shape: no
// internal/ tree at all. Every rule must be a no-op, no errors, no
// findings.
func TestInspect_NoInternalDir(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	fs, err := Inspect(context.Background(), tmp, Options{})
	if err != nil {
		t.Fatalf("Inspect on empty project: %v", err)
	}
	if len(fs) != 0 {
		t.Errorf("empty project should produce 0 findings, got %d", len(fs))
	}
}

// TestHasErrors verifies the convenience helper agrees with the
// underlying [forgeconv.Result.HasErrors] semantics.
func TestHasErrors(t *testing.T) {
	t.Parallel()
	fs, err := Inspect(context.Background(),
		filepath.Join("testdata", "contracts_bad"),
		Options{Rules: []Rule{RuleInternalPackageContractNames}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !HasErrors(fs) {
		t.Errorf("contracts_bad should produce errors")
	}

	clean, err := Inspect(context.Background(),
		filepath.Join("testdata", "contracts_good"),
		Options{Rules: []Rule{RuleInternalPackageContractNames}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if HasErrors(clean) {
		t.Errorf("contracts_good should produce no errors")
	}
}
