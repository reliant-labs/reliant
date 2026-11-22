package vulnscan

import (
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

func defaultGate() release.ShapeVulnGate { return release.DefaultDeliveryPolicy().VulnGate }

func TestJudge(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	f := func(sev Severity, fix string, kev bool) Finding {
		return Finding{ID: "CVE-2026-1", Aliases: []string{"GHSA-aaaa-bbbb-cccc"}, Severity: sev, FixState: fix, KEV: kev, Package: Package{Name: "p", Version: "1"}}
	}
	exempt := func(id, expires string) release.ShapeVulnGate {
		g := defaultGate()
		g.Exemptions = []release.ShapeVulnExempt{{ID: id, Reason: "not reachable", Expires: expires}}
		return g
	}
	strict := defaultGate()
	strict.FixableOnly = false
	strict.BlockSeverity = "high"
	strict.WarnSeverity = "low"
	noKEV := defaultGate()
	noKEV.BlockKEV = false
	off := defaultGate()
	off.BlockSeverity, off.WarnSeverity = "none", "none"

	cases := []struct {
		name       string
		gate       release.ShapeVulnGate
		f          Finding
		want       Action
		wantExempt bool
	}{
		{"critical fixed blocks", defaultGate(), f(SeverityCritical, FixStateFixed, false), ActionBlock, false},
		{"critical not-fixed only warns (fixable_only)", defaultGate(), f(SeverityCritical, FixStateNotFixed, false), ActionWarn, false},
		{"critical wont-fix only warns", defaultGate(), f(SeverityCritical, FixStateWontFix, false), ActionWarn, false},
		{"critical unknown fix state only warns", defaultGate(), f(SeverityCritical, "", false), ActionWarn, false},
		{"high fixed warns", defaultGate(), f(SeverityHigh, FixStateFixed, false), ActionWarn, false},
		{"medium is nothing", defaultGate(), f(SeverityMedium, FixStateFixed, false), ActionNone, false},
		{"KEV blocks at any severity, fixed or not", defaultGate(), f(SeverityLow, FixStateNotFixed, true), ActionBlock, false},
		{"KEV without block_kev falls to severity", noKEV, f(SeverityLow, FixStateNotFixed, true), ActionNone, false},
		{"unknown severity is critical", defaultGate(), f(SeverityUnknown, FixStateFixed, false), ActionBlock, false},
		{"negligible below a low warn", strict, f(SeverityNegligible, FixStateFixed, false), ActionNone, false},
		{"fixable_only off: not-fixed high blocks", strict, f(SeverityHigh, FixStateNotFixed, false), ActionBlock, false},
		{"low warns at warn low", strict, f(SeverityLow, "", false), ActionWarn, false},
		{"bands off judge nothing but KEV", off, f(SeverityCritical, FixStateFixed, false), ActionNone, false},
		{"exemption by id", exempt("CVE-2026-1", "2026-12-31"), f(SeverityCritical, FixStateFixed, true), ActionNone, true},
		{"exemption by alias (case-insensitive)", exempt("ghsa-aaaa-bbbb-cccc", "2026-12-31"), f(SeverityCritical, FixStateFixed, false), ActionNone, true},
		{"exemption good through its expiry date", exempt("CVE-2026-1", "2026-11-20"), f(SeverityCritical, FixStateFixed, false), ActionNone, true},
		{"expired exemption exempts nothing", exempt("CVE-2026-1", "2026-11-19"), f(SeverityCritical, FixStateFixed, false), ActionBlock, false},
		{"other advisory's exemption exempts nothing", exempt("CVE-2026-2", "2026-12-31"), f(SeverityCritical, FixStateFixed, false), ActionBlock, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ex := Judge(tc.gate, tc.f, now)
			if got != tc.want || (ex != nil) != tc.wantExempt {
				t.Fatalf("Judge = %s (exemption %v), want %s (exempt %v)", got, ex, tc.want, tc.wantExempt)
			}
		})
	}
}

