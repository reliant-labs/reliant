package cli

// The "is my copy of the ledger stale?" tests are GONE, because the
// condition they tested can no longer arise.
//
// What they covered: a self-managed env recorded promotions in
// .forge/promotions/<env>.jsonl, COMMITTED TO GIT, so a checkout that had
// not pulled the latest ledger commit computed its release verdict against
// an older promotion — reporting drift for a deploy that went fine, or
// (worse) reporting MATCH when the new release had never deployed, which is
// the one failure that gate exists to catch. The suite had a case per state:
// behind, diverged, current, ahead, unbound-but-behind, and no-git.
//
// Why they are not ported: the staleness was REMOVED rather than detected.
// Neither of today's stores has a second copy that can fall behind — a
// control plane IS the ledger, and the machine ledger is one directory per
// PROJECT outside every checkout, shared by all of that project's worktrees
// whatever branch each one is on. So there is no "upstream copy" to compare
// against and no git ref that could hold a different history. A test
// asserting "behind is undetermined" would now have to manufacture a state
// the code cannot reach.
//
// What replaces them: TestMachineLedgerCannotGoStale below pins the
// invariant that makes the old suite unnecessary, which is the honest
// successor to a deleted guard. The ledgerFreshness vocabulary itself
// survives in ledger_freshness.go (its --json field and verify verdict live
// in files this change does not own) and is still covered by the round-trip
// test at the bottom.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// TestMachineLedgerCannotGoStale is why the freshness suite above was
// deleted rather than ported: there is no second copy of the ledger to be
// behind.
//
// Two "worktrees" of one project — different directories, as two branches
// would be — must resolve ONE ledger and therefore see each other's
// promotions immediately. Under the retired in-checkout backend each
// directory had its own .forge/promotions, which is exactly how a verify on
// a feature branch could read a stale "what does prod run".
func TestMachineLedgerCannotGoStale(t *testing.T) {
	home := t.TempDir()
	useLedgerHome(t, home)

	// The same project, checked out twice. Same name, no remote, so the
	// project id — and the ledger — is the same for both.
	primary, secondary := t.TempDir(), t.TempDir()
	writeProjectName(t, primary, "shared-project")
	writeProjectName(t, secondary, "shared-project")

	first := testPromote(t, primary, release.Promotion{
		Env:      "prod",
		Release:  "v1.0.0",
		Kind:     release.KindPromote,
		Resolved: map[string]string{"api": sha("a")},
	})

	// The OTHER worktree sees it, with no pull and no commit.
	cur, bound, err := testBindings(t, secondary).Current(context.Background(), "prod")
	if err != nil || !bound {
		t.Fatalf("a second worktree must read the same ledger: (bound=%v, %v)", bound, err)
	}
	if cur.ID != first.ID {
		t.Fatalf("two worktrees of one project must share one ledger: got promotion %q, want %q",
			cur.ID, first.ID)
	}

	// And neither worktree holds a ledger of its own, which is the
	// structural reason staleness is impossible rather than merely unlikely.
	for _, dir := range []string{primary, secondary} {
		if _, err := os.Stat(filepath.Join(dir, ".forge", "promotions")); !os.IsNotExist(err) {
			t.Errorf("%s holds an in-checkout ledger; the machine ledger must be the only copy (stat err: %v)", dir, err)
		}
	}
}

// TestNothingClaimsLedgerFreshness pins that the capability is unimplemented
// on purpose. If a store started claiming it again, env_status would resurrect
// a verdict whose premise — that this checkout holds a copy that can be
// behind — is no longer true, and would refuse to verify for a reason that
// cannot occur.
func TestNothingClaimsLedgerFreshness(t *testing.T) {
	dir := newLedgerTestProject(t, "freshness-project")

	var machine bindingStore = testBindings(t, dir)
	if _, ok := machine.(ledgerFreshnessChecker); ok {
		t.Error("the machine ledger lives outside every checkout, so it has no copy to be behind " +
			"and must not claim ledgerFreshnessChecker")
	}

	var hosted bindingStore = &hostedStore{}
	if _, ok := hosted.(ledgerFreshnessChecker); ok {
		t.Error("a control plane IS the ledger, so it must not claim ledgerFreshnessChecker")
	}
}

// The enum's JSON is a closed vocabulary: an unknown word is REFUSED rather
// than defaulted, because defaulting a newer binary's "this ledger is stale"
// to "fine" is the one mistake that would matter.
func TestLedgerFreshnessJSONRoundTripsAndRejectsUnknown(t *testing.T) {
	for _, s := range []ledgerFreshness{ledgerFreshnessUnknown, ledgerCurrent, ledgerBehind, ledgerAhead, ledgerDiverged} {
		raw, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("marshal %v: %v", s, err)
		}
		var back ledgerFreshness
		if err := json.Unmarshal(raw, &back); err != nil || back != s {
			t.Fatalf("round trip of %v gave (%v, %v)", s, back, err)
		}
	}
	var got ledgerFreshness
	err := json.Unmarshal([]byte(`"mostly-current"`), &got)
	if err == nil {
		t.Fatal("an unknown ledger state must be refused, never defaulted")
	}
	if !strings.Contains(err.Error(), "mostly-current") {
		t.Errorf("the error must name the word it did not understand, got %v", err)
	}
}
