package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/config"
)

// The npm half of the vulnerability gate mirrors the Go half: same exemption
// list, same contract (per-ID, reason, expiry, stale reporting). These tests
// pin what is npm-specific — reading the report, the severity floor, and the
// routing of one shared list to two scanners — and re-pin the limits that
// matter most for an escape hatch, on npm data.

// npmAuditFixture is `npm audit --json` from a project scaffolded by forge
// v0.1.45 with `--frontend web` (issue #754), trimmed to the entries that
// exercise every shape the parser meets:
//   - braces carries an advisory object; micromatch reaches it only through a
//     string edge ("braces"), which is NOT an advisory;
//   - GHSA-82fw-gwwq-j7x9 is filed against BOTH vitest and @vitest/mocker,
//     at different severities, and must come out as one finding;
//   - tinypool carries two advisories;
//   - fflate is below the gate's floor.
const npmAuditFixture = `{
  "auditReportVersion": 2,
  "vulnerabilities": {
    "braces": {
      "name": "braces", "severity": "high", "isDirect": false,
      "via": [{"source": 1240992, "name": "braces", "dependency": "braces",
        "title": "braces vulnerable to stack-exhaustion denial of service through deeply nested patterns",
        "url": "https://github.com/advisories/GHSA-vfj7-8cjw-p6xm", "severity": "high", "range": "<=3.0.3"}],
      "effects": ["micromatch"], "range": "*", "nodes": ["node_modules/braces"], "fixAvailable": false
    },
    "micromatch": {
      "name": "micromatch", "severity": "high", "isDirect": false,
      "via": ["braces"], "effects": ["fast-glob"], "range": ">=0.2.0", "nodes": ["node_modules/micromatch"], "fixAvailable": false
    },
    "@vitest/mocker": {
      "name": "@vitest/mocker", "severity": "moderate", "isDirect": false,
      "via": [{"source": 1193684, "name": "@vitest/mocker", "dependency": "@vitest/mocker",
        "title": "Vitest: Path Traversal / Arbitrary File Read via @vitest/mocker Redirect Mock",
        "url": "https://github.com/advisories/GHSA-82fw-gwwq-j7x9", "severity": "moderate", "range": ">=2.1.0 <4.1.11"}],
      "effects": ["vitest"], "range": "2.1.0 - 4.1.10", "nodes": ["node_modules/@vitest/mocker"]
    },
    "vitest": {
      "name": "vitest", "severity": "critical", "isDirect": true,
      "via": ["@vitest/mocker", "tinypool",
        {"source": 1193683, "name": "vitest", "dependency": "vitest",
        "title": "Vitest: Path Traversal / Arbitrary File Read via @vitest/mocker Redirect Mock",
        "url": "https://github.com/advisories/GHSA-82fw-gwwq-j7x9", "severity": "high", "range": ">=2.1.0 <4.1.11"}],
      "effects": [], "range": "0.0.95 - 4.1.10", "nodes": ["node_modules/vitest"]
    },
    "tinypool": {
      "name": "tinypool", "severity": "critical", "isDirect": false,
      "via": [
        {"source": 1241260, "name": "tinypool", "dependency": "tinypool",
          "title": "Tinypool: Prototype Pollution gadget in worker options leads to Remote Code Execution",
          "url": "https://github.com/advisories/GHSA-5gmw-xhrv-c9v3", "severity": "critical", "range": "<=2.1.0"},
        {"source": 1241261, "name": "tinypool", "dependency": "tinypool",
          "title": "Tinypool: Prototype Pollution Gadget to RCE in run() options",
          "url": "https://github.com/advisories/GHSA-85c8-ppgw-ccpr", "severity": "critical", "range": "<2.1.2"}],
      "effects": ["vitest"], "range": "<=2.1.1", "nodes": ["node_modules/tinypool"]
    },
    "fflate": {
      "name": "fflate", "severity": "moderate", "isDirect": false,
      "via": [{"source": 1164783, "name": "fflate", "dependency": "fflate",
        "title": "fflate unzipSync can enter an infinite loop when parsing malformed ZIP64 archives",
        "url": "https://github.com/advisories/GHSA-px8p-9vwx-vf98", "severity": "moderate", "range": ">=0.7.0 <0.7.5"}],
      "effects": ["@hyperdx/otel-web-session-recorder"], "range": "0.7.0 - 0.7.4", "nodes": ["node_modules/fflate"]
    }
  },
  "metadata": {"vulnerabilities": {"info": 0, "low": 0, "moderate": 2, "high": 2, "critical": 2, "total": 6}}
}`

