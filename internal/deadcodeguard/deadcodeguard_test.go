package deadcodeguard

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// THE QUARANTINE
//
// One entry per finding that is real, unfixed, and owned by a surface outside
// this package. A quarantine entry is NOT an allowance: it does not say "this
// is fine", it says "this is a live defect, here is what it costs, and here is
// who must fix it". Two properties keep it from becoming a dumping ground:
//
//   - TestQuarantineIsTight fails when an entry no longer matches a finding.
//     Fixing a defect therefore FORCES the entry's deletion; the ledger can
//     only shrink.
//   - Every entry carries a Cost — the concrete thing that is wrong today.
//     "Known issue" is not a cost. If the cost cannot be stated, the entry is
//     not understood well enough to quarantine.
//
// Adding an entry to make a NEW finding green is the failure mode this whole
// package exists to prevent. Fix the code.
// ─────────────────────────────────────────────────────────────────────────────

type quarantined struct {
	Rule string
	Key  string
	Cost string
}

var quarantine = []quarantined{
	// NOTE: the seven internal/bundle.BuildInput entries were REMOVED, not fixed
	// away by accident. F2 shipped bundle.Build ahead of its caller by scope;
	// F6a wired it in, so `forge env build` and the no-version `forge env
	// deploy` now write a bundle on every build and every field has a
	// production writer. See internal/cli/env_build_bundle.go.

	// NOTE: the four per-service-port entries (config.ComponentConfig.Ports and
	// PortSpec.Port/.Protocol/.Expose) were REMOVED, not fixed away by accident:
	// the field, the PrimaryPort() accessor that always returned 0, the
	// generated `ports` key nothing populated, and every consumer that
	// read the zero are gone. A port is a deploy fact declared in KCL. See
	// config.DefaultServePort for the one port fact forge itself knows.

	// NOTE: internal/generator.ProjectGenerator.Features was quarantined here and
	// was a FALSE POSITIVE. applyKindFeatureDefaults writes g.Features.Codegen /
	// .ORM / .Migrations / .Observability in production, and the rule counted a
	// write THROUGH a field (`g.Features.Codegen = off()`) as a read OF that
	// field. FeaturesConfig also uses *bool where nil deliberately means "derive
	// from project shape" (see FeaturesConfig.resolve), so an unset field is the
	// designed default, not a gap. markWriteChain now marks the whole selector
	// chain; acting on the entry would have deleted working code.
	{
		Rule: RulePhantomField,
		Key:  "internal/generator.ProjectGenerator.BuildVersionVar",
		Cost: "Its own doc comment promises `forge generate` / upgrade re-render with the live " +
			"forge.yaml build.version_var. Nothing assigns it, so project_template_data always passes " +
			"VersionVar:\"\" and the Dockerfile's {{if .VersionVar}} never renders. Owner: internal/generator.",
	},

	// ── Discovery results that never reach the consumer ─────────────────
	{
		Rule: RulePhantomField,
		Key:  "internal/codegen.WorkerSpec.Path",
		Cost: "component_naming reads WorkerSpec.Path; only generator_test sets it. Production always " +
			"reads \"\". Owner: internal/codegen.",
	},
	{
		Rule: RulePhantomField,
		Key:  "internal/codegen.OperatorSpec.Path",
		Cost: "Same as WorkerSpec.Path, for operators. Owner: internal/codegen.",
	},

	// ── A skip result nothing produces ──────────────────────────────────
	{
		Rule: RulePhantomField,
		Key:  "internal/buildtarget.BuildResult.Skipped",
		Cost: "build_external.go branches on res.Skipped to print a skip line; no Runner.Build " +
			"implementation ever sets it, so the branch is unreachable and a genuinely skipped external " +
			"build reports as a normal one. Owner: internal/buildtarget.",
	},
	{
		Rule: RulePhantomField,
		Key:  "internal/buildtarget.BuildResult.SkipMsg",
		Cost: "The message printed by that unreachable branch. Owner: internal/buildtarget.",
	},

	// ── A matcher branch nothing can reach ──────────────────────────────
	{
		Rule: RulePhantomField,
		Key:  "internal/cli.tier1OwnerEntry.prefix",
		Cost: "tier1OwnerEntry.match documents three matchers (exact, prefix, glob) and every entry in " +
			"tier1OwnerRegistry uses exact or glob. The prefix branch is unreachable — the same shape as " +
			"matchServicePort, in the file commit 70c2991b was hardening. Owner: internal/cli.",
	},

	// ── Test-only injection seams ───────────────────────────────────────
	// This is the rule's honest edge: production registers the
	// provider as a zero value, so the field only ever carries a test's
	// value. That is a real constraint on production (it cannot point the
	// provider anywhere but "."), but it is a deliberate seam rather than a
	// dead branch. It is quarantined rather than exempted because the
	// distinction is a judgement about intent that no rule can make, and
	// burying it in an exemption would hide the next one that is NOT a seam.
	{
		Rule: RulePhantomField,
		Key:  "internal/deploytarget.FirebaseProvider.StagingRoot",
		Cost: "deploytarget.go registers FirebaseProvider{}, so firebase.go's StagingRoot is only ever a " +
			"test's value. Deliberate test seam — but production cannot stage anywhere but the default " +
			"root. Owner: internal/deploytarget.",
	},

	// ── No-op functions with live callers ───────────────────────────────
	{
		Rule: RuleNoopFunc,
		Key:  "internal/cli.printDeployExplainHostSkip",
		Cost: "`return nil` reached from deploy --explain, kept \"so the call site keeps compiling while " +
			"the re-wire is in progress\". It is the AppendServiceToConfig shape exactly: the caller " +
			"passes a project config and an env name into nothing, and the user sees no host-mode " +
			"section and no indication one was intended. Owner: internal/cli.",
	},
	{
		Rule: RuleNoopFunc,
		Key:  "internal/cli.loadDeployEnvConfigKV",
		Cost: "Returns an empty map for every (projectDir, envName). The parameters are computed at the " +
			"call site and discarded; the KCL-native config path made the function's job vanish without " +
			"the function. Owner: internal/cli.",
	},
}

