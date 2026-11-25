package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cigate"
	"github.com/reliant-labs/forge/internal/cli/lint"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/vulnscan"
	"github.com/reliant-labs/forge/pkg/release"
)

// The grades a project pinned at pin gets for both rules.
func pinnedVulnGrades(pin string, rules map[string]string) vulnGrades {
	return gradeVulnRulesFor(&config.ProjectConfig{ForgeVersion: pin, Lint: config.LintConfig{Rules: rules}})
}

// gatingPin and warningPin are a pin at the rules' GatesFrom and one below it.
var (
	gatingPin  = lint.GradeRule(nil, lint.RuleVulnGate).GatesFrom
	warningPin = lint.GradeRule(nil, lint.RuleVulnGate).IntroducedIn
)

// fakeVulnSource states the scans of a release.
type fakeVulnSource struct {
	scans   []vulnscan.ArtifactScan
	fetched *bool
}

func (f fakeVulnSource) gateFindings(_ context.Context, _ *release.Release, fetch bool) (vulnGateFindings, error) {
	if f.fetched != nil {
		*f.fetched = fetch
	}
	return vulnGateFindings{Scans: f.scans}, nil
}

func criticalFinding() vulnscan.Finding {
	return vulnscan.Finding{
		ID: "CVE-2026-1234", Aliases: []string{"GHSA-aaaa-bbbb-cccc"}, Severity: vulnscan.SeverityCritical,
		Package:  vulnscan.Package{Name: "golang.org/x/net", Version: "v0.30.0", Type: "go-module"},
		FixState: vulnscan.FixStateFixed, FixedIn: []string{"0.33.0"},
	}
}

func scanWith(built time.Time, fs ...vulnscan.Finding) *vulnscan.Scan {
	if fs == nil {
		fs = []vulnscan.Finding{}
	}
	return &vulnscan.Scan{Schema: vulnscan.ScanSchema, Scanner: "grype", ScannerVersion: "v0.122.1", DBBuiltAt: built, ScannedAt: built, Findings: fs}
}

// sbomRelease is a release whose one image has an SBOM (sbom=true) or a ref
// saying it has none.
func sbomRelease(sbom bool) *release.Release {
	ref := &release.SBOMRef{Digest: "sha256:" + strings.Repeat("d", 64), Status: release.SBOMStatusOK}
	if !sbom {
		ref = &release.SBOMRef{Status: release.SBOMStatusUnavailable, Reason: "skipped: --no-sbom"}
	}
	return &release.Release{Version: "v1.7.0", Artifacts: map[string]release.Artifact{
		"api": {Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{release.SharedVariant: "sha256:" + strings.Repeat("b", 64)}, SBOM: ref},
		// A source pin ships no code: never part of coverage.
		"web-source": {Kind: release.KindGit},
	}}
}

func enforcePolicy() release.ShapeDeliveryPolicy {
	p := release.DefaultDeliveryPolicy()
	p.VulnGate.Mode = release.DeliveryEnforce
	return p
}

func judge(t *testing.T, in vulnPromoteInput, src vulnFindingsSource) ([]release.Gate, string, error) {
	t.Helper()
	if in.Env == "" {
		in.Env, in.Version, in.Promoter = "prod", "v1.7.0", "alice"
	}
	if in.Now == nil {
		in.Now = func() time.Time { return vulnTestNow }
	}
	in.Ask = true
	var out bytes.Buffer
	gates, err := judgeVulnGates(context.Background(), &out, in, src)
	return gates, out.String(), err
}

func gateNamed(gates []release.Gate, name string) *release.Gate {
	for i := range gates {
		if gates[i].Name == name {
			return &gates[i]
		}
	}
	return nil
}

func vulnRefusalReason(t *testing.T, err error) string {
	t.Helper()
	var ref *promoteRefusedError
	if !errors.As(err, &ref) {
		t.Fatalf("want a promotion refusal, got %v", err)
	}
	if ref.ExitCode() != exitRefused {
		t.Errorf("refusal %s exits %d, want %d", ref.Reason, ref.ExitCode(), exitRefused)
	}
	return ref.Reason
}

func blockingSource() fakeVulnSource {
	return fakeVulnSource{scans: []vulnscan.ArtifactScan{{Artifact: "api", SBOMDigest: "sha256:x", Latest: scanWith(vulnTestNow.Add(-time.Hour), criticalFinding())}}}
}