func mustParseNPMAudit(t *testing.T, report string) []vulnFinding {
	t.Helper()
	got, err := parseNPMAuditJSON(strings.NewReader(report))
	if err != nil {
		t.Fatalf("parseNPMAuditJSON: %v", err)
	}
	return got
}

func findingIDs(fs []vulnFinding) []string {
	ids := make([]string, 0, len(fs))
	for _, f := range fs {
		ids = append(ids, f.ID)
	}
	return ids
}

// Only advisory OBJECTS are advisories. A string in `via` is an edge of the
// dependency graph ("micromatch is vulnerable through braces"); counting it
// would invent findings, and keying by package rather than advisory would
// make one exemption either too narrow (per edge) or too broad (per package).
func TestParseNPMAuditJSON_OneFindingPerAdvisory(t *testing.T) {
	t.Parallel()

	got := mustParseNPMAudit(t, npmAuditFixture)
	want := []string{"GHSA-5gmw-xhrv-c9v3", "GHSA-82fw-gwwq-j7x9", "GHSA-85c8-ppgw-ccpr", "GHSA-px8p-9vwx-vf98", "GHSA-vfj7-8cjw-p6xm"}
	if strings.Join(findingIDs(got), ",") != strings.Join(want, ",") {
		t.Fatalf("findings = %v, want exactly the 5 advisories %v", findingIDs(got), want)
	}

	byID := map[string]vulnFinding{}
	for _, f := range got {
		byID[f.ID] = f
	}
	// Filed against two packages at two severities: one finding, at the
	// higher severity, or the floor would judge it by whichever entry the
	// map happened to yield first.
	if s := byID["GHSA-82fw-gwwq-j7x9"].Severity; s != "high" {
		t.Errorf("an advisory filed against two packages must keep its highest severity; got %q, want high", s)
	}
	braces := byID["GHSA-vfj7-8cjw-p6xm"]
	if braces.Module != "braces" || braces.Range != "<=3.0.3" || braces.Severity != "high" {
		t.Errorf("braces finding lost its package/range/severity: %+v", braces)
	}
	if !strings.Contains(braces.Summary, "stack-exhaustion") || braces.URL != "https://github.com/advisories/GHSA-vfj7-8cjw-p6xm" {
		t.Errorf("braces finding lost its title or link: %+v", braces)
	}
}

// Every way of NOT getting a report must be an error. npm audit exits
// non-zero on findings, so the gate no longer reads the exit code — which
// means a parser that returned "no findings" for a report it did not
// understand would turn a broken audit into a pass.
func TestParseNPMAuditJSON_NoReportIsNotAPass(t *testing.T) {
	t.Parallel()

	for name, out := range map[string]string{
		"npm's own error report (no lockfile)": `{"error": {"code": "ENOLOCK", "summary": "This command requires an existing lockfile.", "detail": "Try creating one first with: npm i --package-lock-only"}}`,
		"the npm 6 report shape":               `{"advisories": {}, "metadata": {"vulnerabilities": {"high": 0}}}`,
		"missing vulnerability map":            `{"auditReportVersion":2}`,
		"null advisory":                        `{"auditReportVersion":2,"vulnerabilities":{"x":{"via":[null]}}}`,
		"bad advisory":                         `{"auditReportVersion":2,"vulnerabilities":{"x":{"via":[42]}}}`,
		"empty advisory":                       `{"auditReportVersion":2,"vulnerabilities":{"x":{"via":[{}]}}}`,
		"empty output":                         ``,
		"not JSON":                             `npm ERR! code ECONNREFUSED`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parseNPMAuditJSON(strings.NewReader(out))
			if err == nil {
				t.Fatalf("a report the gate cannot read must be an error, never a pass; got findings %+v", got)
			}
		})
	}

	_, err := parseNPMAuditJSON(strings.NewReader(`{"error": {"code": "ENOLOCK", "summary": "This command requires an existing lockfile."}}`))
	if err == nil || !strings.Contains(err.Error(), "ENOLOCK") || !strings.Contains(err.Error(), "lockfile") {
		t.Errorf("npm's own error must reach the user verbatim; got %v", err)
	}
}

