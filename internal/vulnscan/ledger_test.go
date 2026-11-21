package vulnscan

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/pkg/release"
)

// testRelease puts one SBOM in a temp machine ledger and returns a release
// with that SBOM on "api" and none on "web".
func testRelease(t *testing.T) (*ledgerfile.Store, *release.Release) {
	t.Helper()
	store, err := ledgerfile.Open(t.TempDir(), "vulnscan-test")
	if err != nil {
		t.Fatal(err)
	}
	digest, err := store.PutSBOM([]byte(`{"bomFormat":"CycloneDX","specVersion":"1.6","components":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	rel := &release.Release{
		Version: "v1.2.0",
		Artifacts: map[string]release.Artifact{
			"api": {Kind: "oci", SBOM: &release.SBOMRef{Digest: digest, Status: release.SBOMStatusOK, Source: release.SBOMSourceImage}},
			"web": {Kind: "oci", SBOM: &release.SBOMRef{Status: release.SBOMStatusUnavailable, Reason: "syft found no packages"}},
		},
	}
	return store, rel
}

func TestScanReleaseRescansOnRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	built := testNow.Add(-6 * time.Hour)
	db, g, _, _ := newTestDB(t, built, built)
	scanner := &Scanner{DB: db}
	store, rel := testRelease(t)

	local, err := db.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Never scanned: list scans it once and stores the result.
	rs, err := ScanRelease(ctx, store, scanner, local, rel, RescanIfNewer)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Rescanned != 1 || g.scans.Load() != 1 {
		t.Fatalf("first read rescanned %d (grype scans %d), want 1", rs.Rescanned, g.scans.Load())
	}
	api, web := rs.Artifacts[0], rs.Artifacts[1]
	if api.Latest == nil || len(api.Latest.Findings) != 7 || web.Latest != nil || web.Reason != "no SBOM: syft found no packages" {
		t.Fatalf("artifacts = %+v / %+v", api, web)
	}
	rec, err := store.LatestFindings(api.SBOMDigest)
	if err != nil || rec == nil || !rec.DBBuiltAt.Equal(built) || rec.Scanner != "grype" || rec.ScannerVersion != "v0.122.1" || rec.DBSchema != "v6.1.10" {
		t.Fatalf("stored record = %+v, %v", rec, err)
	}

	// Same database: the stored scan is current, no rescan.
	rs, err = ScanRelease(ctx, store, scanner, local, rel, RescanIfNewer)
	if err != nil || rs.Rescanned != 0 || g.scans.Load() != 1 {
		t.Fatalf("second read rescanned %d (grype %d), %v", rs.Rescanned, g.scans.Load(), err)
	}

	// A newer local database: rescan on read, filed as a second scan.
	newer := testNow.Add(-time.Hour)
	g.setBuilt(newer)
	local.BuiltAt = newer
	rs, err = ScanRelease(ctx, store, scanner, local, rel, RescanIfNewer)
	if err != nil || rs.Rescanned != 1 {
		t.Fatalf("newer db: rescanned %d, %v", rs.Rescanned, err)
	}
	all, _ := store.ListFindings(api.SBOMDigest)
	if len(all) != 2 || len(rs.Artifacts[0].History) != 2 {
		t.Fatalf("stored scans = %d, history %d, want 2", len(all), len(rs.Artifacts[0].History))
	}

	// RescanNever, or no scanner: only what is stored.
	rs, err = ScanRelease(ctx, store, nil, Status{}, rel, RescanAlways)
	if err != nil || rs.Rescanned != 0 || rs.Artifacts[0].Latest == nil || !rs.Artifacts[0].Latest.DBBuiltAt.Equal(newer) {
		t.Fatalf("read-only = %+v, %v", rs, err)
	}
}

// A stale local database is never scanned with: the stored scan stands, and
// the document says it is stale.
func TestScanReleaseDoesNotRescanWithAStaleDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stale := testNow.Add(-7 * 24 * time.Hour)
	db, g, _, _ := newTestDB(t, stale, stale)
	store, rel := testRelease(t)
	local, _ := db.Status(ctx)
	rs, err := ScanRelease(ctx, store, &Scanner{DB: db}, local, rel, RescanIfNewer)
	if err != nil {
		t.Fatal(err)
	}
	if g.scans.Load() != 0 || rs.Artifacts[0].Latest != nil || rs.Artifacts[0].Reason != "never scanned" {
		t.Fatalf("stale db scanned (%d) or wrong reason %q", g.scans.Load(), rs.Artifacts[0].Reason)
	}
}

func TestScanOfRefusesAForeignBody(t *testing.T) {
	t.Parallel()
	if _, err := ScanOf(ledgerfile.FindingsRecord{SBOMDigest: "sha256:x", Body: json.RawMessage(`{"schema":"other/v1"}`)}); err == nil {
		t.Fatal("a body of another schema was read as a scan")
	}
}

// The vuln-list document, golden. FORGE_UPDATE_GOLDEN=1 rewrites it.
func TestBuildDocumentGolden(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/grype-sample.json")
	if err != nil {
		t.Fatal(err)
	}
	scan, err := ParseGrype(data, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	earlier := *scan
	earlier.ScannedAt = scan.ScannedAt.Add(-48 * time.Hour)
	earlier.Findings = scan.Findings[:1]
	gate := release.DefaultDeliveryPolicy().VulnGate
	gate.Mode = release.DeliveryEnforce
	gate.Exemptions = []release.ShapeVulnExempt{{ID: "CVE-2026-48779", Reason: "ws is only used by the dev server", Expires: "2026-12-31"}}

	in := DocumentInput{
		Env: "prod", Release: "v1.2.0", Gate: gate, Now: testNow,
		Artifacts: []ArtifactScan{
			{Artifact: "api", SBOMDigest: "sha256:aaaa", Latest: scan, History: []*Scan{&earlier, scan}},
			{Artifact: "worker", SBOMDigest: "sha256:bbbb", Latest: scan},
			{Artifact: "web", Reason: "no SBOM"},
		},
	}
	doc := BuildDocument(in)
	if doc.Gate.Verdict != VerdictBlock || len(doc.Gate.Blocking) != 1 || doc.Gate.Blocking[0] != "CVE-2026-4738" {
		t.Errorf("gate = %+v", doc.Gate)
	}
	if doc.Coverage.Artifacts != 3 || doc.Coverage.WithSBOM != 2 || doc.Coverage.Scanned != 2 {
		t.Errorf("coverage = %+v", doc.Coverage)
	}
	if len(doc.Findings) != 6 {
		t.Errorf("listed = %d, want 6 (the exempt ws finding hidden)", len(doc.Findings))
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // as forge vuln --json prints it
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		t.Fatal(err)
	}
	got := buf.Bytes()
	const golden = "testdata/vuln-list.golden.json"
	if os.Getenv("FORGE_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read %s (FORGE_UPDATE_GOLDEN=1 writes it): %v", golden, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("vuln-list document changed; if intended, FORGE_UPDATE_GOLDEN=1 go test ./internal/vulnscan -run TestBuildDocumentGolden\n%s", got)
	}

	// Filters narrow what is listed, never the verdict.
	in.Filter = Filter{MinSeverity: "high", Fixable: true, IncludeExempt: true}
	filtered := BuildDocument(in)
	if filtered.Gate.Verdict != VerdictBlock || len(filtered.Findings) != 3 || filtered.Counts.Total() != 3 {
		t.Errorf("filtered: verdict %s, %d findings %+v", filtered.Gate.Verdict, len(filtered.Findings), filtered.Counts)
	}
	in.Filter = Filter{Match: "busybox"}
	if shown := BuildDocument(in); len(shown.Findings) != 1 || shown.Findings[0].Package.Name != "busybox" {
		t.Errorf("match by package = %d findings", len(shown.Findings))
	}
	in.Filter = Filter{Match: "CVE-2026-48779", IncludeExempt: true}
	if shown := BuildDocument(in); len(shown.Findings) != 1 || shown.Findings[0].Exemption == nil {
		t.Errorf("match by alias = %+v", shown.Findings)
	}
}