func TestVulnGateEnforceRefusesABlockingFindingOnceItGates(t *testing.T) {
	_, _, err := judge(t, vulnPromoteInput{Policy: enforcePolicy(), Release: sbomRelease(true), Grades: pinnedVulnGrades(gatingPin, nil)}, blockingSource())
	if got := vulnRefusalReason(t, err); got != reasonVulnGateBlocked {
		t.Fatalf("reason %s, want %s", got, reasonVulnGateBlocked)
	}
	for _, want := range []string{"CVE-2026-1234 in golang.org/x/net v0.30.0 (fixed in 0.33.0)", "forge vuln exempt prod CVE-2026-1234", "--override-vuln-gate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not say %q:\n%s", want, err)
		}
	}
}

func TestVulnGateEnforceBelowGatesFromIsJudgedAsAudit(t *testing.T) {
	gates, out, err := judge(t, vulnPromoteInput{Policy: enforcePolicy(), Release: sbomRelease(true), Grades: pinnedVulnGrades(warningPin, nil)}, blockingSource())
	if err != nil {
		t.Fatalf("a rule that does not gate yet refused: %v", err)
	}
	g := gateNamed(gates, vulnGateGateName)
	if g == nil || g.Status != release.GateStatusFailed || g.Details["outcome"] != "audit" || g.Details["graded_as"] != "audit" || g.Details["mode"] != "enforce" {
		t.Fatalf("recorded %+v, want an audit verdict of an enforce", g)
	}
	want := "new in " + warningPin + "; gates from " + gatingPin
	if !strings.Contains(g.Summary, want) || !strings.Contains(out, "would block") || !strings.Contains(out, want) {
		t.Errorf("does not say it would block (%s):\nsummary %s\nout %s", want, g.Summary, out)
	}
}

func TestVulnGateLintRulesOptsInEarly(t *testing.T) {
	_, _, err := judge(t, vulnPromoteInput{Policy: enforcePolicy(), Release: sbomRelease(true), Grades: pinnedVulnGrades(warningPin, map[string]string{lint.RuleVulnGate: "error"})}, blockingSource())
	if got := vulnRefusalReason(t, err); got != reasonVulnGateBlocked {
		t.Fatalf("{vuln-gate: error} did not gate: %s", got)
	}
	gates, _, err := judge(t, vulnPromoteInput{Policy: enforcePolicy(), Release: sbomRelease(true), Grades: pinnedVulnGrades(gatingPin, map[string]string{lint.RuleVulnGate: "warn"})}, blockingSource())
	if err != nil || gateNamed(gates, vulnGateGateName).Details["outcome"] != "audit" {
		t.Fatalf("{vuln-gate: warn} still gated: %v %+v", err, gates)
	}
}

func TestVulnGateOverrideIsRecorded(t *testing.T) {
	gates, out, err := judge(t, vulnPromoteInput{Policy: enforcePolicy(), Release: sbomRelease(true), Grades: pinnedVulnGrades(gatingPin, nil), Override: "INC-42: the fix ships next"}, blockingSource())
	if err != nil {
		t.Fatalf("override refused: %v", err)
	}
	g := gateNamed(gates, vulnGateGateName)
	ov, _ := g.Details["override"].(map[string]any)
	if g.Details["outcome"] != "overridden" || ov["by"] != "alice" || ov["reason"] != "INC-42: the fix ships next" {
		t.Fatalf("override not recorded: %+v", g)
	}
	if ids, _ := ov["findings"].([]string); len(ids) != 1 || ids[0] != "CVE-2026-1234" {
		t.Errorf("override records findings %v, want [CVE-2026-1234]", ov["findings"])
	}
	if !strings.Contains(g.Summary, "OVERRIDDEN by alice") || !strings.Contains(out, "overridden by alice") {
		t.Errorf("summary %q / out %q", g.Summary, out)
	}
}

