package cli

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/toolfetch"
	"github.com/reliant-labs/forge/internal/vulnscan"
	"github.com/reliant-labs/forge/pkg/release"
)

// gateMatch is one grype match the fake grype reports for every SBOM.
type gateMatch struct {
	id, severity, fixState, pkg string
	kev                         bool
}

// vulnGateHarness is the vuln harness with a fake syft that records what it
// read and a fake grype whose findings each test chooses. Its vulnerability
// database listing fails the test on any request.
type vulnGateHarness struct {
	*vulnHarness
	mu       sync.Mutex
	matches  []gateMatch
	syftSrcs []string
	noSyft   bool
	grypeEnv []string
}

func newVulnGateHarness(t *testing.T) *vulnGateHarness {
	t.Helper()
	g := &vulnGateHarness{vulnHarness: newVulnHarness(t)}
	g.deps.run = g.grype
	g.deps.sbomRunner = g
	listing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("forge vuln gate read the network: %s %s", r.Method, r.URL)
		http.Error(w, "network forbidden", http.StatusForbidden)
	}))
	t.Cleanup(listing.Close)
	g.deps.dbURL = func() string { return listing.URL }
	g.deps.tool = func(ctx context.Context, tl toolfetch.Tool, install bool) (string, string, error) {
		if tl.Name == "syft" && g.noSyft {
			return "", "", errors.New("syft v1.57.2 is not installed")
		}
		return g.vulnHarness.tool(ctx, tl, install)
	}
	return g
}

// Run is the fake syft: it records the source and describes it as an empty
// CycloneDX document named for the artifact.
func (g *vulnGateHarness) Run(_ context.Context, _ string, args, _ []string) ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(args) < 2 || args[0] != "scan" {
		return nil, fmt.Errorf("unexpected syft %v", args)
	}
	g.syftSrcs = append(g.syftSrcs, args[1])
	name := ""
	for i, a := range args {
		if a == "--source-name" && i+1 < len(args) {
			name = args[i+1]
		}
	}
	return fmt.Appendf(nil, `{"bomFormat":"CycloneDX","specVersion":"1.6","metadata":{"component":{"name":%q}},"components":[]}`, name), nil
}

func (g *vulnGateHarness) grype(_ context.Context, c vulnscan.Command) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.grypeEnv = c.Env
	args := c.Args[3:]
	switch {
	case args[0] == "db" && args[1] == "status":
		if g.built.IsZero() {
			_, _ = io.WriteString(c.Stdout, `{"valid":false,"error":"database does not exist"}`)
			return errors.New("exit status 1")
		}
		_, _ = fmt.Fprintf(c.Stdout, `{"schemaVersion":"v6.1.10","built":%q,"valid":true}`, g.built.Format(time.RFC3339))
		return nil
	case strings.HasPrefix(args[0], "sbom:"):
		g.scans++
		matches := []map[string]any{}
		for _, m := range g.matches {
			v := map[string]any{"id": m.id, "severity": m.severity, "fix": map[string]any{"state": m.fixState, "versions": []string{}}}
			if m.fixState == vulnscan.FixStateFixed {
				v["fix"] = map[string]any{"state": m.fixState, "versions": []string{"9.9.9"}}
			}
			if m.kev {
				v["knownExploited"] = []map[string]any{{"cve": m.id}}
			}
			matches = append(matches, map[string]any{
				"vulnerability": v,
				"matchDetails":  []map[string]any{{"type": "exact-direct-match"}},
				"artifact":      map[string]any{"name": m.pkg, "version": "1.0.0", "type": "go-module", "purl": "pkg:golang/" + m.pkg + "@1.0.0"},
			})
		}
		return json.NewEncoder(c.Stdout).Encode(map[string]any{
			"matches":    matches,
			"descriptor": map[string]any{"name": "grype", "version": "0.122.1", "db": map[string]any{"status": map[string]any{"schemaVersion": "v6.1.10", "built": g.built.Format(time.RFC3339), "valid": true}}},
		})
	}
	return fmt.Errorf("unexpected grype %v", args)
}

func (g *vulnGateHarness) execGate(args ...string) (vulnscan.Document, string, int) {
	g.t.Helper()
	stdout, stderr, code := g.exec(append([]string{"gate", "--json"}, args...)...)
	return decodeVulnDoc(g.t, stdout), stderr, code
}

