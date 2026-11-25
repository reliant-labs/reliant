package vulnscan

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseGrypeSample(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/grype-sample.json")
	if err != nil {
		t.Fatal(err)
	}
	scan, err := ParseGrype(data, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if scan.Schema != ScanSchema || scan.Scanner != "grype" || scan.ScannerVersion != "v0.122.1" || scan.DBSchema != "v6.1.10" {
		t.Errorf("record = %s %s %s %s", scan.Schema, scan.Scanner, scan.ScannerVersion, scan.DBSchema)
	}
	if want := time.Date(2026, 11, 19, 18, 41, 5, 0, time.UTC); !scan.DBBuiltAt.Equal(want) {
		t.Errorf("db built = %s, want %s", scan.DBBuiltAt, want)
	}
	if scan.ScannedAt.IsZero() {
		t.Errorf("scanned_at not taken from the descriptor timestamp")
	}
	if len(scan.Findings) != 7 {
		t.Fatalf("findings = %d, want 7", len(scan.Findings))
	}
	byPkg := map[string]Finding{}
	for _, f := range scan.Findings {
		byPkg[f.Package.Name] = f
	}

	z := byPkg["zlib"]
	if scan.Findings[0].ID != "CVE-2026-4738" {
		t.Errorf("strongest finding first: got %s", scan.Findings[0].ID)
	}
	if z.Severity != SeverityCritical || !z.Fixable() || strings.Join(z.FixedIn, ",") != "1.4-r0" {
		t.Errorf("zlib = %+v", z)
	}
	if z.MatchType != "exact-direct-match" || z.Confidence != ConfidenceHigh {
		t.Errorf("zlib match = %s/%s", z.MatchType, z.Confidence)
	}
	if z.CVSS == nil || *z.CVSS != 9.4 || z.EPSS == nil || z.EPSSPercentile == nil {
		t.Errorf("zlib scores = cvss %v epss %v", z.CVSS, z.EPSS)
	}
	if z.Package.Type != "apk" || !strings.HasPrefix(z.Package.PURL, "pkg:apk/alpine/zlib@1.3.2-r1") || z.KEV {
		t.Errorf("zlib package = %+v kev %v", z.Package, z.KEV)
	}

	b := byPkg["busybox"]
	if b.MatchType != "cpe-match" || b.Confidence != ConfidenceLow || b.FixState != "" || b.Fixable() {
		t.Errorf("busybox = %+v", b)
	}

	ws := byPkg["ws"]
	if ws.ID != "GHSA-96hv-2xvq-fx4p" || strings.Join(ws.Aliases, ",") != "CVE-2026-48779" || !ws.Names("cve-2026-48779") {
		t.Errorf("ws aliases = %v", ws.Aliases)
	}

	p := byPkg["perl-base"]
	if p.FixState != FixStateWontFix || p.Fixable() {
		t.Errorf("perl-base = %+v", p)
	}
}

func TestParseGrypeKEVMergeAndUnknownSeverity(t *testing.T) {
	t.Parallel()
	doc := `{
	  "descriptor": {"name":"grype","version":"0.122.1","db":{"status":{"schemaVersion":"v6.1.10","built":"2026-11-20T06:21:00Z","valid":true}}},
	  "matches": [
	    {"vulnerability":{"id":"GHSA-jfh8-c2jp-5v3q","severity":"Critical","fix":{"versions":["2.15.0"],"state":"fixed"}},
	     "relatedVulnerabilities":[{"id":"CVE-2021-44228","knownExploited":[{"cve":"CVE-2021-44228","knownRansomwareCampaignUse":"known"}],"epss":[{"epss":0.99999,"percentile":1}],"cvss":[{"metrics":{"baseScore":10}}]}],
	     "matchDetails":[{"type":"cpe-match"}],
	     "artifact":{"name":"log4j-core","version":"2.14.1","type":"java-archive","purl":"pkg:maven/org.apache.logging.log4j/log4j-core@2.14.1","locations":[{"path":"/app/a.jar"}]}},
	    {"vulnerability":{"id":"GHSA-jfh8-c2jp-5v3q","severity":"Critical","fix":{"versions":["2.15.0"],"state":"fixed"}},
	     "matchDetails":[{"type":"exact-direct-match"}],
	     "artifact":{"name":"log4j-core","version":"2.14.1","type":"java-archive","purl":"pkg:maven/org.apache.logging.log4j/log4j-core@2.14.1","locations":[{"path":"/app/b.jar"}]}},
	    {"vulnerability":{"id":"CVE-2026-0001","severity":"","fix":{"state":"not-fixed"}},
	     "matchDetails":[{"type":"exact-indirect-match"}],
	     "artifact":{"name":"libfoo","version":"1.0","type":"deb"}}
	  ]}`
	scan, err := ParseGrype([]byte(doc), time.Date(2026, 11, 20, 7, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Findings) != 2 {
		t.Fatalf("findings = %d, want the two log4j matches merged into one", len(scan.Findings))
	}
	l := scan.Findings[0]
	if !l.KEV || l.MatchType != "exact-direct-match" || l.Confidence != ConfidenceHigh {
		t.Errorf("log4j = kev %v match %s/%s", l.KEV, l.MatchType, l.Confidence)
	}
	if len(l.Locations) != 2 || l.CVSS == nil || *l.CVSS != 10 || *l.EPSS != 0.99999 {
		t.Errorf("log4j merge = %+v", l)
	}
	if u := scan.Findings[1]; u.Severity != SeverityUnknown || !u.Severity.AtLeast("critical") {
		t.Errorf("unknown severity = %s, must judge as critical", u.Severity)
	}
	if !scan.ScannedAt.Equal(time.Date(2026, 11, 20, 7, 0, 0, 0, time.UTC)) {
		t.Errorf("scanned_at fallback = %s", scan.ScannedAt)
	}
}

func TestParseGrypeRefusesAnUndatedDatabase(t *testing.T) {
	t.Parallel()
	if _, err := ParseGrype([]byte(`{"descriptor":{"db":{"status":{}}},"matches":[]}`), testNow); err == nil {
		t.Fatal("a scan with no database build time was accepted")
	}
	if _, err := ParseGrype([]byte(`not json`), testNow); err == nil {
		t.Fatal("garbage accepted")
	}
	scan, err := ParseGrype([]byte(`{"descriptor":{"db":{"status":{"built":"2026-11-20T06:21:00Z"}}},"matches":null}`), testNow)
	if err != nil || scan.Findings == nil || len(scan.Findings) != 0 {
		t.Fatalf("clean scan = %+v, %v (findings must be an empty list, not null)", scan, err)
	}
}

func TestParseSeverity(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]Severity{
		"Critical": SeverityCritical, "HIGH": SeverityHigh, "Medium": SeverityMedium, "low": SeverityLow,
		"Negligible": SeverityNegligible, "Unknown": SeverityUnknown, "": SeverityUnknown, "bogus": SeverityUnknown,
	} {
		if got := ParseSeverity(in); got != want {
			t.Errorf("ParseSeverity(%q) = %s, want %s", in, got, want)
		}
	}
	if SeverityNegligible.AtLeast("low") || !SeverityLow.AtLeast("low") || !SeverityMedium.AtLeast("none") {
		t.Errorf("AtLeast ordering wrong")
	}
}