func TestVulnGateUnscannedAndStaleAreNeverAPass(t *testing.T) {
	pol := enforcePolicy()
	grades := pinnedVulnGrades(gatingPin, nil)
	never := fakeVulnSource{scans: []vulnscan.ArtifactScan{{Artifact: "api", SBOMDigest: "sha256:x", Reason: "never scanned"}}}
	if _, _, err := judge(t, vulnPromoteInput{Policy: pol, Release: sbomRelease(true), Grades: grades}, never); vulnRefusalReason(t, err) != reasonVulnGateUnscanned {
		t.Fatalf("an artifact never scanned: %v", err)
	}
	stale := fakeVulnSource{scans: []vulnscan.ArtifactScan{{Artifact: "api", SBOMDigest: "sha256:x", Latest: scanWith(vulnTestNow.Add(-10 * 24 * time.Hour))}}}
	_, _, err := judge(t, vulnPromoteInput{Policy: pol, Release: sbomRelease(true), Grades: grades}, stale)
	if vulnRefusalReason(t, err) != reasonVulnGateUnscanned || !strings.Contains(err.Error(), "older than 5 days") {
		t.Fatalf("a stale database: %v", err)
	}
	// No findings source at all (a ledger forge cannot read findings from).
	if _, _, err := judge(t, vulnPromoteInput{Policy: pol, Release: sbomRelease(true), Grades: grades}, nil); vulnRefusalReason(t, err) != reasonVulnGateUnscanned {
		t.Fatalf("no source: %v", err)
	}
}

func TestVulnGateCleanReleasePasses(t *testing.T) {
	var fetched bool
	src := fakeVulnSource{fetched: &fetched, scans: []vulnscan.ArtifactScan{{Artifact: "api", SBOMDigest: "sha256:x", Latest: scanWith(vulnTestNow.Add(-time.Hour))}}}
	gates, _, err := judge(t, vulnPromoteInput{Policy: enforcePolicy(), Release: sbomRelease(true), Grades: pinnedVulnGrades(gatingPin, nil)}, src)
	if err != nil {
		t.Fatal(err)
	}
	if g := gateNamed(gates, vulnGateGateName); g == nil || g.Status != release.GateStatusPassed || g.Details["verdict"] != "pass" {
		t.Fatalf("recorded %+v", g)
	}
	if !fetched {
		t.Error("a gating enforce did not let the source refresh the database")
	}
	if g := gateNamed(gates, sbomGateName); g == nil || g.Status != release.GateStatusPassed {
		t.Errorf("provenance_sbom: %+v", g)
	}
}

func TestVulnGateAuditNeverFetchesOrRefuses(t *testing.T) {
	var fetched bool
	src := blockingSource()
	src.fetched = &fetched
	gates, _, err := judge(t, vulnPromoteInput{Policy: release.DefaultDeliveryPolicy(), Release: sbomRelease(true), Grades: pinnedVulnGrades(gatingPin, nil)}, src)
	if err != nil {
		t.Fatalf("audit refused: %v", err)
	}
	if fetched {
		t.Error("an audit gate downloaded the database")
	}
	if g := gateNamed(gates, vulnGateGateName); g == nil || g.Details["outcome"] != "audit" || !strings.Contains(g.Summary, "mode audit") {
		t.Fatalf("recorded %+v", g)
	}
}

func TestSBOMRequiredIsGradedWarnFirst(t *testing.T) {
	pol := release.DefaultDeliveryPolicy()
	pol.ProvenanceSBOM.Mode = release.DeliveryEnforce // declared; the default is audit
	bare := sbomRelease(false)
	gates, out, err := judge(t, vulnPromoteInput{Policy: pol, Release: bare, Grades: pinnedVulnGrades(warningPin, nil)}, nil)
	if err != nil {
		t.Fatalf("sbom-required gated below its GatesFrom: %v", err)
	}
	g := gateNamed(gates, sbomGateName)
	if g == nil || g.Details["outcome"] != "audit" || !strings.Contains(out, "skipped: --no-sbom") {
		t.Fatalf("recorded %+v\n%s", g, out)
	}
	_, _, err = judge(t, vulnPromoteInput{Policy: pol, Release: bare, Grades: pinnedVulnGrades(gatingPin, nil)}, nil)
	if got := vulnRefusalReason(t, err); got != reasonSBOMMissing {
		t.Fatalf("reason %s, want %s", got, reasonSBOMMissing)
	}
	gates, _, err = judge(t, vulnPromoteInput{Policy: pol, Release: bare, Grades: pinnedVulnGrades(gatingPin, nil), Override: "hotfix"}, nil)
	if err != nil || gateNamed(gates, sbomGateName).Details["outcome"] != "overridden" {
		t.Fatalf("override: %v %+v", err, gates)
	}
}

