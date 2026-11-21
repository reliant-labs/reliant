package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/internal/schemagen"
	"github.com/reliant-labs/forge/internal/toolfetch"
	"github.com/reliant-labs/forge/internal/vulnscan"
	"github.com/reliant-labs/forge/pkg/release"
)

var vulnTestNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// vulnHarness is a project with a machine ledger, a fake grype and syft, and
// a fake env resolution: every forge vuln verb runs against it with no
// network and no real scanner.
type vulnHarness struct {
	t     *testing.T
	store *ledgerfile.Store
	env   vulnEnv
	deps  vulnDeps

	mu         sync.Mutex
	built      time.Time // the fake database's build; zero: none
	noGrype    bool
	installs   []string // tools resolved with install=true
	scans      int
	sample     []byte
	listingHit int
}

func newVulnHarness(t *testing.T) *vulnHarness {
	t.Helper()
	store, err := ledgerfile.Open(t.TempDir(), "vuln-cli-test")
	if err != nil {
		t.Fatal(err)
	}
	sample, err := os.ReadFile("../vulnscan/testdata/grype-sample.json")
	if err != nil {
		t.Fatal(err)
	}
	h := &vulnHarness{t: t, store: store, sample: sample, built: vulnTestNow.Add(-3 * time.Hour)}
	h.env = vulnEnv{Name: "prod", Gate: release.DefaultDeliveryPolicy().VulnGate}
	listing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h.mu.Lock()
		h.listingHit++
		h.mu.Unlock()
		http.Error(w, "offline", http.StatusServiceUnavailable)
	}))
	t.Cleanup(listing.Close)
	dbDir := t.TempDir()
	projectDir := t.TempDir()
	h.deps = vulnDeps{
		tool:          h.tool,
		dbDir:         func() (string, error) { return dbDir, nil },
		dbURL:         func() string { return listing.URL },
		run:           h.run,
		resolveEnv:    func(context.Context, string, string) (vulnEnv, error) { return h.env, nil },
		machineLedger: func(string) (*ledgerfile.Store, error) { return h.store, nil },
		projectDir:    func() string { return projectDir },
		now:           func() time.Time { return vulnTestNow },
	}
	return h
}

func (h *vulnHarness) tool(_ context.Context, t toolfetch.Tool, install bool) (string, string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if install {
		h.installs = append(h.installs, t.Name)
	}
	if t.Name == "grype" && h.noGrype {
		return "", "", errors.New("grype is not installed")
	}
	return "/fake/" + t.Name, t.Version, nil
}

func (h *vulnHarness) run(_ context.Context, c vulnscan.Command) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c.Path == "/fake/syft" {
		if len(c.Args) < 4 || c.Args[0] != "convert" || c.Args[3] != "spdx-json" {
			return fmt.Errorf("unexpected syft %v", c.Args)
		}
		_, err := io.WriteString(c.Stdout, `{"spdxVersion":"SPDX-2.3"}`+"\n")
		return err
	}
	args := c.Args[3:] // after -c <config> -q
	switch {
	case args[0] == "db" && args[1] == "status":
		if h.built.IsZero() {
			_, _ = io.WriteString(c.Stdout, `{"valid":false,"error":"database does not exist"}`)
			return errors.New("exit status 1")
		}
		_, _ = fmt.Fprintf(c.Stdout, `{"schemaVersion":"v6.1.10","built":%q,"valid":true}`, h.built.Format(time.RFC3339))
		return nil
	case strings.HasPrefix(args[0], "sbom:") || strings.HasPrefix(args[0], "dir:") || strings.HasPrefix(args[0], "registry:"):
		h.scans++
		var doc map[string]any
		if err := json.Unmarshal(h.sample, &doc); err != nil {
			return err
		}
		doc["descriptor"].(map[string]any)["db"].(map[string]any)["status"].(map[string]any)["built"] = h.built.Format(time.RFC3339)
		return json.NewEncoder(c.Stdout).Encode(doc)
	}
	return fmt.Errorf("unexpected grype %v", args)
}

