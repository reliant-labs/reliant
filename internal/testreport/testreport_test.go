package testreport_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/testreport"
)

// stream builds a `go test -json` stream the way cmd/go emits one: a run
// event and a terminal event per test, then a terminal package event.
//
// The fabrication is the point. Every rule here has to be provable against a
// shape nobody has to reproduce with a real database, a real docker daemon, or
// a real 40-second suite — otherwise the tests for the skip gate would
// themselves be gated on the environment, which is the disease.
func stream(pkg string, outcomes map[string]string, pkgResult string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{"Action":"start","Package":%q}`+"\n", pkg)
	// Deterministic order is irrelevant to the tallies but keeps golden
	// comparisons stable if any are added later.
	for _, name := range sortedKeys(outcomes) {
		fmt.Fprintf(&b, `{"Action":"run","Package":%q,"Test":%q}`+"\n", pkg, name)
		fmt.Fprintf(&b, `{"Action":"output","Package":%q,"Test":%q,"Output":"--- %s\n"}`+"\n",
			pkg, name, strings.ToUpper(outcomes[name]))
		fmt.Fprintf(&b, `{"Action":%q,"Package":%q,"Test":%q,"Elapsed":0.01}`+"\n", outcomes[name], pkg, name)
	}
	if pkgResult != "" {
		fmt.Fprintf(&b, `{"Action":%q,"Package":%q,"Elapsed":0.2}`+"\n", pkgResult, pkg)
	}
	return b.String()
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// nTests builds an outcome map with `pass` passing tests and `skip` skipping.
func nTests(pass, skip int) map[string]string {
	m := map[string]string{}
	for i := range pass {
		m[fmt.Sprintf("TestPass%02d", i)] = "pass"
	}
	for i := range skip {
		m[fmt.Sprintf("TestSkip%02d", i)] = "skip"
	}
	return m
}

func analyze(t *testing.T, in string, policy testreport.Policy) testreport.Analysis {
	t.Helper()
	run, err := testreport.Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return testreport.Analyze(run, policy)
}

// ─────────────────────────────────────────────────────────────────────────────
// The four shapes the gate has to tell apart.
// ─────────────────────────────────────────────────────────────────────────────

// A healthy package passes with an empty body. Silence on healthy packages is
// the property that makes the report readable at all: if a clean run printed a
// list, nobody would read the list on the run that mattered.
func TestAnalyze_HealthyPackageIsAPassWithNoFindings(t *testing.T) {
	a := analyze(t, stream("example.com/app/internal/healthy", nTests(40, 0), "pass"), testreport.DefaultPolicy())

	if got := a.Status(); got != testreport.StatusPass {
		t.Fatalf("healthy package: status = %q, want %q", got, testreport.StatusPass)
	}
	if len(a.Findings) != 0 {
		t.Fatalf("healthy package produced %d finding(s): %+v", len(a.Findings), a.Findings)
	}
	var buf bytes.Buffer
	testreport.Render(&buf, a)
	if !strings.Contains(buf.String(), "40 test(s): 40 passed") {
		t.Errorf("the pass line must STATE what it verified, got:\n%s", buf.String())
	}
}

// A package with a handful of legitimate skips is still a pass. This is the
// reference project's healthy shape — 1 unconditional skip out of 173 — and
// the single most important negative case: cry wolf here and the gate is
// switched off before it ever catches the real thing.
func TestAnalyze_AFewLegitimateSkipsAreNotAFinding(t *testing.T) {
	for _, tc := range []struct{ pass, skip int }{
		{172, 1}, // the reference project's healthy internal/threads
		{60, 3},  // a few documented framework-limitation skips
		{20, 9},  // just under half
		{10, 10}, // exactly half: the threshold is "MORE than half"
	} {
		t.Run(fmt.Sprintf("%dpass_%dskip", tc.pass, tc.skip), func(t *testing.T) {
			a := analyze(t, stream("example.com/app/internal/ok", nTests(tc.pass, tc.skip), "pass"), testreport.DefaultPolicy())
			if len(a.Findings) != 0 {
				t.Fatalf("%d/%d skipped produced a finding: %+v", tc.skip, tc.pass+tc.skip, a.Findings)
			}
			if a.Status() != testreport.StatusPass {
				t.Fatalf("status = %q, want pass", a.Status())
			}
		})
	}
}

// A package skipping ~90% is the disease: reported, named, with its ratio.
func TestAnalyze_MassSkipIsReported(t *testing.T) {
	a := analyze(t, stream("example.com/app/internal/threads", nTests(9, 91), "pass"), testreport.DefaultPolicy())

	if a.Status() != testreport.StatusFail {
		t.Fatalf("status = %q, want fail", a.Status())
	}
	if len(a.Findings) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(a.Findings), a.Findings)
	}
	f := a.Findings[0]
	if f.Kind != testreport.KindMassSkip {
		t.Errorf("kind = %q, want %q", f.Kind, testreport.KindMassSkip)
	}
	if f.Skipped != 91 || f.Tests != 100 {
		t.Errorf("counts = %d/%d, want 91/100", f.Skipped, f.Tests)
	}
	var buf bytes.Buffer
	testreport.Render(&buf, a)
	out := buf.String()
	for _, want := range []string{"internal/threads", "91 of 100 tests skipped", "e.g. "} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
}

// Unparseable input is UNDETERMINED, never a pass. The whole point of the
// three-state model: "forge could not obtain the facts" and "the facts are
// fine" must never render the same.
func TestAnalyze_UnparseableInputIsUndeterminedNotPass(t *testing.T) {
	junk := "ok  \texample.com/app/internal/threads\t0.412s\n" +
		"ok  \texample.com/app/internal/other\t1.002s\n"

	a := analyze(t, junk, testreport.DefaultPolicy())

	if got := a.Status(); got != testreport.StatusUndetermined {
		t.Fatalf("plain `go test` output (no -json): status = %q, want %q — a pass here is a claim about a file forge never read", got, testreport.StatusUndetermined)
	}
	if len(a.Findings) != 0 {
		t.Errorf("undetermined input must not manufacture findings: %+v", a.Findings)
	}
	var buf bytes.Buffer
	testreport.Render(&buf, a)
	out := buf.String()
	if !strings.Contains(out, "UNDETERMINED") {
		t.Errorf("report must say UNDETERMINED:\n%s", out)
	}
	if strings.Contains(out, "✅") {
		t.Errorf("report must not show a pass mark:\n%s", out)
	}
}

// An empty reader is a distinct error, so the CLI can say "nothing was piped
// in" rather than "your suite is fine".
func TestParse_EmptyInputIsAnError(t *testing.T) {
	if _, err := testreport.Parse(strings.NewReader("")); err == nil {
		t.Fatal("Parse(\"\") returned no error — an empty stream is the absence of facts, not a clean run")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// zero-evidence
// ─────────────────────────────────────────────────────────────────────────────

// A package where every test skipped is reported at ANY size — the mass-skip
// sample-size floor must not shelter it. The reference project has exactly
// this: a 4-test package, 4 skips, invisible to a `MinTests: 5` rule.
func TestAnalyze_ZeroEvidenceIgnoresTheSampleSizeFloor(t *testing.T) {
	a := analyze(t, stream("example.com/app/internal/mcpconfig", nTests(0, 4), "pass"), testreport.DefaultPolicy())

	if len(a.Findings) != 1 {
		t.Fatalf("4-of-4 skipped produced %d finding(s), want 1 — MinTests must not shelter a package that proved nothing", len(a.Findings))
	}
	if a.Findings[0].Kind != testreport.KindZeroEvidence {
		t.Errorf("kind = %q, want %q", a.Findings[0].Kind, testreport.KindZeroEvidence)
	}
}

// The floor still does its job on a genuinely tiny sample that ran something:
// 1-of-2 is 50%, which is arithmetic, not evidence.
func TestAnalyze_TinySampleIsNotAFinding(t *testing.T) {
	a := analyze(t, stream("example.com/app/internal/tiny", nTests(1, 2), "pass"), testreport.DefaultPolicy())
	if len(a.Findings) != 0 {
		t.Fatalf("2-of-3 skipped in a 3-test package produced a finding: %+v", a.Findings)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Leaf counting
// ─────────────────────────────────────────────────────────────────────────────

// A parent test is a container, not a test. Counting it alongside its children
// lets a package inflate its pass count with containers while every leaf
// beneath them skipped — which is exactly how a table-driven suite hides.
func TestParse_ParentTestsAreNotCountedAsTests(t *testing.T) {
	in := stream("example.com/app/internal/table", map[string]string{
		"TestTable":        "pass", // container: all children skipped
		"TestTable/case_a": "skip",
		"TestTable/case_b": "skip",
		"TestTable/case_c": "skip",
		"TestTable/case_d": "skip",
		"TestTable/case_e": "skip",
		"TestTable/case_f": "skip",
		"TestOther":        "pass",
	}, "pass")

	run, err := testreport.Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(run.Packages) != 1 {
		t.Fatalf("want 1 package, got %d", len(run.Packages))
	}
	p := run.Packages[0]
	if p.Tests != 7 || p.Skipped != 6 || p.Passed != 1 {
		t.Fatalf("leaf tally = %d tests / %d passed / %d skipped, want 7/1/6 (TestTable is a container)", p.Tests, p.Passed, p.Skipped)
	}
	if got := p.SkipRatio(); got < 0.85 || got > 0.86 {
		t.Errorf("skip ratio = %v, want ~0.857 — counting the container would report 0.75 and understate the loss", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Declared exemptions
// ─────────────────────────────────────────────────────────────────────────────

func TestAnalyze_DeclaredExemptionSuppressesTheFinding(t *testing.T) {
	policy := testreport.DefaultPolicy()
	policy.Allow = []testreport.Exemption{{
		Package: "internal/dockerintegration",
		Reason:  "every test here needs a live docker daemon",
	}}

	a := analyze(t, stream("example.com/app/internal/dockerintegration", nTests(0, 30), "pass"), policy)

	if len(a.Findings) != 0 {
		t.Fatalf("declared exemption did not suppress: %+v", a.Findings)
	}
	if len(a.Suppressed) != 1 {
		t.Fatalf("want 1 suppressed record, got %d", len(a.Suppressed))
	}
	if a.Status() != testreport.StatusPass {
		t.Errorf("status = %q, want pass", a.Status())
	}
	var buf bytes.Buffer
	testreport.Render(&buf, a)
	// Suppression must still be VISIBLE — silent suppression is how an
	// exemption outlives its reason.
	out := buf.String()
	if !strings.Contains(out, "live docker daemon") {
		t.Errorf("the report must name the declared reason:\n%s", out)
	}
	// The headline must not contradict its own footnote: this package DID
	// skip everything, it was merely excused.
	if strings.Contains(out, "of its tests.\n") {
		t.Errorf("the pass line claims nothing skipped heavily while listing a package that did:\n%s", out)
	}
	if !strings.Contains(out, "apart from the 1 declared below") {
		t.Errorf("the pass line must acknowledge the suppression:\n%s", out)
	}
}

// An exemption with a ceiling suppresses up to that ceiling and no further,
// so a package that got WORSE than declared is still reported.
func TestAnalyze_ExemptionCeilingStillCatchesRegression(t *testing.T) {
	policy := testreport.DefaultPolicy()
	policy.Allow = []testreport.Exemption{{
		Package:      "internal/partly",
		Reason:       "the integration half needs a broker",
		MaxSkipRatio: 0.6,
	}}

	ok := analyze(t, stream("example.com/app/internal/partly", nTests(45, 55), "pass"), policy)
	if len(ok.Findings) != 0 {
		t.Fatalf("55%% skipped under a 60%% ceiling was reported: %+v", ok.Findings)
	}
	bad := analyze(t, stream("example.com/app/internal/partly", nTests(20, 80), "pass"), policy)
	if len(bad.Findings) != 1 {
		t.Fatalf("80%% skipped under a 60%% ceiling produced %d finding(s), want 1", len(bad.Findings))
	}
}

// An exemption that stops suppressing anything is dead config — the same
// unfalsifiable shape the gate exists to find. Reported, never fatal: the run
// that healed the package must not go red for having healed it.
func TestAnalyze_StaleExemptionIsReportedButNotFatal(t *testing.T) {
	policy := testreport.DefaultPolicy()
	policy.Allow = []testreport.Exemption{{
		Package: "internal/healed",
		Reason:  "needed a database once",
	}}

	a := analyze(t, stream("example.com/app/internal/healed", nTests(50, 1), "pass"), policy)

	if a.Status() != testreport.StatusPass {
		t.Fatalf("status = %q, want pass — a stale exemption must not fail the run", a.Status())
	}
	if len(a.Stale) != 1 {
		t.Fatalf("want 1 stale exemption, got %d", len(a.Stale))
	}
	var buf bytes.Buffer
	testreport.Render(&buf, a)
	if !strings.Contains(buf.String(), "no longer needed") {
		t.Errorf("report must flag the dead declaration:\n%s", buf.String())
	}
}

// A declaration for a package this run never mentioned is SILENT. A scoped run
// (`go test ./internal/threads/`) legitimately omits every other package, and
// reporting those would put a paragraph of noise on the most common local
// invocation — the way a check teaches people to skim past it.
func TestAnalyze_ExemptionForAnAbsentPackageIsSilent(t *testing.T) {
	policy := testreport.DefaultPolicy()
	policy.Allow = []testreport.Exemption{{Package: "internal/elsewhere", Reason: "docker"}}

	a := analyze(t, stream("example.com/app/internal/threads", nTests(50, 1), "pass"), policy)

	if len(a.Stale) != 0 {
		t.Fatalf("an unmatched exemption was reported as stale: %+v — a scoped run would drown in these", a.Stale)
	}
}

func TestPolicy_ValidateRejectsUnusableDeclarations(t *testing.T) {
	cases := []struct {
		name string
		e    testreport.Exemption
		want string
	}{
		{"no reason", testreport.Exemption{Package: "internal/x"}, "reason"},
		{"no package", testreport.Exemption{Reason: "because"}, "empty"},
		{"whole module", testreport.Exemption{Package: "./...", Reason: "because"}, "whole module"},
		{"ratio out of range", testreport.Exemption{Package: "internal/x", Reason: "because", MaxSkipRatio: 4}, "between 0 and 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := testreport.Policy{Allow: []testreport.Exemption{tc.e}}.Validate()
			if len(errs) == 0 {
				t.Fatalf("Validate accepted %+v", tc.e)
			}
			joined := fmt.Sprint(errs)
			if !strings.Contains(joined, tc.want) {
				t.Errorf("error %q does not mention %q", joined, tc.want)
			}
		})
	}
}

func TestMatchPackage_TailFragmentNeedsASlashBoundary(t *testing.T) {
	policy := testreport.DefaultPolicy()
	policy.Allow = []testreport.Exemption{{Package: "internal/db", Reason: "needs postgres"}}

	// "internal/db" must not swallow "internal/scaffolddb".
	a := analyze(t, stream("example.com/app/internal/scaffolddb", nTests(0, 20), "pass"), policy)
	if len(a.Findings) != 1 {
		t.Fatalf("exemption for internal/db suppressed internal/scaffolddb: %+v", a)
	}

	// The subtree form does reach nested packages.
	sub := testreport.DefaultPolicy()
	sub.Allow = []testreport.Exemption{{Package: "internal/db/...", Reason: "needs postgres"}}
	nested := analyze(t, stream("example.com/app/internal/db/migrations", nTests(0, 20), "pass"), sub)
	if len(nested.Findings) != 0 {
		t.Fatalf("`internal/db/...` did not cover internal/db/migrations: %+v", nested.Findings)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Facts forge could not obtain
// ─────────────────────────────────────────────────────────────────────────────

// A stream that stops mid-package holds PARTIAL tallies. Reporting the
// fraction that arrived would be a confident answer to a question nobody
// finished asking.
func TestAnalyze_TruncatedStreamIsUndetermined(t *testing.T) {
	full := stream("example.com/app/internal/threads", nTests(30, 1), "pass")
	truncated := full[:len(full)*2/3]

	a := analyze(t, truncated, testreport.DefaultPolicy())

	if a.Status() != testreport.StatusUndetermined {
		t.Fatalf("truncated stream: status = %q, want undetermined", a.Status())
	}
	if len(a.Undetermined) == 0 {
		t.Fatal("no undetermined entry recorded")
	}
	var buf bytes.Buffer
	testreport.Render(&buf, a)
	if !strings.Contains(buf.String(), "UNDETERMINED") {
		t.Errorf("report must say UNDETERMINED:\n%s", buf.String())
	}
}

// "[no test files]" is NOT APPLICABLE, not a skip finding. A package with no
// tests lost nothing.
func TestAnalyze_NoTestFilesIsNotAFinding(t *testing.T) {
	in := `{"Action":"start","Package":"example.com/app/internal/types"}` + "\n" +
		`{"Action":"output","Package":"example.com/app/internal/types","Output":"?   \texample.com/app/internal/types\t[no test files]\n"}` + "\n" +
		`{"Action":"skip","Package":"example.com/app/internal/types","Elapsed":0}` + "\n"

	a := analyze(t, in, testreport.DefaultPolicy())

	if len(a.Findings) != 0 {
		t.Fatalf("a package with no test files produced a finding: %+v", a.Findings)
	}
	if a.Totals.NoTestFiles != 1 {
		t.Errorf("NoTestFiles = %d, want 1", a.Totals.NoTestFiles)
	}
	if a.Status() != testreport.StatusNotApplicable {
		t.Errorf("status = %q, want %q", a.Status(), testreport.StatusNotApplicable)
	}
}

// A package that did not compile is a failure, not "zero tests, nothing to
// see". cmd/go reports it with ImportPath and no Package field, which a parser
// that only knows Package drops on the floor.
func TestAnalyze_BuildFailureIsReported(t *testing.T) {
	in := `{"Action":"build-output","ImportPath":"example.com/app/internal/broken","Output":"./x.go:3:2: undefined: y\n"}` + "\n" +
		`{"Action":"build-fail","ImportPath":"example.com/app/internal/broken"}` + "\n"

	a := analyze(t, in, testreport.DefaultPolicy())

	if a.Status() != testreport.StatusFail {
		t.Fatalf("status = %q, want fail", a.Status())
	}
	if len(a.Findings) != 1 || a.Findings[0].Kind != testreport.KindFailed {
		t.Fatalf("want one KindFailed finding, got %+v", a.Findings)
	}
}

// Failures in the stream fail the command. Without this,
// `go test -json ./... | forge ci verify-test-run` in a shell with no
// `set -o pipefail` reports only forge's status and launders a red suite green.
func TestAnalyze_TestFailuresFailTheRun(t *testing.T) {
	in := stream("example.com/app/internal/x", map[string]string{
		"TestA": "pass", "TestB": "fail", "TestC": "pass",
	}, "fail")

	a := analyze(t, in, testreport.DefaultPolicy())

	if !a.Totals.SuiteFailed {
		t.Fatal("SuiteFailed = false over a stream containing a failing test")
	}
	if a.Status() != testreport.StatusFail {
		t.Fatalf("status = %q, want fail", a.Status())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Report shape
// ─────────────────────────────────────────────────────────────────────────────

// The report is sorted worst-first and capped. A reader who stops after the
// first line has read the worst package; a reader facing 200 bad packages is
// not handed 200 lines.
func TestRender_IsSortedWorstFirstAndCapped(t *testing.T) {
	var b strings.Builder
	b.WriteString(stream("example.com/app/z_mild", nTests(4, 6), "pass"))   // 0.60
	b.WriteString(stream("example.com/app/a_total", nTests(0, 10), "pass")) // 1.00
	b.WriteString(stream("example.com/app/m_heavy", nTests(1, 19), "pass")) // 0.95
	for i := range 25 {
		b.WriteString(stream(fmt.Sprintf("example.com/app/bulk%02d", i), nTests(1, 9), "pass"))
	}

	a := analyze(t, b.String(), testreport.DefaultPolicy())
	var buf bytes.Buffer
	testreport.Render(&buf, a)
	out := buf.String()

	if !strings.Contains(out, "and 8 more") {
		t.Errorf("the list must be capped, got:\n%s", out)
	}
	total := strings.Index(out, "a_total") // 1.00, zero-evidence
	heavy := strings.Index(out, "m_heavy") // 0.95, mass-skip
	if total < 0 || heavy < 0 || total > heavy {
		t.Errorf("findings are not worst-first (zero-evidence should lead):\n%s", out)
	}
	// The mildest package (0.60) is below 20 worse ones and is correctly
	// cut: the cap drops the tail, never the head.
	if strings.Contains(out, "z_mild") {
		t.Errorf("the cap dropped the wrong end of the list:\n%s", out)
	}
	if n := strings.Count(out, "\n"); n > 60 {
		t.Errorf("report is %d lines for 28 bad packages — a wall nobody reads:\n%s", n, out)
	}
}

// A healthy multi-package run prints no per-package line at all.
func TestRender_SaysNothingAboutHealthyPackages(t *testing.T) {
	var b strings.Builder
	for i := range 40 {
		b.WriteString(stream(fmt.Sprintf("example.com/app/pkg%02d", i), nTests(20, 1), "pass"))
	}
	a := analyze(t, b.String(), testreport.DefaultPolicy())

	var buf bytes.Buffer
	testreport.Render(&buf, a)
	out := buf.String()

	if strings.Contains(out, "pkg00") {
		t.Errorf("a healthy package was listed:\n%s", out)
	}
	if lines := strings.Count(strings.TrimSpace(out), "\n") + 1; lines > 3 {
		t.Errorf("a clean 40-package run printed %d lines, want <= 3:\n%s", lines, out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Skip reasons
// ─────────────────────────────────────────────────────────────────────────────

// The t.Skip message is the actionable half of a finding: "124 tests skipped"
// is a number to argue with, `118× "DATABASE_URL not set"` is an instruction.
// cmd/go prints it as an ordinary output line just before `--- SKIP:`.
func TestAnalyze_ReportsTheDominantSkipReason(t *testing.T) {
	var b strings.Builder
	const pkg = "example.com/app/internal/threads"
	fmt.Fprintf(&b, `{"Action":"start","Package":%q}`+"\n", pkg)
	for i := range 20 {
		name := fmt.Sprintf("TestDB%02d", i)
		fmt.Fprintf(&b, `{"Action":"run","Package":%q,"Test":%q}`+"\n", pkg, name)
		fmt.Fprintf(&b, `{"Action":"output","Package":%q,"Test":%q,"Output":"=== RUN   %s\n"}`+"\n", pkg, name, name)
		fmt.Fprintf(&b, `{"Action":"output","Package":%q,"Test":%q,"Output":"    bounded_messages_test.go:30: DATABASE_URL not set, skipping database test\n"}`+"\n", pkg, name)
		fmt.Fprintf(&b, `{"Action":"output","Package":%q,"Test":%q,"Output":"--- SKIP: %s (0.00s)\n"}`+"\n", pkg, name, name)
		fmt.Fprintf(&b, `{"Action":"skip","Package":%q,"Test":%q}`+"\n", pkg, name)
	}
	for i := range 3 {
		name := fmt.Sprintf("TestDocker%02d", i)
		fmt.Fprintf(&b, `{"Action":"output","Package":%q,"Test":%q,"Output":"    x_test.go:9: docker is not available\n"}`+"\n", pkg, name)
		fmt.Fprintf(&b, `{"Action":"skip","Package":%q,"Test":%q}`+"\n", pkg, name)
	}
	fmt.Fprintf(&b, `{"Action":"pass","Package":%q,"Test":"TestPure"}`+"\n", pkg)
	fmt.Fprintf(&b, `{"Action":"pass","Package":%q,"Elapsed":0.2}`+"\n", pkg)

	a := analyze(t, b.String(), testreport.DefaultPolicy())

	if len(a.Findings) != 1 {
		t.Fatalf("want 1 finding, got %+v", a.Findings)
	}
	f := a.Findings[0]
	if len(f.Reasons) != 2 {
		t.Fatalf("want the two distinct skip reasons, got %+v", f.Reasons)
	}
	// Most common first, and the "file_test.go:NN: " prefix is stripped so
	// the same message from two call sites counts as one reason.
	if f.Reasons[0].Count != 20 || f.Reasons[0].Reason != "DATABASE_URL not set, skipping database test" {
		t.Errorf("dominant reason = %+v, want 20x the DATABASE_URL message", f.Reasons[0])
	}
	if f.Reasons[1].Count != 3 || f.Reasons[1].Reason != "docker is not available" {
		t.Errorf("second reason = %+v", f.Reasons[1])
	}
	// Reasons replace the test-name examples: two lines to say one thing
	// is how a report stops being read.
	if len(f.Examples) != 0 {
		t.Errorf("names were printed alongside reasons: %v", f.Examples)
	}

	var buf bytes.Buffer
	testreport.Render(&buf, a)
	if !strings.Contains(buf.String(), `20× "DATABASE_URL not set, skipping database test"`) {
		t.Errorf("the report must show the actionable reason:\n%s", buf.String())
	}
}

// When no message can be recovered the report falls back to test names — a
// verdict must never depend on scraping free text.
func TestAnalyze_FallsBackToNamesWhenNoReasonIsRecoverable(t *testing.T) {
	a := analyze(t, stream("example.com/app/internal/quiet", nTests(1, 30), "pass"), testreport.DefaultPolicy())

	if len(a.Findings) != 1 {
		t.Fatalf("want 1 finding, got %+v", a.Findings)
	}
	if len(a.Findings[0].Examples) == 0 {
		t.Error("no reasons AND no example names — the finding says only a number")
	}
}

// A container parent that also skipped must not inflate the reason count past
// the skip count printed beside it. Two numbers on the same line that disagree
// undermine both.
func TestParse_ReasonCountsExcludeContainerParents(t *testing.T) {
	const pkg = "example.com/app/internal/nested"
	var b strings.Builder
	for _, name := range []string{"TestP", "TestP/case_a", "TestP/case_b"} {
		fmt.Fprintf(&b, `{"Action":"output","Package":%q,"Test":%q,"Output":"    x_test.go:1: needs a broker\n"}`+"\n", pkg, name)
		fmt.Fprintf(&b, `{"Action":"skip","Package":%q,"Test":%q}`+"\n", pkg, name)
	}
	fmt.Fprintf(&b, `{"Action":"pass","Package":%q,"Elapsed":0.1}`+"\n", pkg)

	run, err := testreport.Parse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := run.Packages[0]
	if p.Skipped != 2 {
		t.Fatalf("leaf skips = %d, want 2 (TestP is a container)", p.Skipped)
	}
	if got := p.SkipReasons["needs a broker"]; got != 2 {
		t.Errorf("reason count = %d, want 2 — it must match the skip count beside it", got)
	}
}