// The schema default (audit, owner 2026-11-20) never refuses a release with
// no SBOM, at any pin: it records what enforce would have refused.
func TestSBOMRequiredDefaultAuditsAtAnyPin(t *testing.T) {
	gates, _, err := judge(t, vulnPromoteInput{Policy: release.DefaultDeliveryPolicy(), Release: sbomRelease(false), Grades: pinnedVulnGrades(gatingPin, nil)}, nil)
	if err != nil {
		t.Fatalf("the default provenance_sbom refused: %v", err)
	}
	if g := gateNamed(gates, sbomGateName); g == nil || g.Details["mode"] != "audit" || g.Details["outcome"] != "audit" {
		t.Fatalf("recorded %+v", g)
	}
}

// A release an older forge cut has no SBOM refs: nothing is recorded and
// nothing refused, whatever the pin, so no existing project's rollback or
// re-promotion of a pre-SBOM release can break.
func TestPreSBOMReleaseIsNeverRefused(t *testing.T) {
	old := &release.Release{Version: "v1.2.0", Provenance: &release.Provenance{ForgeVersion: "v1.8.0"}, Artifacts: map[string]release.Artifact{
		"api": {Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{release.SharedVariant: "sha256:" + strings.Repeat("b", 64)}},
	}}
	gates, _, err := judge(t, vulnPromoteInput{Policy: release.DefaultDeliveryPolicy(), Release: old, Grades: pinnedVulnGrades(gatingPin, nil)}, nil)
	if err != nil || len(gates) != 0 {
		t.Fatalf("default policy over a pre-SBOM release: %v %+v", err, gates)
	}
	gates, _, err = judge(t, vulnPromoteInput{Policy: enforcePolicy(), Release: old, Grades: pinnedVulnGrades(gatingPin, nil)}, nil)
	if err != nil {
		t.Fatalf("enforce over a pre-SBOM release refused: %v", err)
	}
	if g := gateNamed(gates, vulnGateGateName); g == nil || g.Details["outcome"] != "not-applicable" {
		t.Fatalf("recorded %+v", gates)
	}
	// A release with no provenance at all (recorded before it existed).
	old.Provenance = nil
	if gates, _, err := judge(t, vulnPromoteInput{Policy: release.DefaultDeliveryPolicy(), Release: old, Grades: pinnedVulnGrades(gatingPin, nil)}, nil); err != nil || len(gates) != 0 {
		t.Fatalf("no provenance: %v %+v", err, gates)
	}
}

func TestVulnGateRefusesACallerGateOfItsName(t *testing.T) {
	_, _, err := judge(t, vulnPromoteInput{Policy: enforcePolicy(), Release: sbomRelease(true), Grades: pinnedVulnGrades(gatingPin, nil),
		Gates: []release.Gate{{Name: vulnGateGateName, Status: release.GateStatusPassed}}}, blockingSource())
	if err == nil || !strings.Contains(err.Error(), "records its own verdict") {
		t.Fatalf("a forged vuln_gate gate was accepted: %v", err)
	}
}

func TestVulnGateOffJudgesNothing(t *testing.T) {
	pol := release.DefaultDeliveryPolicy()
	pol.VulnGate.Mode, pol.ProvenanceSBOM.Mode = release.DeliveryOff, release.DeliveryOff
	if gates, _, err := judge(t, vulnPromoteInput{Policy: pol, Release: sbomRelease(false), Grades: pinnedVulnGrades(gatingPin, nil)}, blockingSource()); err != nil || len(gates) != 0 {
		t.Fatalf("%v %+v", err, gates)
	}
}