// ─────────────────────────────────────────────────────────────────────────────
// The guard
// ─────────────────────────────────────────────────────────────────────────────

// TestNoDeadClaims is the repository-wide guard. It fails on any finding that
// is not quarantined above.
func TestNoDeadClaims(t *testing.T) {
	findings := scanRepo(t)

	q := map[string]quarantined{}
	for _, e := range quarantine {
		q[e.Rule+"|"+e.Key] = e
	}

	var fresh []Finding
	for _, f := range findings {
		if _, ok := q[f.Rule+"|"+f.Key]; !ok {
			fresh = append(fresh, f)
		}
	}
	if len(fresh) == 0 {
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d dead claim(s) — code that asserts something its own data refutes.\n\n", len(fresh))
	for _, f := range fresh {
		fmt.Fprintf(&b, "  %s\n", f)
	}
	b.WriteString("\n")
	b.WriteString("  phantom-field: the field can only ever hold its zero value, so every branch,\n")
	b.WriteString("  format string and heuristic downstream of it is decoration. Either write the\n")
	b.WriteString("  field on the real path, or delete the field AND everything that reads it.\n")
	b.WriteString("  If the only writers are tests, those tests are green on a shape production\n")
	b.WriteString("  cannot produce — that is a false green, not coverage.\n\n")
	b.WriteString("  noop-func: the parameters are a claim nothing backs. Delete the function and\n")
	b.WriteString("  the argument computation at every call site with it.\n\n")
	b.WriteString("  Do NOT quarantine a new finding to make this green. The quarantine records\n")
	b.WriteString("  defects that predate the guard; adding to it is the disease, not the cure.\n")
	t.Error(b.String())
}

// TestQuarantineIsTight makes the ledger a ratchet. An entry that no longer
// matches a finding means the defect was fixed (delete the entry) or the rule
// stopped seeing it (which is a guard regression, and far worse). Either way
// the ledger must not silently carry it.
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
				"  Either the defect was fixed — delete this entry, that is the point of the ratchet —\n"+
				"  or a rule was narrowed until it stopped seeing it, which is a guard regression.",
				id)
		}
	}
}

