// File: internal/cli/generate_validate_ownership_test.go
//
// The test-file typecheck gate must fail the run for forge's OWN output
// and only for that. A user-owned _test.go is the opposite case in every
// respect: forge will not rewrite it, the author can fix it, and failing
// generate over it blocks the one command that would refresh the code the
// file is failing against — a deadlock, not a gate.
//
// Regression for the Fixture Corpus break, whose terminal failure was a
// user-owned probe and a user-owned scaffold-once lifecycle test (neither
// written by that generate run) failing `forge generate` outright.

package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// stampedForgeOwned renders content carrying a REAL forge certification
// marker, the way every Tier-1 writer emits it. Hand-writing a marker line
// would not do: the value must be a genuine body hash or Verify reads it as
// a mention rather than a stamp.
func stampedForgeOwned(t *testing.T, relPath, body string) string {
	t.Helper()
	stamped, ok := checksums.Stamp(relPath, []byte(body))
	if !ok {
		t.Fatalf("%s is not stampable", relPath)
	}
	if checksums.Verify(stamped) != checksums.Pristine {
		t.Fatalf("stamped %s did not verify as forge-owned", relPath)
	}
	return string(stamped)
}

// A broken USER-owned test must not fail generation. It carries no forge
// marker, so it is the user's to fix — and blocking `forge generate` on it
// is what stops them fixing it.
func TestValidateTestFiles_UserOwnedBreakageDoesNotFailGenerate(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real typecheck over a temp module; full mode only")
	}
	dir := writeValidateFixture(t, map[string]string{
		// No forge:hash marker — a hand-written test, or forge's
		// scaffold-once lifecycle test after the user owns it.
		"lifecycle_test.go": `package validateapp

import "testing"

func TestLifecycle(t *testing.T) {
	_ = ListResponse{}.GetItems()
}
`,
	})

	buf, restore := captureStderr(t)
	err := runGoBuildValidate(dir)
	restore()

	if err != nil {
		t.Fatalf("a user-owned _test.go that does not compile must NOT fail generate — "+
			"forge does not write that file and the user cannot fix it without running generate; got: %v", err)
	}
	// Silence would be the other failure: the user still needs to know.
	out := buf.String()
	if !strings.Contains(out, "lifecycle_test.go") {
		t.Errorf("the user-owned breakage must still be REPORTED by name; stderr was:\n%s", out)
	}
	if !strings.Contains(out, "YOURS") {
		t.Errorf("the warning must say the file is the user's to fix; stderr was:\n%s", out)
	}
}

// The converse, and the behavior the gate exists for: forge's own
// generated test file failing to typecheck is a forge codegen bug and
// must still fail the run loudly.
func TestValidateTestFiles_ForgeOwnedBreakageStillFails(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real typecheck over a temp module; full mode only")
	}
	broken := `package validateapp

type Store interface{ WithTx() Store }

type stubStore struct{}

func (stubStore) WithTx() Store { return Store{} }
`
	dir := writeValidateFixture(t, map[string]string{
		"helpers_gen_test.go": stampedForgeOwned(t, "helpers_gen_test.go", broken),
	})

	_, restore := captureStderr(t)
	err := runGoBuildValidate(dir)
	restore()

	if err == nil {
		t.Fatal("a FORGE-OWNED _test.go that does not compile must fail validation — " +
			"that is a forge codegen bug the user cannot edit their way out of")
	}
	if !strings.Contains(err.Error(), "helpers_gen_test.go") &&
		!strings.Contains(errOutputOf(err), "helpers_gen_test.go") {
		t.Errorf("the failure must name the offending generated file; got: %v\n%s", err, errOutputOf(err))
	}
}

// Mixed tree: forge's own breakage decides the verdict even when a
// user-owned file is broken too, and both are reported.
func TestValidateTestFiles_MixedOwnershipFailsOnForgeOwned(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real typecheck over a temp module; full mode only")
	}
	broken := `package validateapp

func genHelper() int { return undefinedSymbol() }
`
	dir := writeValidateFixture(t, map[string]string{
		"helpers_gen_test.go": stampedForgeOwned(t, "helpers_gen_test.go", broken),
		"mine_test.go":        "package validateapp\n\nfunc mine() int { return alsoUndefined() }\n",
	})

	buf, restore := captureStderr(t)
	err := runGoBuildValidate(dir)
	restore()

	if err == nil {
		t.Fatal("forge-owned breakage must fail the run even alongside user-owned breakage")
	}
	out := buf.String() + errOutputOf(err)
	if !strings.Contains(out, "helpers_gen_test.go") {
		t.Errorf("forge-owned file must be named; got:\n%s", out)
	}
	if !strings.Contains(out, "mine_test.go") {
		t.Errorf("user-owned file must still be reported; got:\n%s", out)
	}
}