// cut records release version with one SBOM'd artifact ("api") and,
// when bare is set, a second artifact with none ("web"), and promotes it to
// prod.
func (h *vulnHarness) cut(version string, bare bool) string {
	h.t.Helper()
	digest, err := h.store.PutSBOM([]byte(`{"bomFormat":"CycloneDX","specVersion":"1.6","components":[]}`))
	if err != nil {
		h.t.Fatal(err)
	}
	r := release.Release{
		Version:   version,
		CreatedAt: vulnTestNow.Add(-time.Hour),
		Artifacts: map[string]release.Artifact{
			"api": {
				Kind: release.KindOCI, Mode: release.ModeShared,
				Digests: map[string]string{release.SharedVariant: "sha256:" + strings.Repeat("b", 64)},
				SBOM:    &release.SBOMRef{Digest: digest, Status: release.SBOMStatusOK, Format: release.SBOMFormatCycloneDXJSON, Source: release.SBOMSourceImage},
			},
		},
	}
	if bare {
		r.Artifacts["web"] = release.Artifact{
			Kind: release.KindOCI, Mode: release.ModeShared,
			Digests: map[string]string{release.SharedVariant: "sha256:" + strings.Repeat("c", 64)},
			SBOM:    &release.SBOMRef{Status: release.SBOMStatusUnavailable, Reason: "syft found no packages"},
		}
	}
	if _, err := h.store.CutRelease(r); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.store.AppendPromotion(release.Promotion{Env: "prod", Release: version, Kind: release.KindPromote}, nil); err != nil {
		h.t.Fatal(err)
	}
	return digest
}

func (h *vulnHarness) exec(args ...string) (stdout, stderr string, code int) {
	h.t.Helper()
	cmd := newVulnCmdWith(h.deps)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.ExecuteContext(context.Background())
	code = 0
	if err != nil {
		code = 1
		var ec *exitCodeError
		if errors.As(err, &ec) {
			code = ec.code
		}
		errOut.WriteString(err.Error())
	}
	return out.String(), errOut.String(), code
}

