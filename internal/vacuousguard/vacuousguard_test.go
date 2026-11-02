package vacuousguard

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/repowalk"
)

// ─────────────────────────────────────────────────────────────────────────────
// THE QUARANTINE
//
// One entry per finding that predates this guard. As in internal/deadcodeguard,
// an entry is NOT an allowance: it records a live defect, its cost, and its
// owner, and TestQuarantineIsTight deletes the entry's right to exist the
// moment the defect is fixed. The ledger can only shrink.
//
// Adding an entry to make a NEW finding green is the failure this package
// exists to prevent.
// ─────────────────────────────────────────────────────────────────────────────

type quarantined struct {
	Rule string
	Key  string
	Cost string
}

var quarantine = []quarantined{
	// ── Skips guarding files this repository tracks ─────────────────────
	// Each stats a path that is committed here, so it cannot fire on a
	// checkout — nothing falsifies it — and the day the layout moves it
	// deletes its test instead of failing.
	{
		Rule: RuleDeadSkip,
		Key:  "internal/cli/frontend_envvars_test.go::TestFrontendEnvVarsRoundTrip",
		Cost: "Skips when kcl/schema.k — tracked in this repo — is missing. Owner: internal/cli.",
	},

	{
		Rule: RuleDeadSkip,
		Key:  "internal/generator/frontend_webruntime_test.go::TestWebRuntimePublishedRangeTracksPackage",
		Cost: "Skips when web-runtime/package.json — tracked in this repo — is unreadable, so the " +
			"published-range assertion evaporates instead of failing. Owner: internal/generator.",
	},

	// ── Tests that run and decline to check ─────────────────────────────
	{
		Rule: RuleSilentCapability,
		Key:  "internal/cli/scaffold_frontend_runtime_e2e_test.go::TestE2EScaffoldFrontendRuntime",
		Cost: "`if !toolAvailable(\"node\") || !toolAvailable(\"npm\") { t.Log(...); return }` drops the " +
			"npm install + next build gate — the ONLY end-to-end check that the generated frontend " +
			"runtime compiles — and reports PASS. This is the lane whose absence cost a measured agent " +
			"43 minutes. Every sibling site was converted to requireTool in the same change; this file " +
			"was under concurrent edit and could not be touched. Fix: replace the bail with " +
			"requireTool(t, \"node\", \"npm\") and DELETE this entry. Owner: internal/cli.",
	},

	// ── Both halves of the reliant defect, in one test ──────────────────
	{
		Rule: RuleDeadSkip,
		Key:  "internal/generator/contract/fixedpoint_test.go::TestGenerate_MockOutputIsCanonicalFormatterFixedPoint",
		Cost: "Per-fixture skip when testdata/<fixture>/contract.go is missing: a fixture that loses " +
			"its contract.go stops being checked instead of failing. Owner: internal/generator.",
	},
	{
		Rule: RuleVacuousLoop,
		Key:  "internal/generator/contract/fixedpoint_test.go::TestGenerate_MockOutputIsCanonicalFormatterFixedPoint",
		Cost: "The same test ranges over os.ReadDir(\"testdata\") with no non-empty guard. Empty the " +
			"fixture corpus and the goimports fixed-point guard — the one that exists BECAUSE a " +
			"formatter drift incident cost a day — passes having checked nothing. This is the reliant " +
			"TestProjectWorkflows_* bug, both halves, in one function. Owner: internal/generator.",
	},
}

// ─────────────────────────────────────────────────────────────────────────────
// The guard
// ─────────────────────────────────────────────────────────────────────────────