// scanRepo runs the guard over forge itself, once per test process.
func scanRepo(t *testing.T) []Finding {
	t.Helper()
	// The one gate for every test that judges the real tree: the scan
	// type-checks the whole forge module and is shared through a sync.Once,
	// so gating any single caller only moves the cost to the next one.
	if testing.Short() {
		t.Skip("type-checks the whole forge module with go/packages; runs in task test")
	}
	repoScanOnce.Do(func() {
		repoFindings, repoScanErr = Scan(repoRoot(t), ForgeInternalPrefix)
	})
	if repoScanErr != nil {
		t.Fatalf("scan forge: %v", repoScanErr)
	}
	if len(repoFindings) == 0 && len(quarantine) > 0 {
		t.Fatalf("the scan found nothing at all while %d quarantine entries exist — "+
			"the loader saw an empty or wrong tree, and a scan that sees nothing passes everything",
			len(quarantine))
	}
	return repoFindings
}

var (
	repoScanOnce sync.Once
	repoFindings []Finding
	repoScanErr  error
)

// ─────────────────────────────────────────────────────────────────────────────
// Rule-level proof: the planted defects in testdata/fixture
//
// A guard that cannot fail is the disease it is curing. These tests run the
// real rules over a module of deliberately-broken code and assert the exact
// set of findings — so both directions are pinned: the defects fire, and the
// look-alikes beside them do not.
// ─────────────────────────────────────────────────────────────────────────────

func TestRulesFireOnPlantedDefects(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks the testdata fixture module with go/packages; runs in task test")
	}
	fixture := fixtureDir(t)

	got, err := ScanStandalone(fixture, "deadcodeguardfixture/internal/")
	if err != nil {
		t.Fatalf("scan fixture: %v", err)
	}

	want := []string{
		"noop-func|deadcodeguardfixture/internal/noop.AppendServiceToConfig",
		"noop-func|deadcodeguardfixture/internal/noop.BranchingNoop",
		"noop-func|deadcodeguardfixture/internal/noop.LoadConfigKV",
		"noop-func|deadcodeguardfixture/internal/perplatform.probe",
		"phantom-field|deadcodeguardfixture/internal/phantom.Component.Ports",
		"phantom-field|deadcodeguardfixture/internal/phantom.Component.Schedule",
		"phantom-field|deadcodeguardfixture/internal/phantom.Outer.Inner",
	}
	var gotKeys []string
	for _, f := range got {
		gotKeys = append(gotKeys, f.Rule+"|"+f.Key)
	}
	sort.Strings(gotKeys)

	if strings.Join(gotKeys, "\n") != strings.Join(want, "\n") {
		t.Errorf("fixture findings mismatch.\n got:\n  %s\nwant:\n  %s",
			strings.Join(gotKeys, "\n  "), strings.Join(want, "\n  "))
	}

	// The two flavours must be distinguishable in the message, because they
	// call for different fixes: a test-only write means a test is green on
	// an impossible shape, a no-write-anywhere means the read is decoration.
	for _, f := range got {
		if f.Key == "deadcodeguardfixture/internal/phantom.Component.Ports" &&
			!strings.Contains(f.Detail, "only writers are") {
			t.Errorf("Ports finding does not name its test-only writers: %s", f.Detail)
		}
		if f.Key == "deadcodeguardfixture/internal/phantom.Component.Schedule" &&
			strings.Contains(f.Detail, "only writers are") {
			t.Errorf("Schedule has no writers at all; the finding must not claim test writers: %s", f.Detail)
		}
	}
}