// A clean report is a pass with zero findings, not an error.
func TestParseNPMAuditJSON_CleanReport(t *testing.T) {
	t.Parallel()

	got := mustParseNPMAudit(t, `{"auditReportVersion": 2, "vulnerabilities": {}, "metadata": {}}`)
	if len(got) != 0 {
		t.Errorf("a clean report has no findings; got %+v", got)
	}
}

// The floor stays exactly where `--audit-level=high` put it, so a project
// with no exemptions passes and fails on the same advisories it did before
// the gate learned exemptions. Below-floor advisories are reported, not
// dropped.
func TestNPMGate_SeverityFloorIsHigh(t *testing.T) {
	t.Parallel()

	res := evaluateVulnFindings(mustParseNPMAudit(t, npmAuditFixture), nil, time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC))
	below := belowNPMGate(&res)

	if got, want := strings.Join(findingIDs(res.Blocking), ","), "GHSA-5gmw-xhrv-c9v3,GHSA-82fw-gwwq-j7x9,GHSA-85c8-ppgw-ccpr,GHSA-vfj7-8cjw-p6xm"; got != want {
		t.Errorf("blocking = %s, want every high/critical advisory: %s", got, want)
	}
	if got := strings.Join(findingIDs(below), ","); got != "GHSA-px8p-9vwx-vf98" {
		t.Errorf("below the floor = %s, want only the moderate fflate advisory", got)
	}

	// A severity npm adds later must fail closed, not slip under the floor.
	if npmSeverityRank("catastrophic") < npmSeverityRank(npmGateSeverity) {
		t.Error("an unknown severity must rank at or above the floor")
	}
}

// The safety argument for the escape hatch, on npm data: an exemption
// accepts the advisory it names and NOTHING else — not the other advisories
// in the same package, not the rest of the chain.
func TestNPMGate_ExemptionIsScopedToItsID(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	res := evaluateVulnFindings(mustParseNPMAudit(t, npmAuditFixture), []config.CIVulnExemption{
		{ID: "GHSA-5gmw-xhrv-c9v3", Reason: "test-only worker pool", Expires: "2026-12-31"},
	}, now)
	belowNPMGate(&res)

	if got := strings.Join(findingIDs(res.Accepted), ","); got != "GHSA-5gmw-xhrv-c9v3" {
		t.Errorf("accepted = %s, want only the named advisory", got)
	}
	blocking := strings.Join(findingIDs(res.Blocking), ",")
	if !strings.Contains(blocking, "GHSA-85c8-ppgw-ccpr") {
		t.Errorf("the OTHER advisory in the same package (tinypool) must still block; blocking = %s", blocking)
	}
}

