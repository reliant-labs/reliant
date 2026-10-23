package cli

// Test helpers for the MACHINE ledger.
//
// The ledger is no longer a path under the project directory, so a test can
// no longer construct a store from a t.TempDir() alone: it needs a project
// NAME (the ledger is keyed by project, not by directory) and a ledger HOME
// that is not the developer's real ~/.forge/ledger.
//
// Both are supplied here without t.Setenv, by pointing the ledgerHome seam at
// a temp dir. That keeps these helpers usable from parallel tests and, more
// importantly, guarantees a test run never reads or writes the machine's
// actual ledger.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/pkg/release"
)

// newLedgerTestProject prepares a project directory whose machine ledger is
// a fresh temp dir, and returns the project directory.
//
// It writes a forge.yaml name because the ledger refuses to key an unnamed
// project — a deliberate refusal (falling back to the directory name would
// re-create the per-worktree split the machine ledger exists to remove), so
// every test that touches a ledger states a name.
func newLedgerTestProject(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	useLedgerHome(t, t.TempDir())
	ledgerHomeIsRedirected[t] = true
	t.Cleanup(func() { delete(ledgerHomeIsRedirected, t) })
	writeProjectName(t, dir, name)
	return dir
}

// useLedgerHome points the ledger home at home for the duration of the test.
//
// It mutates a package var, so a test that calls it (directly or through
// testStore) must not be parallel. That is the same constraint t.Setenv
// imposes, without t.Setenv's process-wide reach — and unlike an env var it
// cannot leak into a subprocess that then writes the real ledger.
func useLedgerHome(t *testing.T, home string) {
	t.Helper()
	prev := ledgerHome
	ledgerHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { ledgerHome = prev })
}

// writeProjectName makes dir resolve to a named project, WITHOUT writing a
// forge.yaml into it.
//
// It overrides the ledgerProjectName seam rather than creating the file,
// because creating one would change what the project is: a new untracked
// file makes a git tree dirty, and a dirty tree silently disables the
// build-staleness guard that several tests in this package assert on. A test
// helper must not alter the subject it is setting up.
//
// A directory that DOES have a real forge.yaml keeps its own name, so a test
// that wrote one still gets what it declared.
func writeProjectName(t *testing.T, dir, name string) {
	t.Helper()
	prev := ledgerProjectName
	ledgerProjectName = func(projectDir string) string {
		if got := prev(projectDir); got != "" {
			return got
		}
		return name
	}
	t.Cleanup(func() { ledgerProjectName = prev })
}

// useTestLedger makes dir resolve to a named project on a private ledger
// home, for a test that drives a COMMAND (runBuild, runPromote,
// cutReleaseFromBuildState) rather than calling a helper.
//
// Those tests need the seams live before the command runs, because the
// command opens the ledger itself; the helpers cannot do it for them. Call
// it right after creating the project directory.
func useTestLedger(t *testing.T, dir string) string {
	t.Helper()
	ensureLedgerReady(t, dir)
	return dir
}

// testStore opens dir's machine ledger, failing the test if it cannot.
//
// It makes dir ledger-ready first — a forge.yaml name, and a ledger home
// under a temp dir — so an existing test that starts from a bare
// t.TempDir() keeps working without naming either. Both are REQUIRED in
// production (the ledger is keyed by project, and refuses an unnamed one),
// but neither is what any of these tests is about.
//
// The home redirection is the part that must never be skipped: without it a
// test run would read and write the developer's real ~/.forge/ledger.
func testStore(t *testing.T, dir string) *ledgerfile.Store {
	t.Helper()
	ensureLedgerReady(t, dir)
	store, err := openMachineLedger(dir)
	if err != nil {
		t.Fatalf("open the machine ledger for %s: %v", dir, err)
	}
	return store
}

// ledgerHomeIsRedirected tracks whether this test already pointed the home
// at a temp dir, so ensureLedgerReady does not reset it mid-test (which
// would hand a test two different ledgers and make a promote vanish).
var ledgerHomeIsRedirected = map[*testing.T]bool{}

// ensureLedgerReady gives dir a project name and this test a private ledger
// home, both idempotently.
func ensureLedgerReady(t *testing.T, dir string) {
	t.Helper()
	if !ledgerHomeIsRedirected[t] {
		useLedgerHome(t, t.TempDir())
		ledgerHomeIsRedirected[t] = true
		t.Cleanup(func() { delete(ledgerHomeIsRedirected, t) })
	}
	writeProjectName(t, dir, "test-project")
}

// testBindings is the machine ledger's bindingStore for dir — the
// replacement for the retired testBindings(t, dir).
func testBindings(t *testing.T, dir string) machineBindingStore {
	t.Helper()
	return machineBindingStore{store: testStore(t, dir)}
}

// testReleases is the machine ledger's releaseLedger for dir.
func testReleases(t *testing.T, dir string) machineReleaseLedger {
	t.Helper()
	return machineReleaseLedger{store: testStore(t, dir)}
}

// testLedger is both halves, the replacement for the retired
// testLedger(t, dir).
func testLedger(t *testing.T, dir string) envLedger {
	t.Helper()
	ensureLedgerReady(t, dir)
	l, err := machineLedger(dir)
	if err != nil {
		t.Fatalf("machine ledger for %s: %v", dir, err)
	}
	return l
}

// testCutRelease records a release in dir's machine ledger — the replacement
// for the retired WriteRelease(dir, r). It returns the error so the tests
// that assert on a refused cut still can.
func testCutRelease(t *testing.T, dir string, r release.Release) error {
	t.Helper()
	_, err := testStore(t, dir).CutRelease(r)
	return err
}

// testGetRelease reads a release back — the replacement for the retired
// ReadRelease(dir, version).
func testGetRelease(t *testing.T, dir, version string) (*release.Release, error) {
	t.Helper()
	return testStore(t, dir).Release(version)
}

// storeHistory is an env's promotions OLDEST FIRST — the replacement for the
// retired fileBindingStore.History(env), which was a method the seam never
// declared. Expressed through HistoryPage (which IS a declared capability)
// and reversed, so the helper needs no privileged access to the store.
func storeHistory(t *testing.T, store bindingStore, env string) ([]release.Promotion, error) {
	t.Helper()
	reader, ok := store.(bindingHistoryReader)
	if !ok {
		t.Fatalf("store %T cannot read history", store)
	}
	page, err := reader.HistoryPage(context.Background(), env, historyQuery{Limit: maxHistoryLimit})
	if err != nil {
		return nil, err
	}
	oldestFirst := make([]release.Promotion, len(page.Promotions))
	for i, p := range page.Promotions {
		oldestFirst[len(page.Promotions)-1-i] = p
	}
	return oldestFirst, nil
}

// testPromotionLogPath is where a project's promotions for env are written —
// the replacement for the retired promotionLogPath(projectDir, env), which
// pointed inside the checkout.
func testPromotionLogPath(t *testing.T, dir, env string) string {
	t.Helper()
	return filepath.Join(testStore(t, dir).Dir(), "promotions", env+".jsonl")
}

// testPromote appends a promotion with no CAS — the common test setup.
func testPromote(t *testing.T, dir string, p release.Promotion) release.Promotion {
	t.Helper()
	got, err := testBindings(t, dir).Append(context.Background(), p, appendGuard{})
	if err != nil {
		t.Fatalf("promote %s → %s: %v", p.Env, p.Release, err)
	}
	return got
}