// TestExemptionsAreLoadBearing names, one by one, the look-alikes the fixture
// keeps beside the planted defects. Asserting the negative as a LIST (rather
// than relying on the set comparison above) means a future widening of a rule
// fails with the name of the construct it broke.
func TestExemptionsAreLoadBearing(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks the testdata fixture module with go/packages; runs in task test")
	}
	fixture := fixtureDir(t)
	got, err := ScanStandalone(fixture, "deadcodeguardfixture/internal/")
	if err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	fired := map[string]bool{}
	for _, f := range got {
		fired[f.Key] = true
	}

	mustNotFire := []struct{ key, why string }{
		{"deadcodeguardfixture/internal/phantom.Component.Kind", "written by NewComponent through a field assignment"},
		{"deadcodeguardfixture/internal/phantom.Component.Name", "written by NewComponent through a keyed literal"},
		{"deadcodeguardfixture/internal/phantom.Tagged.Label", "a json-tagged struct is a reflection target; the decoder is the writer"},
		{"deadcodeguardfixture/internal/phantom.Tagged.Other", "same struct, same reason"},
		{"deadcodeguardfixture/internal/phantom.Seams.Runner", "a func-typed field is an injection seam"},
		{"deadcodeguardfixture/internal/phantom.Seams.Sink", "an interface-typed field is an injection seam"},
		{"deadcodeguardfixture/internal/phantom.Seams.mu", "mu.Lock() has a pointer receiver: it mutates, it does not read"},
		{"deadcodeguardfixture/internal/phantom.Seams.Positional", "written by an UNKEYED composite literal"},
		{"deadcodeguardfixture/internal/phantom.Seams.guarded", "written by the same unkeyed literal"},
		{"deadcodeguardfixture/internal/phantom.Client.cached", "a value-typed cache is written through its subfields and its mutex"},
		{"deadcodeguardfixture/internal/phantom.Runner.Flags", "written through a subfield (`r.Flags.Verbose = …`)"},
		{"deadcodeguardfixture/internal/phantom.Leaf.Value", "written by NewLeaf's keyed literal"},
		{"deadcodeguardfixture/internal/phantom.Cross.CrossWritten", "written from another package — only a whole-program scan sees it"},
		{"deadcodeguardfixture/internal/phantom.Emitted.Hook", "declared in a generated file; the emitter re-creates it every run"},
		{"deadcodeguardfixture/internal/phantom.EmittedNoop", "a generated no-op is the emitter's shape, not a human's claim"},
		{"deadcodeguardfixture/internal/noop.Register", "no parameters: nothing for the body to ignore"},
		{"deadcodeguardfixture/internal/noop.Render", "does real work before returning nil"},
		{"deadcodeguardfixture/internal/noop.Resolve", "a METHOD satisfying an interface — the Null Object pattern is deliberate"},
		{"deadcodeguardfixture/internal/perplatform.Snapshot.PID", "written only in a _windows.go file — a host-only load cannot see it"},
		{"deadcodeguardfixture/internal/perplatform.Snapshot.Created", "same Windows-only writer"},
		{"deadcodeguardfixture/internal/perplatform.lock", "a per-OS seam: a stub on Windows, real work on Unix under the same name"},
		{"deadcodeguardfixture/internal/noop.New", "returns a real value"},
	}
	for _, m := range mustNotFire {
		if fired[m.key] {
			t.Errorf("%s was flagged, but it is legitimate (%s).\n"+
				"A rule was widened until it swallowed a good construct. Narrow the rule; do not\n"+
				"delete the fixture.", m.key, m.why)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Plumbing
// ─────────────────────────────────────────────────────────────────────────────

// fixtureDir is testdata/fixture as an absolute path, resolved against the
// package directory `go test` runs in (not runtime.Caller; see repoRoot).
func fixtureDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("testdata", "fixture"))
	if err != nil {
		t.Fatalf("resolve testdata/fixture: %v", err)
	}
	return dir
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