func TestEvaluateVerdicts(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	block := Finding{ID: "CVE-B", Severity: SeverityCritical, FixState: FixStateFixed, Package: Package{Name: "a"}}
	block2 := Finding{ID: "CVE-B", Severity: SeverityCritical, FixState: FixStateFixed, Package: Package{Name: "b"}}
	warn := Finding{ID: "CVE-W", Severity: SeverityHigh, Package: Package{Name: "c"}}
	full := Coverage{Artifacts: 2, WithSBOM: 2, Scanned: 2}
	partial := Coverage{Artifacts: 2, WithSBOM: 1, Scanned: 1, Unscanned: []UnscannedArtifact{{Artifact: "web", Reason: "no SBOM"}}}
	gateOff := defaultGate()
	gateOff.Mode = release.DeliveryOff
	enforce := defaultGate()
	enforce.Mode = release.DeliveryEnforce

	cases := []struct {
		name     string
		gate     release.ShapeVulnGate
		findings []Finding
		cov      Coverage
		stale    bool
		want     Verdict
		blocking int
	}{
		{"clean", defaultGate(), nil, full, false, VerdictPass, 0},
		{"warn only", defaultGate(), []Finding{warn}, full, false, VerdictWarn, 0},
		{"block, one id listed once", enforce, []Finding{block, block2, warn}, full, false, VerdictBlock, 1},
		{"stale beats block: old data cannot establish the status, its blockers still listed", defaultGate(), []Finding{block}, full, true, VerdictStale, 1},
		{"a current block beats an unscanned artifact", defaultGate(), []Finding{block}, partial, false, VerdictBlock, 1},
		{"stale is never a pass", defaultGate(), nil, full, true, VerdictStale, 0},
		{"stale beats warn", defaultGate(), []Finding{warn}, full, true, VerdictStale, 0},
		{"unscanned artifact is never a pass", defaultGate(), []Finding{warn}, partial, false, VerdictUnscanned, 0},
		{"off judges nothing", gateOff, []Finding{block}, partial, true, VerdictOff, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ev := Evaluate(EvaluateInput{Gate: tc.gate, Findings: tc.findings, Coverage: tc.cov, Stale: tc.stale, Now: now})
			if ev.Gate.Verdict != tc.want || len(ev.Gate.Blocking) != tc.blocking {
				t.Fatalf("verdict %s blocking %v, want %s with %d", ev.Gate.Verdict, ev.Gate.Blocking, tc.want, tc.blocking)
			}
			if ev.Gate.Mode != string(tc.gate.Mode) || ev.Gate.GradedAs != ev.Gate.Mode {
				t.Errorf("mode %s graded as %s", ev.Gate.Mode, ev.Gate.GradedAs)
			}
			if ev.Gate.Blocking == nil {
				t.Errorf("blocking must encode as [], never null")
			}
			if ev.Gate.Determined != tc.want.Determined() {
				t.Errorf("determined = %v for verdict %s", ev.Gate.Determined, tc.want)
			}
		})
	}
	if VerdictStale.Determined() || VerdictUnscanned.Determined() || !VerdictBlock.Determined() {
		t.Errorf("Determined wrong")
	}
}

// An authoritative ledger's judgement wins over this rule, both ways: a
// finding the rule would block does not when the ledger says so, and one the
// rule would pass blocks when the ledger says it does; the ledger's exemption
// is the one recorded. Only the warn band stays the rule's.
func TestEvaluateWithAnAuthoritativeJudgement(t *testing.T) {
	t.Parallel()
	gate := release.DefaultDeliveryPolicy().VulnGate
	critical := Finding{ID: "CVE-2026-1", Severity: SeverityCritical, FixState: FixStateFixed, Package: Package{Name: "a", Version: "1"}}
	low := Finding{ID: "CVE-2026-2", Severity: SeverityLow, Package: Package{Name: "b", Version: "1"}}
	high := Finding{ID: "CVE-2026-3", Severity: SeverityHigh, Package: Package{Name: "c", Version: "1"}}
	exempt := &Exemption{ID: "CVE-2026-1", Reason: "accepted on the control plane", Expires: "2026-12-31"}
	cov := Coverage{Artifacts: 1, WithSBOM: 1, Scanned: 1}
	in := EvaluateInput{Gate: gate, Findings: []Finding{critical, low, high}, Coverage: cov, Now: testNow}

	if ev := Evaluate(in); ev.Gate.Verdict != VerdictBlock || len(ev.Gate.Blocking) != 1 || ev.Gate.Blocking[0] != "CVE-2026-1" {
		t.Fatalf("local rule: %+v", ev.Gate)
	}
	in.Judged = map[string]Judgement{low.Key(): {Blocking: true}}
	ev := Evaluate(in)
	if ev.Gate.Verdict != VerdictBlock || len(ev.Gate.Blocking) != 1 || ev.Gate.Blocking[0] != "CVE-2026-2" {
		t.Fatalf("authoritative: gate %+v, want only CVE-2026-2 blocking", ev.Gate)
	}
	if ev.Findings[0].Action != ActionNone || ev.Findings[2].Action != ActionWarn {
		t.Errorf("actions = %s %s %s, want none block warn", ev.Findings[0].Action, ev.Findings[1].Action, ev.Findings[2].Action)
	}
	in.Judged = map[string]Judgement{critical.Key(): {Exemption: exempt}}
	if ev := Evaluate(in); ev.Gate.Verdict != VerdictWarn || ev.Findings[0].Exemption != exempt {
		t.Fatalf("authoritative exemption: gate %+v, exemption %+v", ev.Gate, ev.Findings[0].Exemption)
	}
}