// The day-0 case from #754: braces has no fixed release, so a fresh Next.js
// project can only be green by accepting it. With that one entry — and
// nothing else — the scaffold's (post-fix) report passes.
func TestNPMGate_AcceptedNoFixAdvisoryPasses(t *testing.T) {
	t.Parallel()

	const afterFix = `{"auditReportVersion": 2, "vulnerabilities": {
	  "braces": {"name": "braces", "severity": "high", "via": [{"source": 1240992, "name": "braces",
	    "title": "braces vulnerable to stack-exhaustion denial of service through deeply nested patterns",
	    "url": "https://github.com/advisories/GHSA-vfj7-8cjw-p6xm", "severity": "high", "range": "<=3.0.3"}]},
	  "micromatch": {"name": "micromatch", "severity": "high", "via": ["braces"]},
	  "stylelint": {"name": "stylelint", "severity": "high", "via": ["fast-glob", "globby", "micromatch"]}
	}}`
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	res := evaluateVulnFindings(mustParseNPMAudit(t, afterFix), []config.CIVulnExemption{
		{ID: "GHSA-vfj7-8cjw-p6xm", Reason: "dev lint toolchain only", Expires: "2027-01-12"},
	}, now)
	belowNPMGate(&res)

	if len(res.Blocking) != 0 || len(res.Malformed) != 0 || len(res.StaleExemptions) != 0 {
		t.Errorf("one accepted no-fix advisory must leave a passing gate; got %+v", res)
	}
}

// GitHub prints GHSA ids with a lower-case body; people retype them in any
// case. An entry that silently matched nothing because of case would be
// worse than no entry: it LOOKS like an acceptance.
func TestNPMGate_GHSAMatchIgnoresCase(t *testing.T) {
	t.Parallel()

	res := evaluateVulnFindings([]vulnFinding{{ID: "GHSA-vfj7-8cjw-p6xm", Severity: "high"}},
		[]config.CIVulnExemption{{ID: "ghsa-VFJ7-8CJW-P6XM", Reason: "dev only", Expires: "2026-12-31"}},
		time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC))
	if len(res.Accepted) != 1 || len(res.StaleExemptions) != 0 {
		t.Errorf("a GHSA id must match regardless of case; got %+v", res)
	}
}