func decodeVulnDoc(t *testing.T, out string) vulnscan.Document {
	t.Helper()
	var doc vulnscan.Document
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not a vuln-list document: %v\n%s", err, out)
	}
	compiled, err := schemagen.Compile(vulnListSchemaJSON())
	if err != nil {
		t.Fatal(err)
	}
	inst, err := schemagen.DecodeJSON([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if errs := compiled.Validate(inst); len(errs) > 0 {
		t.Fatalf("document does not validate against forge project schema vuln-list: %v", errs)
	}
	return doc
}

func TestVulnListBlocksAndStoresTheScan(t *testing.T) {
	t.Parallel()
	h := newVulnHarness(t)
	digest := h.cut("v1.0.0", false)
	out, stderr, code := h.exec("list", "prod", "--json")
	if code != exitWrong {
		t.Fatalf("exit %d, want 1 (a fixable critical blocks)\n%s\n%s", code, out, stderr)
	}
	doc := decodeVulnDoc(t, out)
	if doc.Schema != vulnscan.DocumentSchema || doc.Env != "prod" || doc.Release != "v1.0.0" {
		t.Errorf("doc head = %s %s %s", doc.Schema, doc.Env, doc.Release)
	}
	if doc.Gate.Verdict != vulnscan.VerdictBlock || strings.Join(doc.Gate.Blocking, ",") != "CVE-2026-4738" || doc.Gate.Mode != "audit" {
		t.Errorf("gate = %+v", doc.Gate)
	}
	rec, err := h.store.LatestFindings(digest)
	if err != nil || rec == nil || rec.Scanner != "grype" {
		t.Fatalf("scan not stored: %+v %v", rec, err)
	}
	if len(h.installs) != 0 {
		t.Errorf("list installed %v: list must never download", h.installs)
	}
	// Stored and current: the second list reads, it does not scan.
	if _, _, code := h.exec("list", "prod"); code != exitWrong || h.scans != 1 {
		t.Fatalf("second list: exit %d, scans %d", code, h.scans)
	}
	// Text names the blocking advisory and the next command.
	text, _, _ := h.exec("list", "prod")
	for _, want := range []string{"vuln_gate (audit): BLOCK: CVE-2026-4738", "fixed in 1.4-r0", "→ forge vuln show prod CVE-2026-4738", "CPE match, low confidence"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
}

func TestVulnListUnscannedArtifactIsUndetermined(t *testing.T) {
	t.Parallel()
	h := newVulnHarness(t)
	h.env.Gate.BlockSeverity = "none" // nothing but KEV blocks
	h.cut("v1.0.0", true)
	out, _, code := h.exec("list", "prod", "--json")
	if code != exitUndetermined {
		t.Fatalf("exit %d, want 2 (web has no SBOM)", code)
	}
	doc := decodeVulnDoc(t, out)
	if doc.Gate.Verdict != vulnscan.VerdictUnscanned || doc.Coverage.WithSBOM != 1 || doc.Coverage.Artifacts != 2 {
		t.Errorf("doc = %+v / %+v", doc.Gate, doc.Coverage)
	}
}

// Findings from a database older than five days are never a pass, and a
// stale local database is never scanned with.
func TestVulnListStaleIsUndetermined(t *testing.T) {
	t.Parallel()
	h := newVulnHarness(t)
	h.env.Gate.BlockSeverity = "none"
	digest := h.cut("v1.0.0", false)
	old := vulnTestNow.Add(-6 * 24 * time.Hour)
	stored := &vulnscan.Scan{Schema: vulnscan.ScanSchema, Scanner: "grype", ScannerVersion: "v0.122.1", DBSchema: "v6.1.10", DBBuiltAt: old, ScannedAt: old, Findings: []vulnscan.Finding{}}
	rec, err := vulnscan.Record(digest, stored)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.PutFindings(rec); err != nil {
		t.Fatal(err)
	}
	h.built = old
	out, _, code := h.exec("list", "prod", "--json")
	if code != exitUndetermined {
		t.Fatalf("exit %d, want 2", code)
	}
	if doc := decodeVulnDoc(t, out); doc.Gate.Verdict != vulnscan.VerdictStale || !doc.DB.Stale {
		t.Errorf("gate %+v db %+v", doc.Gate, doc.DB)
	}
	if h.scans != 0 {
		t.Errorf("scanned with a stale database")
	}
}

func TestVulnListOnAControlPlaneEnvSaysSoAndExits2(t *testing.T) {
	t.Parallel()
	h := newVulnHarness(t)
	h.env.Endpoint = "https://api.reliantlabs.io"
	for _, args := range [][]string{{"list", "prod"}, {"show", "prod", "CVE-1"}, {"sbom", "prod"}, {"scan", "--deployed", "prod"}} {
		_, stderr, code := h.exec(args...)
		if code != exitUndetermined || !strings.Contains(stderr, "lands in a later forge release") {
			t.Errorf("%v: exit %d\n%s", args, code, stderr)
		}
	}
}

func TestVulnListWithoutScannerReadsWhatIsStored(t *testing.T) {
	t.Parallel()
	h := newVulnHarness(t)
	h.noGrype = true
	h.env.Gate.BlockSeverity = "none"
	h.cut("v1.0.0", false)
	out, _, code := h.exec("list", "prod")
	if code != exitUndetermined || !strings.Contains(out, "grype is not installed") || !strings.Contains(out, "never scanned") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

func TestVulnShowByAliasAndPackage(t *testing.T) {
	t.Parallel()
	h := newVulnHarness(t)
	h.env.Gate.Exemptions = []release.ShapeVulnExempt{{ID: "CVE-2026-48779", Reason: "dev server only", Expires: "2026-12-31"}}
	h.cut("v1.0.0", false)
	out, _, _ := h.exec("show", "prod", "cve-2026-48779", "--json")
	doc := decodeVulnDoc(t, out)
	if len(doc.Findings) != 1 || doc.Findings[0].ID != "GHSA-96hv-2xvq-fx4p" || doc.Findings[0].Exemption == nil {
		t.Fatalf("show by alias = %+v", doc.Findings)
	}
	out, _, _ = h.exec("show", "prod", "busybox", "--json")
	if doc := decodeVulnDoc(t, out); len(doc.Findings) != 1 || doc.Findings[0].Package.Name != "busybox" {
		t.Fatalf("show by package = %+v", doc.Findings)
	}
	text, _, _ := h.exec("show", "prod", "CVE-0000-0000")
	if !strings.Contains(text, `no finding in prod release v1.0.0 matches "CVE-0000-0000"`) {
		t.Fatalf("no-match text:\n%s", text)
	}
}

func TestVulnListFilters(t *testing.T) {
	t.Parallel()
	h := newVulnHarness(t)
	h.cut("v1.0.0", false)
	out, _, code := h.exec("list", "prod", "--severity", "high", "--fixable", "--json")
	doc := decodeVulnDoc(t, out)
	if code != exitWrong || doc.Gate.Verdict != vulnscan.VerdictBlock {
		t.Errorf("a filter changed the verdict: exit %d %s", code, doc.Gate.Verdict)
	}
	for _, f := range doc.Findings {
		if !f.Fixable() || !f.Severity.AtLeast("high") {
			t.Errorf("filtered list holds %s %s %s", f.ID, f.Severity, f.FixState)
		}
	}
	if _, _, code := h.exec("list", "prod", "--severity", "huge"); code != 1 {
		t.Errorf("--severity huge accepted")
	}
}

func TestVulnScanRelease(t *testing.T) {
	t.Parallel()
	h := newVulnHarness(t)
	digest := h.cut("v1.0.0", false)
	out, _, code := h.exec("scan", "--release", "v1.0.0", "--json")
	doc := decodeVulnDoc(t, out)
	if code != exitWrong || doc.Env != "" || doc.Release != "v1.0.0" || h.scans != 1 {
		t.Fatalf("exit %d env %q release %q scans %d", code, doc.Env, doc.Release, h.scans)
	}
	if rec, _ := h.store.LatestFindings(digest); rec == nil {
		t.Fatalf("scan --release stored nothing")
	}
	if !strings.Contains(strings.Join(h.installs, ","), "grype") {
		t.Errorf("scan did not ensure grype: %v", h.installs)
	}
	// --deployed: the release prod runs, judged by prod's gate.
	h.env.Gate.Mode = release.DeliveryOff
	out, _, code = h.exec("scan", "--deployed", "prod", "--json")
	if doc := decodeVulnDoc(t, out); code != 0 || doc.Gate.Verdict != vulnscan.VerdictOff || doc.Env != "prod" {
		t.Fatalf("--deployed with the gate off: exit %d verdict %s", code, doc.Gate.Verdict)
	}
}

func TestVulnScanDirectory(t *testing.T) {
	t.Parallel()
	h := newVulnHarness(t)
	out, _, code := h.exec("scan", t.TempDir(), "--json")
	doc := decodeVulnDoc(t, out)
	if code != exitWrong || doc.Coverage.Scanned != 1 || len(doc.Findings) != 7 {
		t.Fatalf("exit %d coverage %+v findings %d", code, doc.Coverage, len(doc.Findings))
	}
	if _, stderr, code := h.exec("scan", "--image", "ghcr.io/acme/api:v1"); code != 1 || !strings.Contains(stderr, "names no digest") {
		t.Errorf("an image by tag was scanned: exit %d %s", code, stderr)
	}
	if _, _, code := h.exec("scan", ".", "--release", "v1"); code != 1 {
		t.Errorf("two subjects accepted")
	}
}

// A database the listing cannot refresh and that is past five days: scan
// refuses to judge (exit 2) rather than pass.
func TestVulnScanWithAStaleDatabaseIsUndetermined(t *testing.T) {
	t.Parallel()
	h := newVulnHarness(t)
	h.built = vulnTestNow.Add(-6 * 24 * time.Hour)
	_, stderr, code := h.exec("scan", t.TempDir())
	if code != exitUndetermined || !strings.Contains(stderr, "stale") || h.scans != 0 {
		t.Fatalf("exit %d scans %d\n%s", code, h.scans, stderr)
	}
	if h.listingHit == 0 {
		t.Errorf("scan did not try to refresh a stale database")
	}
	h.built = time.Time{}
	if _, stderr, code := h.exec("scan", t.TempDir()); code != exitUndetermined || !strings.Contains(stderr, "no vulnerability database") {
		t.Fatalf("no db: exit %d\n%s", code, stderr)
	}
}

func TestVulnSBOM(t *testing.T) {
	t.Parallel()
	h := newVulnHarness(t)
	h.cut("v1.0.0", true)
	out, _, code := h.exec("sbom", "prod")
	if code != 0 || !strings.Contains(out, `"bomFormat":"CycloneDX"`) {
		t.Fatalf("sbom: exit %d\n%s", code, out)
	}
	if _, stderr, code := h.exec("sbom", "prod", "--artifact", "web"); code != exitUndetermined || !strings.Contains(stderr, "syft found no packages") {
		t.Errorf("sbom of an artifact with none: exit %d %s", code, stderr)
	}
	out, _, code = h.exec("sbom", "prod", "--format", "spdx")
	if code != 0 || !strings.Contains(out, "SPDX-2.3") {
		t.Errorf("spdx: exit %d\n%s", code, out)
	}
}

func TestVulnDBStatus(t *testing.T) {
	t.Parallel()
	h := newVulnHarness(t)
	out, _, code := h.exec("db", "status", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var doc vulnDBDocument
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Schema != vulnDBDocumentSchema || !doc.Database.Present || doc.Database.Schema != "v6.1.10" || !doc.Fresh || doc.CacheKey != "forge-vulndb-v6-2026-11-20" {
		t.Errorf("db status = %+v", doc)
	}
	h.built = time.Time{}
	if text, _, code := h.exec("db", "status"); code != exitUndetermined || !strings.Contains(text, "database does not exist") {
		t.Errorf("absent db: exit %d\n%s", code, text)
	}
}

// TestVulnListSchemaIsCurrent regenerates the vuln-list schema from
// vulnscan.Document and fails when the committed file differs.
func TestVulnListSchemaIsCurrent(t *testing.T) {
	got, err := generateVulnListSchema(newSourceDescriber(t, "../.."))
	if err != nil {
		t.Fatal(err)
	}
	const path = "../../schema/vuln-list.v1.schema.json"
	if os.Getenv("FORGE_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s (FORGE_UPDATE_GOLDEN=1 writes it): %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("schema/vuln-list.v1.schema.json is stale: vulnscan.Document changed.\n" +
			"Regenerate it: FORGE_UPDATE_GOLDEN=1 go test ./internal/cli -run TestVulnListSchemaIsCurrent")
	}
	if !bytes.Equal(want, vulnListSchemaJSON()) {
		t.Errorf("the embedded schema (schema.VulnListV1) is not the committed file")
	}
}

// The vulnscan golden document validates under forge's validator and under
// an independent draft 2020-12 implementation.
func TestVulnListSchemaValidatesTheGoldenDocument(t *testing.T) {
	t.Parallel()
	golden, err := os.ReadFile("../vulnscan/testdata/vuln-list.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := schemagen.Compile(vulnListSchemaJSON())
	if err != nil {
		t.Fatal(err)
	}
	inst, err := schemagen.DecodeJSON(golden)
	if err != nil {
		t.Fatal(err)
	}
	if errs := compiled.Validate(inst); len(errs) > 0 {
		t.Fatalf("forge validator: %v", errs)
	}
	var std jsonschema.Schema
	if err := json.Unmarshal(vulnListSchemaJSON(), &std); err != nil {
		t.Fatal(err)
	}
	resolved, err := std.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(golden, &v); err != nil {
		t.Fatal(err)
	}
	if err := resolved.Validate(v); err != nil {
		t.Fatalf("draft 2020-12 validator: %v", err)
	}
	var out bytes.Buffer
	if err := runProjectSchema(&out, "vuln-list"); err != nil || !bytes.Equal(out.Bytes(), vulnListSchemaJSON()) {
		t.Fatalf("forge project schema vuln-list: %v", err)
	}
}

// storeScan files a stored scan of digest against a database built at
// built: clean (no findings) or the sample's findings (one blocks).
func (h *vulnHarness) storeScan(digest string, built time.Time, clean bool) {
	h.t.Helper()
	s := &vulnscan.Scan{Schema: vulnscan.ScanSchema, Scanner: "grype", ScannerVersion: "v0.122.1", DBSchema: "v6.1.10", DBBuiltAt: built, ScannedAt: built.Add(time.Minute), Findings: []vulnscan.Finding{}}
	if !clean {
		parsed, err := vulnscan.ParseGrype(h.sample, built)
		if err != nil {
			h.t.Fatal(err)
		}
		s.Findings = parsed.Findings
	}
	rec, err := vulnscan.Record(digest, s)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.store.PutFindings(rec); err != nil {
		h.t.Fatal(err)
	}
}

// TestVulnListWithoutACurrentScanIsUndetermined is B1: when nothing can be
// rescanned (no grype, no database, a stale database), the stored scans
// stand in, and the verdict is determined only if each artifact's newest
// stored scan used a database built within five days. Otherwise exit 2,
// whatever the stored findings say; they are still listed.
func TestVulnListWithoutACurrentScanIsUndetermined(t *testing.T) {
	t.Parallel()
	staleBuild := vulnTestNow.Add(-6 * 24 * time.Hour)
	freshBuild := vulnTestNow.Add(-2 * 24 * time.Hour)
	cases := []struct {
		name      string
		local     string // "no-grype", "no-db", "stale-db"
		stored    time.Time
		clean     bool
		want      int
		verdict   vulnscan.Verdict
		wantNote  string
		blockedBy string
	}{
		{"absent scanner, stored clean scan with a stale db", "no-grype", staleBuild, true, exitUndetermined, vulnscan.VerdictStale, "grype is not installed", ""},
		{"absent db, stored clean scan with a stale db", "no-db", staleBuild, true, exitUndetermined, vulnscan.VerdictStale, "no vulnerability database", ""},
		{"stale local db, stored clean scan with it", "stale-db", staleBuild, true, exitUndetermined, vulnscan.VerdictStale, "older than 5 days, so nothing was rescanned", ""},
		{"stale local db, stored blocking scan: still undetermined, blocker listed", "stale-db", staleBuild, false, exitUndetermined, vulnscan.VerdictStale, "undetermined:", "CVE-2026-4738"},
		{"absent scanner, stored clean scan with a fresh db: determined", "no-grype", freshBuild, true, exitOK, vulnscan.VerdictPass, "grype is not installed", ""},
		{"absent db, stored clean scan with a fresh db: determined", "no-db", freshBuild, true, exitOK, vulnscan.VerdictPass, "no vulnerability database", ""},
		{"absent scanner, stored fresh blocking scan: blocks", "no-grype", freshBuild, false, exitWrong, vulnscan.VerdictBlock, "grype is not installed", "CVE-2026-4738"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newVulnHarness(t)
			digest := h.cut("v1.0.0", false)
			h.storeScan(digest, tc.stored, tc.clean)
			switch tc.local {
			case "no-grype":
				h.noGrype = true
			case "no-db":
				h.built = time.Time{}
			case "stale-db":
				h.built = staleBuild
			}
			out, _, code := h.exec("list", "prod", "--json")
			if code != tc.want {
				t.Fatalf("exit %d, want %d\n%s", code, tc.want, out)
			}
			doc := decodeVulnDoc(t, out)
			if doc.Gate.Verdict != tc.verdict || doc.Gate.Determined != tc.verdict.Determined() || doc.DB.Stale != (tc.verdict == vulnscan.VerdictStale) {
				t.Errorf("gate %+v db %+v", doc.Gate, doc.DB)
			}
			if !strings.Contains(strings.Join(doc.Notes, "\n"), tc.wantNote) {
				t.Errorf("notes %q lack %q", doc.Notes, tc.wantNote)
			}
			if tc.blockedBy != "" && (strings.Join(doc.Gate.Blocking, ",") != tc.blockedBy || len(doc.Findings) == 0) {
				t.Errorf("stored findings not kept for display: blocking %v, %d findings", doc.Gate.Blocking, len(doc.Findings))
			}
			if h.scans != 0 {
				t.Errorf("rescanned %d times with no usable scanner or database", h.scans)
			}
			// show reads the same path and exits the same way.
			if _, _, code := h.exec("show", "prod", "zlib"); code != tc.want {
				t.Errorf("show: exit %d, want %d", code, tc.want)
			}
		})
	}
}

// scan --deployed needs a current scan: a stale database it cannot refresh
// is exit 2 even when a fresh-looking clean scan is stored.
func TestVulnScanDeployedWithAStaleDatabaseIsUndetermined(t *testing.T) {
	t.Parallel()
	h := newVulnHarness(t)
	digest := h.cut("v1.0.0", false)
	h.storeScan(digest, vulnTestNow.Add(-time.Hour), true)
	h.built = vulnTestNow.Add(-6 * 24 * time.Hour)
	if _, stderr, code := h.exec("scan", "--deployed", "prod"); code != exitUndetermined || !strings.Contains(stderr, "stale") {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
}