// The machine ledger's source end to end: the SBOM a cut stored, scanned by
// (fake) grype, judged and refused.
func TestMachineVulnSourceFeedsTheGate(t *testing.T) {
	h := newVulnHarness(t)
	h.cut("v1.7.0", false)
	rel, err := h.store.Release("v1.7.0")
	if err != nil {
		t.Fatal(err)
	}
	src := machineVulnSource{deps: h.deps, store: h.store, log: &bytes.Buffer{}}
	_, _, err = judge(t, vulnPromoteInput{Policy: enforcePolicy(), Release: rel, Grades: pinnedVulnGrades(gatingPin, nil)}, src)
	if vulnRefusalReason(t, err) != reasonVulnGateBlocked || !strings.Contains(err.Error(), "CVE-2026-4738") {
		t.Fatalf("%v", err)
	}
	if h.scans != 1 {
		t.Errorf("scanned %d times, want 1 (the stored SBOM, never scanned before)", h.scans)
	}
}

// ─── The cut ────────────────────────────────────────────────────────────────

// cutHarness points the cut's scan at a vulnHarness, in a project pinned at
// pin.
func cutHarness(t *testing.T, pin string, dbOnDisk bool) (*vulnHarness, string) {
	t.Helper()
	h := newVulnHarness(t)
	projectDir := h.deps.projectDir()
	if err := os.WriteFile(filepath.Join(projectDir, "forge.yaml"), []byte("name: vuln-cli-test\nforge_version: "+pin+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if dbOnDisk {
		putVulnDBFile(t, h)
	}
	prev := cutVulnDeps
	cutVulnDeps = func() vulnDeps { return h.deps }
	t.Cleanup(func() { cutVulnDeps = prev })
	return h, projectDir
}

// putVulnDBFile puts a database file where grype keeps it, so the cut and
// the gate step see a machine that has pulled one.
func putVulnDBFile(t *testing.T, h *vulnHarness) {
	t.Helper()
	dir, _ := h.deps.dbDir()
	if err := os.MkdirAll(filepath.Join(dir, "grype", "6"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "grype", "6", "vulnerability.db"), []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func cutRelease(t *testing.T, h *vulnHarness) *release.Release {
	t.Helper()
	h.cut("v1.7.0", false)
	rel, err := h.store.Release("v1.7.0")
	if err != nil {
		t.Fatal(err)
	}
	return rel
}

func TestCutPrintsTheAdvisoryVerdict(t *testing.T) {
	h, projectDir := cutHarness(t, warningPin, true)
	pol := enforcePolicy()
	var log bytes.Buffer
	v := scanReleaseAtCut(context.Background(), cutVulnInput{ProjectDir: projectDir, Env: "prod", Entities: &KCLEntities{DeliveryPolicy: &pol}, Release: cutRelease(t, h), Log: &log})
	var out bytes.Buffer
	printCutVulnVerdict(&out, v)
	got := out.String()
	for _, want := range []string{
		"[build] vulnerabilities: 1 critical, 3 high, 3 medium, 0 low (3 fixable, 0 KEV)",
		"vuln_gate (prod, enforce): would block: CVE-2026-4738 in zlib",
		"new in " + warningPin + "; gates from " + gatingPin,
		"lint.rules: {vuln-gate: error}",
		"→ forge vuln show prod CVE-2026-4738",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("cut output lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(log.String(), "scanning 1 SBOM(s)") {
		t.Errorf("no progress line: %s", log.String())
	}
	if h.listingHit != 0 {
		t.Errorf("a fresh database was refreshed at the cut (%d listing reads)", h.listingHit)
	}
}

func TestCutSaysWhenItGatesAndForAControlPlaneEnv(t *testing.T) {
	h, projectDir := cutHarness(t, gatingPin, true)
	pol := enforcePolicy()
	v := scanReleaseAtCut(context.Background(), cutVulnInput{ProjectDir: projectDir, Env: "prod", Hosted: true, Entities: &KCLEntities{DeliveryPolicy: &pol}, Release: cutRelease(t, h)})
	var out bytes.Buffer
	printCutVulnVerdict(&out, v)
	if !strings.Contains(out.String(), "blocks: CVE-2026-4738") || !strings.Contains(out.String(), reasonVulnGateBlocked) || !strings.Contains(out.String(), "advisory: the env's control plane") {
		t.Errorf("%s", out.String())
	}
}

// The cut never downloads the first database: it says how to get one.
func TestCutWithNoDatabaseSaysNotScanned(t *testing.T) {
	h, projectDir := cutHarness(t, warningPin, false)
	pol := release.DefaultDeliveryPolicy()
	v := scanReleaseAtCut(context.Background(), cutVulnInput{ProjectDir: projectDir, Env: "staging", Entities: &KCLEntities{DeliveryPolicy: &pol}, Release: cutRelease(t, h)})
	var out bytes.Buffer
	printCutVulnVerdict(&out, v)
	if !strings.Contains(out.String(), "not scanned: no vulnerability database on this machine; `forge vuln db pull`") ||
		!strings.Contains(out.String(), "vuln_gate (staging, audit): could not determine") {
		t.Errorf("%s", out.String())
	}
	if h.scans != 0 || h.listingHit != 0 {
		t.Errorf("scanned %d / read the listing %d times with no database on disk", h.scans, h.listingHit)
	}
}

func TestCutJudgesNothingWhenThePolicySaysOff(t *testing.T) {
	h, projectDir := cutHarness(t, warningPin, true)
	for _, mut := range []func(*release.ShapeDeliveryPolicy){
		func(p *release.ShapeDeliveryPolicy) { p.VulnGate.Mode = release.DeliveryOff },
		func(p *release.ShapeDeliveryPolicy) {
			p.ProvenanceSBOM.Mode = release.DeliveryOff
			p.VulnGate.Mode = release.DeliveryAudit
		},
	} {
		pol := release.DefaultDeliveryPolicy()
		mut(&pol)
		if v := scanReleaseAtCut(context.Background(), cutVulnInput{ProjectDir: projectDir, Env: "prod", Entities: &KCLEntities{DeliveryPolicy: &pol}, Release: sbomRelease(true)}); v != nil {
			t.Errorf("judged %+v", v)
		}
	}
	if h.scans != 0 {
		t.Errorf("scanned %d times", h.scans)
	}
}

// ─── forge ci vuln-sbom ─────────────────────────────────────────────────────

func runCIVulnSBOMForTest(t *testing.T, h *vulnHarness, pin string) (string, string, int) {
	t.Helper()
	return runCIVulnSBOMPullForTest(t, h, pin, false)
}

func runCIVulnSBOMPullForTest(t *testing.T, h *vulnHarness, pin string, pull bool) (string, string, int) {
	t.Helper()
	prev := vulnSBOMSyft
	vulnSBOMSyft = func(_ context.Context, bin string, args []string) ([]byte, error) {
		if bin != "/fake/syft" || args[0] != "scan" || !strings.HasPrefix(args[1], "dir:") {
			t.Errorf("unexpected syft %s %v", bin, args)
		}
		return []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6","components":[]}`), nil
	}
	t.Cleanup(func() { vulnSBOMSyft = prev })
	var out, errOut bytes.Buffer
	cfg := &config.ProjectConfig{Name: "demo", ForgeVersion: pin}
	err := runCIVulnSBOM(context.Background(), &out, &errOut, h.deps, cfg, t.TempDir(), false, pull)
	code := 0
	var ec *exitCodeError
	var ecv exitCodeError
	switch {
	case errors.As(err, &ec):
		code = ec.code
	case errors.As(err, &ecv):
		code = ecv.code
	case err != nil:
		t.Fatalf("unexpected error: %v", err)
	}
	return out.String(), errOut.String(), code
}

func TestCIVulnSBOMIsGradedThroughTheRollout(t *testing.T) {
	h := newVulnHarness(t)
	putVulnDBFile(t, h)
	out, _, code := runCIVulnSBOMForTest(t, h, warningPin)
	if code != 0 || !strings.Contains(out, "would block: CVE-2026-4738") || !strings.Contains(out, "gates from "+gatingPin) {
		t.Fatalf("below GatesFrom: exit %d\n%s", code, out)
	}
	out, _, code = runCIVulnSBOMForTest(t, h, gatingPin)
	if code != exitWrong || !strings.Contains(out, "BLOCK") {
		t.Fatalf("at GatesFrom: exit %d\n%s", code, out)
	}
}

func TestCIVulnSBOMSkipsCleanlyWithoutADatabaseWhileItWarns(t *testing.T) {
	h := newVulnHarness(t) // no database file on disk
	_, errOut, code := runCIVulnSBOMForTest(t, h, warningPin)
	if code != cigate.VulnSBOMAllowedSkipExit || !strings.Contains(errOut, "does not gate this project yet") {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if h.scans != 0 || h.listingHit != 0 {
		t.Errorf("fetched or scanned with no database while the rule warns: scans %d listing %d", h.scans, h.listingHit)
	}
	h.built = time.Time{} // grype reports no database; the listing is offline
	_, _, code = runCIVulnSBOMForTest(t, h, gatingPin)
	if code != cigate.VulnSBOMSkipExit {
		t.Fatalf("a gating rule that could not scan exited %d, want %d (a failing SKIP)", code, cigate.VulnSBOMSkipExit)
	}
}

// --pull is what a fresh CI runner needs: with no database on disk and a rule
// that does not gate, the step fetches and scans instead of skipping, and
// the skip names the flag.
func TestCIVulnSBOMPullScansWithoutADatabaseWhileItWarns(t *testing.T) {
	h := newVulnHarness(t) // no database file on disk
	_, errOut, code := runCIVulnSBOMForTest(t, h, warningPin)
	if code != cigate.VulnSBOMAllowedSkipExit || !strings.Contains(errOut, "forge ci vuln-sbom --pull") {
		t.Fatalf("without --pull: exit %d, want the skip naming --pull: %s", code, errOut)
	}
	out, errOut, code := runCIVulnSBOMPullForTest(t, h, warningPin, true)
	if code != 0 || !strings.Contains(out, "would block: CVE-2026-4738") {
		t.Fatalf("with --pull: exit %d\n%s\n%s", code, out, errOut)
	}
	if h.scans == 0 {
		t.Error("--pull did not scan")
	}
}

// The SBOM scan honors every well-formed ci.vuln_scan.exemptions entry,
// whatever its namespace (a CVE included), and refuses the malformed ones the
// shared contract refuses.
func TestSBOMScanExemptions(t *testing.T) {
	accepted, malformed := sbomScanExemptions([]config.CIVulnExemption{
		{ID: "CVE-2026-4738", Reason: "zlib is never fed untrusted input", Expires: "2026-12-31"},
		{ID: "GHSA-VFJ7-8CJW-P6XM", Reason: "dev only", Expires: "2026-12-31"},
		{ID: "GO-2026-4887", Reason: "not called", Expires: "2026-12-31"},
		{ID: "", Reason: "x", Expires: "2026-12-31"},
		{ID: "not an id", Reason: "x", Expires: "2026-12-31"},
		{ID: "CVE-2026-1", Expires: "2026-12-31"},
		{ID: "CVE-2026-2", Reason: "x", Expires: "31/12/2026"},
	})
	var ids []string
	for _, a := range accepted {
		ids = append(ids, a.ID)
	}
	if strings.Join(ids, " ") != "CVE-2026-4738 GHSA-vfj7-8cjw-p6xm GO-2026-4887" {
		t.Errorf("accepted %v", ids)
	}
	if len(malformed) != 4 {
		t.Errorf("malformed %q, want the blank id, the non-id, the missing reason and the bad expiry", malformed)
	}
}

func runCIVulnSBOMWith(t *testing.T, h *vulnHarness, pin string, ex []config.CIVulnExemption) (string, int) {
	t.Helper()
	prev := vulnSBOMSyft
	vulnSBOMSyft = func(context.Context, string, []string) ([]byte, error) {
		return []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6","components":[]}`), nil
	}
	t.Cleanup(func() { vulnSBOMSyft = prev })
	cfg := &config.ProjectConfig{Name: "demo", ForgeVersion: pin}
	cfg.CI.VulnScan.Exemptions = ex
	var out, errOut bytes.Buffer
	err := runCIVulnSBOM(context.Background(), &out, &errOut, h.deps, cfg, t.TempDir(), false, false)
	var ec *exitCodeError
	var ecv exitCodeError
	switch {
	case err == nil:
		return out.String(), 0
	case errors.As(err, &ec):
		return out.String() + err.Error(), ec.code
	case errors.As(err, &ecv):
		return out.String() + err.Error(), ecv.code
	}
	t.Fatalf("unexpected error: %v", err)
	return "", 0
}

// A CVE entry in the shared list exempts the grype finding it names (that is
// what accepting CVE ids is for), and a malformed entry fails the step once
// the rule gates — a note before.
func TestCIVulnSBOMHonorsTheSharedExemptions(t *testing.T) {
	h := newVulnHarness(t)
	putVulnDBFile(t, h)
	cve := config.CIVulnExemption{ID: "CVE-2026-4738", Reason: "zlib is never fed untrusted input", Expires: "2026-12-31"}
	if out, code := runCIVulnSBOMWith(t, h, gatingPin, []config.CIVulnExemption{cve}); code != 0 {
		t.Fatalf("a CVE exemption did not exempt the finding: exit %d\n%s", code, out)
	}
	bad := config.CIVulnExemption{ID: "CVE-2026-9", Expires: "2026-12-31"}
	if out, code := runCIVulnSBOMWith(t, h, gatingPin, []config.CIVulnExemption{cve, bad}); code != exitWrong || !strings.Contains(out, "CVE-2026-9 has no `reason`") {
		t.Fatalf("a malformed entry under a gating rule: exit %d\n%s", code, out)
	}
	if out, code := runCIVulnSBOMWith(t, h, warningPin, []config.CIVulnExemption{cve, bad}); code != 0 || !strings.Contains(out, "exemption not honored") {
		t.Fatalf("a malformed entry while the rule warns: exit %d\n%s", code, out)
	}
}

// A version-only release names nothing to scan: under a graded enforce that
// is "could not determine", never the pass "0 of 0 scanned" would compute.
func TestVulnGateVersionOnlyReleaseIsUnscannedNotAPass(t *testing.T) {
	rel := &release.Release{Version: "v2.0.0", RecordOnly: true, Provenance: &release.Provenance{ForgeVersion: gatingPin}}
	_, _, err := judge(t, vulnPromoteInput{Policy: enforcePolicy(), Release: rel, Grades: pinnedVulnGrades(gatingPin, nil)}, fakeVulnSource{})
	if vulnRefusalReason(t, err) != reasonVulnGateUnscanned || !strings.Contains(err.Error(), "names no artifact an SBOM describes") {
		t.Fatalf("%v", err)
	}
	gates, _, err := judge(t, vulnPromoteInput{Policy: enforcePolicy(), Release: rel, Grades: pinnedVulnGrades(warningPin, nil)}, fakeVulnSource{})
	if err != nil || gateNamed(gates, vulnGateGateName).Details["outcome"] != "audit" {
		t.Fatalf("below GatesFrom: %v %+v", err, gates)
	}
}

// F4's freshness rule: findings from a database older than five days are not
// a verdict, and stale outranks block. Under a graded enforce that is
// vuln_gate_unscanned, naming what the old data reported; never blocked, and
// never a pass.
func TestVulnGateStaleOutranksBlock(t *testing.T) {
	stale := fakeVulnSource{scans: []vulnscan.ArtifactScan{{Artifact: "api", SBOMDigest: "sha256:x", Latest: scanWith(vulnTestNow.Add(-6*24*time.Hour), criticalFinding())}}}
	_, _, err := judge(t, vulnPromoteInput{Policy: enforcePolicy(), Release: sbomRelease(true), Grades: pinnedVulnGrades(gatingPin, nil)}, stale)
	if vulnRefusalReason(t, err) != reasonVulnGateUnscanned || !strings.Contains(err.Error(), "older than 5 days") || !strings.Contains(err.Error(), "CVE-2026-1234") {
		t.Fatalf("%v", err)
	}
	gates, _, err := judge(t, vulnPromoteInput{Policy: enforcePolicy(), Release: sbomRelease(true), Grades: pinnedVulnGrades(warningPin, nil)}, stale)
	g := gateNamed(gates, vulnGateGateName)
	if err != nil || g.Details["determined"] != false || g.Details["verdict"] != "stale" || g.Details["outcome"] != "audit" {
		t.Fatalf("below GatesFrom: %v %+v", err, g)
	}
	// A database four days old is still a verdict.
	fresh := fakeVulnSource{scans: []vulnscan.ArtifactScan{{Artifact: "api", SBOMDigest: "sha256:x", Latest: scanWith(vulnTestNow.Add(-4*24*time.Hour), criticalFinding())}}}
	if _, _, err := judge(t, vulnPromoteInput{Policy: enforcePolicy(), Release: sbomRelease(true), Grades: pinnedVulnGrades(gatingPin, nil)}, fresh); vulnRefusalReason(t, err) != reasonVulnGateBlocked {
		t.Fatalf("a 4-day-old database: %v", err)
	}
}