// One list, two scanners: each entry goes to the scanner whose ids it
// spells. Without the split, every GHSA entry would read as stale to
// govulncheck (and every GO- entry to npm), so the stale report would be
// noise on every run and stop being read.
func TestPartitionExemptions_RoutesByIDNamespace(t *testing.T) {
	t.Parallel()

	byEco, unroutable := partitionExemptions([]config.CIVulnExemption{
		{ID: "GO-2026-4887"},
		{ID: "GHSA-vfj7-8cjw-p6xm"},
		{ID: " ghsa-px8p-9vwx-vf98 "},
		{ID: "CVE-2026-93687"},
		{ID: ""},
	})

	if len(byEco[ecosystemGo]) != 1 || byEco[ecosystemGo][0].ID != "GO-2026-4887" {
		t.Errorf("go entries = %+v, want only GO-2026-4887", byEco[ecosystemGo])
	}
	if len(byEco[ecosystemNPM]) != 2 {
		t.Errorf("npm entries = %+v, want both GHSA ids", byEco[ecosystemNPM])
	}
	// A CVE alias and a blank id can accept nothing anywhere: both must
	// come back for the caller to FAIL on, each named once.
	if len(unroutable) != 2 {
		t.Fatalf("unroutable = %q, want the CVE alias and the blank id", unroutable)
	}
	if !strings.Contains(unroutable[0], "CVE-2026-93687") || !strings.Contains(unroutable[0], "GHSA-") {
		t.Errorf("an unroutable id must be named, with the id forms that DO work; got %q", unroutable[0])
	}

	// The point of routing: a GHSA entry is not stale to govulncheck.
	goRes := evaluateVulnFindings(nil, byEco[ecosystemGo], time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	for _, id := range goRes.StaleExemptions {
		if strings.HasPrefix(id, "GHSA-") {
			t.Errorf("govulncheck must never judge a GHSA entry; it reported %s stale", id)
		}
	}
}

// Exemptions belong to the project, not to one frontend: an advisory in two
// frontends is one finding naming both, and an entry is stale only when no
// frontend carries its advisory any more.
func TestMergeNPMFindings_OneFindingAcrossFrontends(t *testing.T) {
	t.Parallel()

	all := map[string]*vulnFinding{}
	mergeNPMFindings(all, "web", []vulnFinding{{ID: "GHSA-vfj7-8cjw-p6xm", Module: "braces", Severity: "high"}})
	mergeNPMFindings(all, "admin", []vulnFinding{{ID: "GHSA-vfj7-8cjw-p6xm", Module: "braces", Severity: "high"}})

	if len(all) != 1 {
		t.Fatalf("want one finding across frontends, got %d", len(all))
	}
	f := all["GHSA-vfj7-8cjw-p6xm"]
	if strings.Join(f.In, ",") != "web,admin" {
		t.Errorf("the finding must name every frontend carrying it; got %v", f.In)
	}
	if !strings.Contains(f.label(), "in web, admin") {
		t.Errorf("the report line must say where the advisory is; got %q", f.label())
	}
}

// writeFakeNPM puts an `npm` on PATH that prints report and exits with code,
// the way the real one does: non-zero whenever it found anything.
func writeFakeNPM(t *testing.T, report string, code int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake npm is a POSIX shell script")
	}
	bin := t.TempDir()
	reportPath := filepath.Join(bin, "report.json")
	if err := os.WriteFile(reportPath, []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat '" + reportPath + "'\nexit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// End to end through ciRunNPMAudit, against an npm that behaves like the
// real one: it exits 1 because it found advisories. The verdict must come
// from the report and the exemptions, not from that exit code.
func TestCIRunNPMAudit_VerdictComesFromTheReport(t *testing.T) {
	writeFakeNPM(t, npmAuditFixture, 1)
	cfg := &config.ProjectConfig{Frontends: []config.FrontendConfig{
		config.FrontendConfig{Name: "web"}.WithDir(t.TempDir()),
	}}
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	reviewed := func(id string) config.CIVulnExemption {
		return config.CIVulnExemption{ID: id, Reason: "reviewed", Expires: "2026-12-31"}
	}

	if _, err := ciRunNPMAudit(context.Background(), cfg, nil, now); err == nil {
		t.Error("high and critical advisories with no exemption must fail the gate")
	}

	allBlocking := []config.CIVulnExemption{
		reviewed("GHSA-5gmw-xhrv-c9v3"), reviewed("GHSA-82fw-gwwq-j7x9"),
		reviewed("GHSA-85c8-ppgw-ccpr"), reviewed("GHSA-vfj7-8cjw-p6xm"),
	}
	what, err := ciRunNPMAudit(context.Background(), cfg, allBlocking, now)
	if err != nil {
		t.Fatalf("with every high/critical advisory accepted the gate must pass despite npm's exit 1; got %v", err)
	}
	if !strings.Contains(what, "4 accepted exemption(s)") {
		t.Errorf("the success line must count the accepted exemptions; got %q", what)
	}

	expired := append(allBlocking[:3:3], config.CIVulnExemption{ID: "GHSA-vfj7-8cjw-p6xm", Reason: "reviewed", Expires: "2026-01-31"})
	if _, err := ciRunNPMAudit(context.Background(), cfg, expired, now); err == nil {
		t.Error("an expired npm exemption must stop accepting, exactly as on the Go side")
	}
}

// npm's own failure report (here: no lockfile) is a failed gate, never a
// clean one — the case where "exit code ignored" could most easily turn into
// "pass".
func TestCIRunNPMAudit_NPMErrorReportFails(t *testing.T) {
	writeFakeNPM(t, `{"error": {"code": "ENOLOCK", "summary": "This command requires an existing lockfile."}}`, 1)
	cfg := &config.ProjectConfig{Frontends: []config.FrontendConfig{
		config.FrontendConfig{Name: "web"}.WithDir(t.TempDir()),
	}}

	_, err := ciRunNPMAudit(context.Background(), cfg, nil, time.Now())
	if err == nil || !strings.Contains(err.Error(), "ENOLOCK") {
		t.Errorf("an audit npm could not run must fail and say why; got %v", err)
	}
}