// TestNoVacuousTests is the repository-wide guard.
func TestNoVacuousTests(t *testing.T) {
	if testing.Short() {
		t.Skip("parses every test file in the forge repository; runs in task test")
	}
	findings := scanRepo(t)

	q := map[string]bool{}
	for _, e := range quarantine {
		q[e.Rule+"|"+e.Key] = true
	}
	var fresh []Finding
	for _, f := range findings {
		if !q[f.Rule+"|"+f.Key] {
			fresh = append(fresh, f)
		}
	}
	if len(fresh) == 0 {
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d test(s) that can report green without testing anything.\n\n", len(fresh))
	for _, f := range fresh {
		fmt.Fprintf(&b, "  %s\n\n", f)
	}
	b.WriteString("  A test that declines to test is silent, and silence reads as success on every\n")
	b.WriteString("  dashboard forge has. Six tests in the sibling repo sat hard-red for weeks behind\n")
	b.WriteString("  exactly these two shapes.\n\n")
	b.WriteString("  Do NOT quarantine a new finding to make this green.\n")
	t.Error(b.String())
}

// TestQuarantineIsTight makes the ledger a ratchet: an entry that matches no
// finding must be deleted, so fixing a defect forces its removal.
func TestQuarantineIsTight(t *testing.T) {
	findings := scanRepo(t)
	live := map[string]bool{}
	for _, f := range findings {
		live[f.Rule+"|"+f.Key] = true
	}
	seen := map[string]bool{}
	for _, e := range quarantine {
		id := e.Rule + "|" + e.Key
		if seen[id] {
			t.Errorf("quarantine lists %s twice", id)
		}
		seen[id] = true
		if strings.TrimSpace(e.Cost) == "" {
			t.Errorf("quarantine entry %s has no Cost; an entry nobody can justify is an entry nobody will remove", id)
		}
		if !live[id] {
			t.Errorf("quarantine entry %s matches no finding.\n"+
				"  Either the test was fixed — delete this entry, that is the point of the ratchet —\n"+
				"  or a rule was narrowed until it stopped seeing it, which is a guard regression.", id)
		}
	}
}

var (
	repoOnce     sync.Once
	repoFindings []Finding
	repoErr      error
)

func scanRepo(t *testing.T) []Finding {
	t.Helper()
	repoOnce.Do(func() { repoFindings, repoErr = Scan(repoRoot(t)) })
	if repoErr != nil {
		t.Fatalf("scan forge: %v", repoErr)
	}
	return repoFindings
}

// TestScanReachesTheWholeRepo keeps a future skipDirs edit from quietly
// shrinking the surface. A guard that scans nothing passes everything.
func TestScanReachesTheWholeRepo(t *testing.T) {
	root := repoRoot(t)
	scanned := mustScanKeys(t, root)
	if len(scanned) < 200 {
		t.Fatalf("the walk reached only %d _test.go files under %s — either the repo root is wrong "+
			"or skipDirs grew until the guard stopped looking at forge", len(scanned), root)
	}
	// The scan must reach both halves of this repository: the CLI's
	// internal/ tree and the pkg/ runtime library.
	var sawInternal, sawPkg bool
	for _, f := range scanned {
		if strings.HasPrefix(f, "internal/") {
			sawInternal = true
		}
		if strings.HasPrefix(f, "pkg/") {
			sawPkg = true
		}
	}
	if !sawInternal {
		t.Error("the scan saw no test file under internal/")
	}
	if !sawPkg {
		t.Error("the scan saw no test file under pkg/ — it is a second module in this repo and is " +
			"walked by path, not by `go list`; a skipDirs edit that drops it would be invisible")
	}
}

// mustScanKeys returns every test file the walk reaches, independent of whether
// it produced a finding.
func mustScanKeys(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := repowalk.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && (skipDirs[d.Name()] || rel == "internal/templates") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(rel, "_test.go") {
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Rule-level proof: the planted defects in testdata/
// ─────────────────────────────────────────────────────────────────────────────

func TestRulesFireOnPlantedDefects(t *testing.T) {
	got := scanPlanted(t)

	var keys []string
	for _, f := range got {
		keys = append(keys, f.Rule+"|"+strings.TrimPrefix(f.Key, "planted_test.go::"))
	}
	sort.Strings(keys)

	want := []string{
		"dead-skip|TestPermanentlySkipped",
		"dead-skip|TestSkipOnRepoFixture",
		"silent-capability|TestCapabilityBailsMidTest",
		"silent-capability|TestCapabilityGuardedLint",
		"silent-capability|TestLookPathGuardedWork",
		"vacuous-loop|TestVacuousDiscovery",
		"vacuous-loop|TestVacuousGlob",
	}
	if strings.Join(keys, "\n") != strings.Join(want, "\n") {
		t.Errorf("planted-defect findings mismatch.\n got:\n  %s\nwant:\n  %s",
			strings.Join(keys, "\n  "), strings.Join(want, "\n  "))
	}
}

// TestLegitimateShapesAreNeverFlagged names the good constructs sitting beside
// the planted defects, one by one, so a future widening fails with the name of
// what it broke rather than as an opaque count mismatch.
func TestLegitimateShapesAreNeverFlagged(t *testing.T) {
	fired := map[string]bool{}
	for _, f := range scanPlanted(t) {
		fired[strings.TrimPrefix(f.Key, "planted_test.go::")] = true
	}
	mustNotFire := []struct{ name, why string }{
		{"TestGuardedByLen", "guards the discovery with len(entries) == 0 → t.Fatal"},
		{"TestGuardedByWitness", "proves it with a flag the loop sets, two loops deep"},
		{"TestGuardedByCounter", "proves it with a counter the loop bumps (checked++) and the test compares after it"},
		{"TestAssertsEmptiness", "ranges to prove the set is EMPTY; vacuity is the pass condition"},
		{"TestSkipOnMissingToolchain", "no checkout can supply kcl"},
		{"TestSkipOnShort", "testing.Short() is the caller's explicit choice"},
		{"TestSkipOnPlatform", "runtime.GOOS is a fact about the machine"},
		{"TestSkipOnSiblingCheckout", "the path comes from $HOME / an env var, so it is outside this repo"},
		{"TestSkipOnMissingHome", "a container may genuinely have no home directory"},
		{"TestProbeThenSkip", "the tool probe SKIPS, so the machine's gap is reported instead of swallowed"},
		{"TestProbeThenFail", "the tool probe FAILS, which is the CI-side answer to the same question"},
		{"TestProbeIsBookkeeping", "both outcomes verify equally (not at all); it builds a list before judging it"},
		{"TestProbeChoosesBetweenTwoRealPaths", "both branches verify the same claim, one per package manager"},
		{"TestNonProbeConditional", "asserts a bad substring is absent — the commonest shape in the repo"},
	}
	for _, m := range mustNotFire {
		if fired[m.name] {
			t.Errorf("%s was flagged, but it is legitimate (%s).\n"+
				"A rule was widened until it swallowed a good test. Narrow the rule; do not delete\n"+
				"the fixture.", m.name, m.why)
		}
	}
}

func scanPlanted(t *testing.T) []Finding {
	t.Helper()
	// Relative to the package directory `go test` runs in.
	src, err := os.ReadFile(filepath.Join("testdata", "planted_defects.go.txt"))
	if err != nil {
		t.Fatalf("read planted defects: %v", err)
	}
	// .go.txt, not .go: the file must stay invisible to the Go toolchain
	// (it does not compile — toolAvailable is a stub) while still being real
	// Go source the parser accepts.
	got, err := ScanSource("planted_test.go", src)
	if err != nil {
		t.Fatalf("scan planted defects: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("the planted-defect file produced no findings at all — the rules have stopped working")
	}
	return got
}

// TestCapabilityProbeSetIsComplete is what stops the silent-capability rule
// from going blind.
//
// The rule recognises a probe two ways: `exec.LookPath` written inline, or a
// call to a helper NAMED in capabilityProbeNames. The second half is a list,
// and a list is a thing that rots — rename toolAvailable, or add a second
// helper beside it, and the rule quietly stops seeing a whole file's worth of
// sites while still reporting green. That is the disease this package treats,
// so it is not allowed to have it.
//
// The check is structural, not by name: every function in the repository that
// consults exec.LookPath and answers a bool IS a capability probe, whatever it
// is called, and must be listed.
func TestCapabilityProbeSetIsComplete(t *testing.T) {
	if testing.Short() {
		t.Skip("parses every test file in the forge repository; runs in task test")
	}
	got, err := LookPathProbeFuncs(repoRoot(t))
	if err != nil {
		t.Fatalf("scan for probe helpers: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("found no exec.LookPath-to-bool helper anywhere in the repo — the walk is broken, " +
			"and a completeness check that inspects nothing certifies everything")
	}
	for _, name := range got {
		if capabilityProbeNames[name] {
			continue
		}
		t.Errorf("%s consults exec.LookPath and returns a bool — it is a capability probe — but it is "+
			"not in capabilityProbeNames (vacuousguard/capability.go).\n"+
			"  Until it is listed, every `if %s(...) { ...assertions... }` in this repo is invisible to\n"+
			"  the silent-capability rule. Add it.", name, name)
	}
}

// repoRoot walks up from the working directory, which `go test` sets to this
// package's source directory whatever directory it was invoked from.
//
// Not runtime.Caller: under -trimpath (the workspace image's GOFLAGS default)
// that reports "github.com/reliant-labs/forge/internal/...", a module-relative
// path that is not a file on disk. internal/trimpathguard holds the tree to it.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := wd
	for {
		b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && bytes.Contains(b, []byte("module github.com/reliant-labs/forge\n")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod declaring module github.com/reliant-labs/forge found above %s", wd)
		}
		dir = parent
	}
}