// buildOutput is a directory of build output.
func buildOutput(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "dist")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app"), []byte("\x7fELF"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// imageTar is a tiny `docker save` tarball: a manifest.json and no layers.
func imageTar(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := `[{"Config":"config.json","RepoTags":["api:dev"],"Layers":[]}]`
	if err := tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	p := filepath.Join(t.TempDir(), "api.tar")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// dbDirTree lists a directory, so a test can prove nothing changed in it.
func dbDirTree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func TestVulnGateWithDBIsReadOnlyAndOffline(t *testing.T) {
	t.Parallel()
	h := newVulnGateHarness(t)
	// Four days old: past the one-day refresh a networked scan would do.
	h.built = vulnTestNow.Add(-4 * 24 * time.Hour)
	db := t.TempDir()
	if err := os.MkdirAll(filepath.Join(db, "grype", "6"), 0o755); err != nil {
		t.Fatal(err)
	}
	before := dbDirTree(t, db)
	out := filepath.Join(t.TempDir(), "out")

	doc, stderr, code := h.execGate(buildOutput(t), "--db", db, "--out", out)
	if code != 0 || doc.Gate.Verdict != vulnscan.VerdictPass {
		t.Fatalf("exit %d, verdict %s: %s", code, doc.Gate.Verdict, stderr)
	}
	if after := dbDirTree(t, db); strings.Join(before, ",") != strings.Join(after, ",") {
		t.Errorf("--db directory changed: %v -> %v", before, after)
	}
	if len(h.installs) != 0 {
		t.Errorf("offline gate installed %v", h.installs)
	}
	if h.listingHit != 0 {
		t.Errorf("offline gate read the database listing %d times", h.listingHit)
	}
	if !hasEnvVar(h.grypeEnv, "GRYPE_DB_CACHE_DIR="+filepath.Join(db, "grype")) {
		t.Errorf("grype not pointed at --db: %v", h.grypeEnv)
	}
	if _, err := os.Stat(filepath.Join(out, vulnGateDocFile)); err != nil {
		t.Errorf("--out has no %s: %v", vulnGateDocFile, err)
	}
}

func TestVulnGateOfflineMachineDatabaseNeverFetches(t *testing.T) {
	t.Parallel()
	h := newVulnGateHarness(t)
	h.built = vulnTestNow.Add(-4 * 24 * time.Hour)
	_, stderr, code := h.execGate(buildOutput(t), "--offline")
	if code != 0 || len(h.installs) != 0 || h.listingHit != 0 {
		t.Fatalf("exit %d, installs %v, listing reads %d: %s", code, h.installs, h.listingHit, stderr)
	}
}

func TestVulnGateWithNoDatabaseIsUnscanned(t *testing.T) {
	t.Parallel()
	h := newVulnGateHarness(t)
	h.built = time.Time{}
	out := filepath.Join(t.TempDir(), "out")
	doc, _, code := h.execGate(buildOutput(t), "--db", filepath.Join(t.TempDir(), "absent"), "--out", out)
	if code != exitUndetermined {
		t.Fatalf("exit %d, want %d", code, exitUndetermined)
	}
	if doc.Gate.Verdict != vulnscan.VerdictUnscanned || doc.Gate.Reason != release.RefusalVulnGateUnscanned || doc.Gate.Determined {
		t.Errorf("gate = %+v", doc.Gate)
	}
	if len(doc.Coverage.Unscanned) != 1 || !strings.Contains(doc.Coverage.Unscanned[0].Reason, "no vulnerability database") {
		t.Errorf("coverage = %+v", doc.Coverage)
	}
	// The SBOM is still made and written: the release needs it scanned or not.
	if len(doc.SBOMs) != 1 || doc.Coverage.WithSBOM != 1 {
		t.Fatalf("sboms = %+v, coverage %+v", doc.SBOMs, doc.Coverage)
	}
	if _, err := os.Stat(filepath.Join(out, doc.SBOMs[0].Path)); err != nil {
		t.Errorf("SBOM not written: %v", err)
	}
}

func TestVulnGateWithAStaleDatabaseIsStale(t *testing.T) {
	t.Parallel()
	h := newVulnGateHarness(t)
	h.built = vulnTestNow.Add(-6 * 24 * time.Hour)
	h.matches = []gateMatch{{id: "CVE-2026-1", severity: "Critical", fixState: "fixed", pkg: "x"}}
	doc, _, code := h.execGate(buildOutput(t), "--db", t.TempDir())
	if code != exitUndetermined {
		t.Fatalf("exit %d, want %d", code, exitUndetermined)
	}
	if doc.Gate.Verdict != vulnscan.VerdictStale || doc.Gate.Reason != release.RefusalVulnGateUnscanned || !doc.DB.Stale {
		t.Errorf("gate = %+v, db = %+v", doc.Gate, doc.DB)
	}
	if h.scans != 0 {
		t.Errorf("scanned %d times against a stale database", h.scans)
	}
}

func TestVulnGateVerdicts(t *testing.T) {
	t.Parallel()
	exemptPolicy := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(exemptPolicy, []byte("exemptions:\n  - id: CVE-2026-2\n    reason: the parser is never called\n    expires: 2026-12-31\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		matches    []gateMatch
		args       []string
		wantCode   int
		wantVerd   vulnscan.Verdict
		wantRule   vulnscan.Rule
		wantExempt string
	}{
		{name: "a KEV-listed advisory blocks", matches: []gateMatch{{id: "CVE-2026-1", severity: "Low", fixState: "not-fixed", pkg: "a", kev: true}},
			wantCode: exitWrong, wantVerd: vulnscan.VerdictBlock, wantRule: vulnscan.RuleBlockKEV},
		{name: "a fixable critical blocks", matches: []gateMatch{{id: "CVE-2026-2", severity: "Critical", fixState: "fixed", pkg: "b"}},
			wantCode: exitWrong, wantVerd: vulnscan.VerdictBlock, wantRule: vulnscan.RuleBlockSeverity},
		{name: "a high warns and passes", matches: []gateMatch{{id: "CVE-2026-3", severity: "High", fixState: "fixed", pkg: "c"}},
			wantCode: exitOK, wantVerd: vulnscan.VerdictWarn, wantRule: vulnscan.RuleWarnSeverity},
		{name: "an exemption takes a critical out", matches: []gateMatch{{id: "CVE-2026-2", severity: "Critical", fixState: "fixed", pkg: "b"}},
			args: []string{"--policy", exemptPolicy}, wantCode: exitOK, wantVerd: vulnscan.VerdictPass, wantExempt: "CVE-2026-2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newVulnGateHarness(t)
			h.matches = tc.matches
			doc, stderr, code := h.execGate(append([]string{buildOutput(t), "--db", t.TempDir()}, tc.args...)...)
			if code != tc.wantCode || doc.Gate.Verdict != tc.wantVerd {
				t.Fatalf("exit %d verdict %s, want %d %s: %s", code, doc.Gate.Verdict, tc.wantCode, tc.wantVerd, stderr)
			}
			if tc.wantRule != "" && (len(doc.Findings) != 1 || doc.Findings[0].Rule != tc.wantRule) {
				t.Errorf("findings = %+v, want one decided by %s", doc.Findings, tc.wantRule)
			}
			if tc.wantCode == exitWrong && doc.Gate.Reason != release.RefusalVulnGateBlocked {
				t.Errorf("reason = %q", doc.Gate.Reason)
			}
			if tc.wantExempt != "" && (len(doc.Gate.ExemptionsApplied) != 1 || doc.Gate.ExemptionsApplied[0].ID != tc.wantExempt) {
				t.Errorf("exemptions applied = %+v", doc.Gate.ExemptionsApplied)
			}
		})
	}
}

func TestVulnGateReadsDirectoriesImageTarsAndSBOMs(t *testing.T) {
	t.Parallel()
	h := newVulnGateHarness(t)
	dir, tarball := buildOutput(t), imageTar(t)
	provided := filepath.Join(t.TempDir(), "web.spdx.json")
	if err := os.WriteFile(provided, []byte(`{"spdxVersion":"SPDX-2.3","creationInfo":{"creators":["Tool: syft-1.57.2"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out")
	doc, stderr, code := h.execGate("site="+dir, tarball, "--sbom", "web="+provided, "--db", t.TempDir(), "--out", out)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	absDir, _ := filepath.Abs(dir)
	absTar, _ := filepath.Abs(tarball)
	sort.Strings(h.syftSrcs)
	if want := []string{"dir:" + absDir, "docker-archive:" + absTar}; strings.Join(h.syftSrcs, " ") != strings.Join(want, " ") {
		t.Errorf("syft read %v, want %v", h.syftSrcs, want)
	}
	if doc.Coverage.Artifacts != 3 || doc.Coverage.Scanned != 3 || h.scans != 3 {
		t.Errorf("coverage = %+v after %d scans", doc.Coverage, h.scans)
	}
	got := map[string]vulnscan.SBOMFile{}
	for _, f := range doc.SBOMs {
		got[f.Artifact] = f
		if _, err := os.Stat(filepath.Join(out, f.Path)); err != nil {
			t.Errorf("%s: %v", f.Path, err)
		}
	}
	if got["site"].Source != release.SBOMSourceLockfile || got["api.tar"].Source != release.SBOMSourceImage ||
		got["web"].Source != release.SBOMSourceCIProvided || got["web"].Path != "web.spdx.json" || got["api.tar"].Path != "api.tar.cdx.json" {
		t.Errorf("sboms = %+v", doc.SBOMs)
	}
	var written vulnscan.Document
	b, err := os.ReadFile(filepath.Join(out, vulnGateDocFile))
	if err != nil || json.Unmarshal(b, &written) != nil || len(written.SBOMs) != 3 {
		t.Errorf("%s = %s (%v)", vulnGateDocFile, b, err)
	}
}

func TestVulnGateUnscannedInputs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		setup  func(h *vulnGateHarness, t *testing.T) []string
		reason string
	}{
		{"syft missing offline", func(h *vulnGateHarness, t *testing.T) []string {
			h.noSyft = true
			return []string{buildOutput(t)}
		}, "forge tools install syft grype"},
		{"grype missing offline", func(h *vulnGateHarness, t *testing.T) []string {
			h.noGrype = true
			return []string{buildOutput(t)}
		}, "forge tools install syft grype"},
		{"a path the build never made", func(_ *vulnGateHarness, t *testing.T) []string {
			return []string{filepath.Join(t.TempDir(), "absent")}
		}, "no such input"},
		{"a tar with no image", func(_ *vulnGateHarness, t *testing.T) []string {
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			_ = tw.WriteHeader(&tar.Header{Name: "README", Mode: 0o644})
			_ = tw.Close()
			p := filepath.Join(t.TempDir(), "x.tar")
			_ = os.WriteFile(p, buf.Bytes(), 0o644)
			return []string{p}
		}, "neither a docker manifest.json"},
		{"an SBOM that is not one", func(_ *vulnGateHarness, t *testing.T) []string {
			p := filepath.Join(t.TempDir(), "sbom.json")
			_ = os.WriteFile(p, []byte(`{"hello":1}`), 0o644)
			return []string{"--sbom", p}
		}, "is not an SBOM"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newVulnGateHarness(t)
			args := tc.setup(h, t)
			doc, stderr, code := h.execGate(append(args, "--db", t.TempDir())...)
			if code != exitUndetermined || doc.Gate.Verdict != vulnscan.VerdictUnscanned {
				t.Fatalf("exit %d verdict %s, want 2 unscanned: %s", code, doc.Gate.Verdict, stderr)
			}
			if len(doc.Coverage.Unscanned) != 1 || !strings.Contains(doc.Coverage.Unscanned[0].Reason, tc.reason) {
				t.Errorf("unscanned = %+v, want a reason saying %q", doc.Coverage.Unscanned, tc.reason)
			}
		})
	}
}

func TestVulnGatePolicySources(t *testing.T) {
	t.Parallel()
	critical := []gateMatch{{id: "CVE-2026-2", severity: "Critical", fixState: "fixed", pkg: "b"}}

	t.Run("--env judges by the env's gate", func(t *testing.T) {
		t.Parallel()
		h := newVulnGateHarness(t)
		h.matches = critical
		h.env.Gate.BlockSeverity = "none"
		h.env.Gate.BlockKEV = false
		doc, _, code := h.execGate(buildOutput(t), "--db", t.TempDir(), "--env", "prod")
		if code != 0 || doc.Env != "prod" || doc.Gate.Verdict != vulnscan.VerdictWarn {
			t.Fatalf("exit %d env %q verdict %s", code, doc.Env, doc.Gate.Verdict)
		}
	})
	t.Run("--policy JSON over the defaults", func(t *testing.T) {
		t.Parallel()
		h := newVulnGateHarness(t)
		h.matches = []gateMatch{{id: "CVE-2026-3", severity: "High", fixState: "fixed", pkg: "c"}}
		p := filepath.Join(t.TempDir(), "p.json")
		_ = os.WriteFile(p, []byte(`{"block_severity":"high"}`), 0o644)
		doc, _, code := h.execGate(buildOutput(t), "--db", t.TempDir(), "--policy", p)
		if code != exitWrong || doc.Gate.Verdict != vulnscan.VerdictBlock {
			t.Fatalf("exit %d verdict %s", code, doc.Gate.Verdict)
		}
	})
	t.Run("forge.yaml exemptions apply with no --env", func(t *testing.T) {
		t.Parallel()
		h := newVulnGateHarness(t)
		h.matches = critical
		proj := h.deps.projectDir()
		_ = os.WriteFile(filepath.Join(proj, "forge.yaml"), []byte("name: demo\nci:\n  vuln_scan:\n    exemptions:\n      - id: CVE-2026-2\n        reason: never reached\n        expires: 2026-12-31\n"), 0o644)
		doc, stderr, code := h.execGate(buildOutput(t), "--db", t.TempDir())
		if code != 0 || len(doc.Gate.ExemptionsApplied) != 1 {
			t.Fatalf("exit %d, exemptions %+v, notes %v: %s", code, doc.Gate.ExemptionsApplied, doc.Notes, stderr)
		}
	})
	// A scoped exemption in --policy covers its advisory only in the packages
	// it names, and is checked like an unscoped one.
	for _, tc := range []struct {
		name, policy string
		wantCode     int
		wantApplied  bool
		wantNote     string
	}{
		{name: "in its package", policy: "scoped_exemptions:\n  - id: CVE-2026-2\n    reason: never reached\n    expires: 2026-12-31\n    packages: [b]\n", wantCode: exitOK, wantApplied: true},
		{name: "in another package", policy: "scoped_exemptions:\n  - id: CVE-2026-2\n    reason: never reached\n    expires: 2026-12-31\n    packages: [other]\n", wantCode: exitWrong},
		{name: "with no reason", policy: "scoped_exemptions:\n  - id: CVE-2026-2\n    expires: 2026-12-31\n    packages: [b]\n", wantCode: exitWrong, wantNote: "exemption not honored"},
	} {
		t.Run("--policy scoped exemption "+tc.name, func(t *testing.T) {
			t.Parallel()
			h := newVulnGateHarness(t)
			h.matches = critical
			p := filepath.Join(t.TempDir(), "p.yaml")
			_ = os.WriteFile(p, []byte(tc.policy), 0o644)
			doc, stderr, code := h.execGate(buildOutput(t), "--db", t.TempDir(), "--policy", p)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d: %s", code, tc.wantCode, stderr)
			}
			if applied := len(doc.Gate.ExemptionsApplied) == 1 && slices.Equal(doc.Gate.ExemptionsApplied[0].Packages, []string{"b"}); applied != tc.wantApplied {
				t.Errorf("exemptions applied = %+v", doc.Gate.ExemptionsApplied)
			}
			if tc.wantNote != "" && !strings.Contains(strings.Join(doc.Notes, "\n"), tc.wantNote) {
				t.Errorf("notes = %v, want %q", doc.Notes, tc.wantNote)
			}
		})
	}
	for _, bad := range []string{`block_severity: severe`, `mode: sometimes`, `unknown_field: 1`} {
		t.Run("bad policy "+bad, func(t *testing.T) {
			t.Parallel()
			h := newVulnGateHarness(t)
			p := filepath.Join(t.TempDir(), "p.yaml")
			_ = os.WriteFile(p, []byte(bad+"\n"), 0o644)
			_, stderr, code := h.exec("gate", buildOutput(t), "--db", t.TempDir(), "--policy", p)
			if code != exitUndetermined || !strings.Contains(stderr, "--policy") {
				t.Fatalf("exit %d: %s", code, stderr)
			}
		})
	}
}

func TestVulnGateRefusesAmbiguousInvocations(t *testing.T) {
	t.Parallel()
	h := newVulnGateHarness(t)
	dir := buildOutput(t)
	for _, args := range [][]string{
		{"gate"},
		{"gate", dir, "--policy", "p.yaml", "--env", "prod"},
		{"gate", "a=" + dir, "a=" + dir},
	} {
		if _, stderr, code := h.exec(args...); code != exitUndetermined {
			t.Errorf("%v: exit %d, want %d: %s", args, code, exitUndetermined, stderr)
		}
	}
}

func hasEnvVar(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

func TestVulnGateTextNamesTheSBOMsWritten(t *testing.T) {
	t.Parallel()
	h := newVulnGateHarness(t)
	out := filepath.Join(t.TempDir(), "out")
	stdout, stderr, code := h.exec("gate", "site="+buildOutput(t), "--db", t.TempDir(), "--out", out)
	if code != 0 || !strings.Contains(stdout, "sbom: "+filepath.Join(out, "site.cdx.json")+" (site)") {
		t.Fatalf("exit %d:\n%s\n%s", code, stdout, stderr)
	}
}
