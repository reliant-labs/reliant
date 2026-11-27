package cli

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/internal/sbom"
	"github.com/reliant-labs/forge/internal/sbomoci"
	"github.com/reliant-labs/forge/internal/sbomoci/ocitest"
	"github.com/reliant-labs/forge/internal/vulnscan"
	"github.com/reliant-labs/forge/pkg/release"
)

// fakeVulnService is control-plane's VulnerabilityService and the
// vulnerability half of DeployService/GetCapabilities, speaking Connect's
// JSON binding with the proto's field names spelled literally (a typo in the
// client's wire shapes fails the test rather than moving with it). Every
// other procedure goes to next (a fakeDeployService), when set.
type fakeVulnService struct {
	mu sync.Mutex
	// features is GetCapabilities.vulnerabilityFeatures; noCapabilities
	// answers GetCapabilities Unimplemented (a control plane predating it).
	features       []string
	noCapabilities bool
	// unimplemented answers every VulnerabilityService rpc Unimplemented.
	unimplemented bool
	// refuse answers every read PermissionDenied.
	refuse bool
	// digestOverride is what RecordReleaseSBOM answers as the digest,
	// instead of the one it derives from the bytes.
	digestOverride string
	// records are the RecordReleaseSBOM bodies, in order.
	records []map[string]any
	// releaseVulns is GetReleaseVulnerabilities' answer, per version.
	releaseVulns map[string]map[string]any
	// summary is GetVulnerabilitySummary's environments.
	summary []map[string]any
	// advisory is GetVulnerability's answer, by advisory id.
	advisory map[string]map[string]any
	calls    []string
	next     http.Handler
	// hold, when set, holds every RecordReleaseSBOM answer until it is
	// closed or the request is abandoned (a slow upload); arrived gets a
	// value as each such request comes in.
	hold, arrived chan struct{}
}

func (f *fakeVulnService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const svc = "/controlplane.v1.VulnerabilityService/"
	if r.URL.Path != "/controlplane.v1.DeployService/GetCapabilities" && !strings.HasPrefix(r.URL.Path, svc) {
		if f.next == nil {
			connectErr(w, http.StatusNotFound, "unimplemented", "no such procedure "+r.URL.Path)
			return
		}
		f.next.ServeHTTP(w, r)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	if f.hold != nil && r.URL.Path == svc+"RecordReleaseSBOM" {
		f.arrived <- struct{}{}
		select {
		case <-f.hold:
		case <-r.Context().Done():
			return // the client gave up: nothing was recorded
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.TrimPrefix(r.URL.Path, "/"))
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/controlplane.v1.DeployService/GetCapabilities" {
		if f.noCapabilities {
			connectErr(w, http.StatusNotFound, "unimplemented", "no such procedure")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"vulnerabilityFeatures": f.features})
		return
	}
	if f.unimplemented {
		connectErr(w, http.StatusNotImplemented, "unimplemented", "not implemented")
		return
	}
	str := func(k string) string { s, _ := body[k].(string); return s }
	switch strings.TrimPrefix(r.URL.Path, svc) {
	case "RecordReleaseSBOM":
		f.records = append(f.records, body)
		gz, err := base64.StdEncoding.DecodeString(str("sbom"))
		if err != nil || str("encoding") != "SBOM_ENCODING_GZIP" {
			connectErr(w, http.StatusBadRequest, "invalid_argument", "sbom is not gzip base64")
			return
		}
		zr, err := gzip.NewReader(bytes.NewReader(gz))
		if err != nil {
			connectErr(w, http.StatusBadRequest, "invalid_argument", "sbom is not gzip")
			return
		}
		doc, _ := io.ReadAll(zr)
		sum := sha256.Sum256(doc)
		digest := "sha256:" + hex.EncodeToString(sum[:])
		if f.digestOverride != "" {
			digest = f.digestOverride
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"digest": digest, "created": true, "scanState": "VULNERABILITY_SCAN_STATE_PENDING", "componentCount": 3})
	case "GetVulnerabilitySummary":
		if f.refuse {
			connectErr(w, http.StatusForbidden, "permission_denied", "missing scope vuln:read")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"environments": f.summary})
	case "GetReleaseVulnerabilities":
		if f.refuse {
			connectErr(w, http.StatusForbidden, "permission_denied", "missing scope vuln:read")
			return
		}
		resp, ok := f.releaseVulns[str("version")]
		if !ok {
			connectErr(w, http.StatusNotFound, "not_found", "no such release")
			return
		}
		_ = json.NewEncoder(w).Encode(resp)
	case "GetVulnerability":
		resp, ok := f.advisory[str("advisoryId")]
		if !ok {
			connectErr(w, http.StatusNotFound, "not_found", "no such advisory")
			return
		}
		_ = json.NewEncoder(w).Encode(resp)
	default:
		connectErr(w, http.StatusNotFound, "unimplemented", "no such procedure "+r.URL.Path)
	}
}

func (f *fakeVulnService) count(proc string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == proc {
			n++
		}
	}
	return n
}

// hostedSBOMCut is a hosted store over fake (with a fakeDeployService behind
// it), whose machine ledger holds one SBOM per named artifact, and the
// release those SBOMs belong to.
func hostedSBOMCut(t *testing.T, fake *fakeVulnService, names ...string) (*hostedStore, release.Release, *bytes.Buffer) {
	t.Helper()
	fake.next = newFakeDeployService(map[string]string{"prod": "env-prod-id"})
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	ep := cloud.Endpoint{Env: "prod", URL: srv.URL, TokenEnv: "T"}
	l := hostedLedger(cloud.NewClient(ep, cloud.Credential{Token: "rlat_test"}), srv.URL, "acme", deploytarget.HostedEnvPersistent)
	hs := l.Bindings.(*hostedStore)
	store, err := ledgerfile.Open(t.TempDir(), "hosted-vuln-test")
	if err != nil {
		t.Fatal(err)
	}
	hs.sboms = func() (sbomBytesReader, error) { return store, nil }
	var warn bytes.Buffer
	hs.warn = &warn
	r := release.Release{Version: "v1.0.0", Artifacts: map[string]release.Artifact{}}
	for i, name := range names {
		digest, err := store.PutSBOM([]byte(`{"bomFormat":"CycloneDX","specVersion":"1.6","serialNumber":"` + name + `"}`))
		if err != nil {
			t.Fatal(err)
		}
		r.Artifacts[name] = release.Artifact{
			Kind: release.KindOCI, Mode: release.ModeShared,
			Digests: map[string]string{release.SharedVariant: sha(string("0123456789"[i]))},
			SBOM: &release.SBOMRef{Digest: digest, Status: release.SBOMStatusOK, Format: release.SBOMFormatCycloneDXJSON,
				Generator: "syft@v1.57.2", Source: release.SBOMSourceRegistry, Referrer: "registry.test/acme/" + name + "@" + sha("f")},
		}
	}
	// One artifact without an SBOM, which is never uploaded.
	r.Artifacts["bare"] = release.Artifact{Kind: release.KindOCI, Mode: release.ModeShared,
		Digests: map[string]string{release.SharedVariant: sha("e")},
		SBOM:    &release.SBOMRef{Status: release.SBOMStatusUnavailable, Reason: "syft found no packages"}}
	return hs, r, &warn
}

func TestHostedCutUploadsEachSBOM(t *testing.T) {
	t.Parallel()
	// vuln-sbom-upload alone: a control plane that takes SBOMs before it
	// serves their findings.
	fake := &fakeVulnService{features: []string{hostedVulnFeatureSBOMUpload}}
	hs, r, warn := hostedSBOMCut(t, fake, "api", "worker")
	if _, err := hs.Cut(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if warn.Len() != 0 {
		t.Fatalf("a clean upload printed:\n%s", warn)
	}
	if len(fake.records) != 2 {
		t.Fatalf("RecordReleaseSBOM calls = %d, want 2 (api, worker; never the artifact without an SBOM)", len(fake.records))
	}
	got := fake.records[0]
	ref := r.Artifacts["api"].SBOM
	for k, want := range map[string]string{
		"version": "v1.0.0", "project": "acme", "artifactName": "api", "encoding": "SBOM_ENCODING_GZIP",
		"format": ref.Format, "generator": ref.Generator, "source": ref.Source, "referrer": ref.Referrer,
	} {
		if got[k] != want {
			t.Errorf("request %s = %v, want %q", k, got[k], want)
		}
	}
	if _, ok := got["orgId"]; ok {
		t.Errorf("orgId sent: the org is the credential's, as on DeployService")
	}
	// A re-cut uploads again (the server is idempotent): the backfill path.
	if _, err := hs.Cut(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if len(fake.records) != 4 || warn.Len() != 0 {
		t.Fatalf("re-cut: %d uploads, output %q", len(fake.records), warn)
	}
	if n := fake.count("controlplane.v1.DeployService/GetCapabilities"); n != 1 {
		t.Errorf("GetCapabilities asked %d times, want once per store", n)
	}
}

func TestHostedCutWarnsOnADigestMismatch(t *testing.T) {
	t.Parallel()
	fake := &fakeVulnService{features: []string{hostedVulnFeatureFindings}, digestOverride: sha("9")}
	hs, r, warn := hostedSBOMCut(t, fake, "api")
	if _, err := hs.Cut(context.Background(), r); err != nil {
		t.Fatalf("a digest mismatch failed the cut: %v", err)
	}
	if !strings.Contains(warn.String(), sha("9")) || !strings.Contains(warn.String(), r.Artifacts["api"].SBOM.Digest) {
		t.Fatalf("warning does not name both digests:\n%s", warn)
	}
}

func TestHostedCutNotesAnUnimplementedSBOMStoreOnce(t *testing.T) {
	t.Parallel()
	fake := &fakeVulnService{features: []string{hostedVulnFeatureFindings}, unimplemented: true}
	hs, r, warn := hostedSBOMCut(t, fake, "api", "worker")
	if _, err := hs.Cut(context.Background(), r); err != nil {
		t.Fatalf("Unimplemented failed the cut: %v", err)
	}
	if n := strings.Count(warn.String(), "does not store SBOMs yet"); n != 1 || strings.Count(warn.String(), "\n") != 1 {
		t.Fatalf("want exactly one note, got:\n%s", warn)
	}
	if n := fake.count(procRecordReleaseSBOM); n != 1 {
		t.Errorf("RecordReleaseSBOM called %d times after Unimplemented, want 1", n)
	}
}

func TestHostedCutSkipsTheUploadWithoutTheCapability(t *testing.T) {
	t.Parallel()
	for name, fake := range map[string]*fakeVulnService{
		"features withheld":     {features: []string{}},
		"predates capabilities": {noCapabilities: true},
		// The gate is not an upload: enforcing it names nothing forge sends.
		"only enforces-vuln-gate": {features: []string{hostedVulnFeatureEnforcesGate}},
	} {
		hs, r, warn := hostedSBOMCut(t, fake, "api")
		if _, err := hs.Cut(context.Background(), r); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if n := fake.count(procRecordReleaseSBOM); n != 0 || warn.Len() != 0 {
			t.Errorf("%s: %d uploads, output %q; want none, silently", name, n, warn)
		}
	}
}

// A large SBOM over a slow link outlasts the client's default timeout: the
// upload runs under its own, scaled to its size. Ordered by events, not
// durations: the upload is held open until a second call under the default
// timeout has been held as long and abandoned, then answered.
func TestHostedCutUploadOutlastsTheDefaultTimeout(t *testing.T) {
	t.Parallel()
	fake := &fakeVulnService{features: []string{hostedVulnFeatureFindings}, hold: make(chan struct{}), arrived: make(chan struct{}, 2)}
	hs, r, warn := hostedSBOMCut(t, fake, "api")
	client := hs.client.(*cloud.Client)
	client.HTTP.Timeout = 100 * time.Millisecond

	cut := make(chan error, 1)
	go func() {
		_, err := hs.Cut(context.Background(), r)
		cut <- err
	}()
	<-fake.arrived // the upload is in flight, held
	// The test bites: a call under the default timeout, held the same way,
	// fails — and the held upload is still open after it has.
	probe := make(chan error, 1)
	go func() {
		probe <- client.Call(context.Background(), procRecordReleaseSBOM, map[string]any{"version": "v", "artifactName": "a", "sbom": []byte("x")}, nil)
	}()
	<-fake.arrived
	if err := <-probe; err == nil {
		t.Fatal("a held call under the default timeout succeeded; the test does not exercise the timeout")
	}
	select {
	case err := <-cut:
		t.Fatalf("the upload ended before it was answered (%v): it ran under the default timeout", err)
	default:
	}
	close(fake.hold)
	if err := <-cut; err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	// The abandoned probe may or may not be recorded (the server learns of a
	// client that gave up asynchronously); the upload must be, once.
	uploads := 0
	for _, rec := range fake.records {
		if rec["artifactName"] == "api" {
			uploads++
		}
	}
	if uploads != 1 || warn.Len() != 0 {
		t.Fatalf("upload under its own timeout: %d recorded, output:\n%s", uploads, warn)
	}
}

func TestSBOMUploadTimeoutScalesWithSize(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		gzipped int
		want    time.Duration
	}{
		{0, 30 * time.Second},
		{1 << 10, 30 * time.Second},
		{3 << 20, 30*time.Second + 8*time.Second},   // 4 MiB on the wire
		{32 << 20, 30*time.Second + 85*time.Second}, // the cap: ~43 MiB on the wire
		{1 << 30, 10 * time.Minute},                 // capped
	} {
		if got := sbomUploadTimeout(tc.gzipped); got != tc.want {
			t.Errorf("sbomUploadTimeout(%d) = %s, want %s", tc.gzipped, got, tc.want)
		}
	}
}

// vuln-findings alone (a control plane that named findings before upload had
// its own feature) still takes the upload: findings are scans of uploads.
func TestHostedCutUploadsUnderVulnFindingsForCompatibility(t *testing.T) {
	t.Parallel()
	fake := &fakeVulnService{features: []string{hostedVulnFeatureFindings}}
	hs, r, warn := hostedSBOMCut(t, fake, "api")
	if _, err := hs.Cut(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if n := fake.count(procRecordReleaseSBOM); n != 1 || warn.Len() != 0 {
		t.Fatalf("%d uploads, output %q; want 1, silently", n, warn)
	}
}

func TestHostedVulnPlanLine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		features         []string
		upload, findings string
		uploads, serves  bool
	}{
		{nil, "does not take SBOMs yet", "does not serve findings yet", false, false},
		{[]string{hostedVulnFeatureSBOMUpload}, "keeps and scans this release's SBOMs", "does not serve findings yet", true, false},
		{[]string{hostedVulnFeatureFindings}, "keeps and scans this release's SBOMs", "does not judge vuln_gate at promote yet", true, true},
		{[]string{hostedVulnFeatureSBOMUpload, hostedVulnFeatureFindings, hostedVulnFeatureEnforcesGate}, "keeps and scans", "and judges vuln_gate at promote", true, true},
	} {
		fake := &fakeVulnService{features: tc.features}
		hs, _, _ := hostedSBOMCut(t, fake)
		v := hostedVulnPlan(context.Background(), envLedger{Bindings: hs, Releases: hs})
		if v.SBOMUpload != tc.uploads || v.Findings != tc.serves {
			t.Errorf("features %v: sbom_upload %v findings %v, want %v %v", tc.features, v.SBOMUpload, v.Findings, tc.uploads, tc.serves)
		}
		var out bytes.Buffer
		renderPlanVulns(&out, v)
		lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
		if len(lines) != 2 || !strings.HasPrefix(lines[0], "  sboms     ") || !strings.Contains(lines[0], tc.upload) ||
			!strings.HasPrefix(lines[1], "  vulns     ") || !strings.Contains(lines[1], tc.findings) {
			t.Errorf("features %v: plan lines\n%s\nwant sboms %q, vulns %q", tc.features, out.String(), tc.upload, tc.findings)
		}
	}
	if v := hostedVulnPlan(context.Background(), envLedger{Bindings: memBindingStoreForVulnPlan{}}); v != nil {
		t.Errorf("a machine-ledger env got a vulns line: %+v", v)
	}
}

// memBindingStoreForVulnPlan is any binding store that is not a control plane.
type memBindingStoreForVulnPlan struct{ bindingStore }

// ─── forge vuln on a control-plane env ──────────────────────────────────────

// hostedVulnHarness is a vulnHarness whose env records on fake.
func hostedVulnHarness(t *testing.T, fake *fakeVulnService) *vulnHarness {
	t.Helper()
	h := newVulnHarness(t)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	h.env.Endpoint = cloud.DefaultEndpoint
	h.env.Project = "acme"
	h.deps.hostedClient = func(context.Context, vulnEnv) (cloudCaller, error) {
		return cloud.NewClient(cloud.Endpoint{Env: "prod", URL: srv.URL}, cloud.Credential{Token: "rlat_test"}), nil
	}
	return h
}

// hostedFindings is a control plane holding release v1.0.0 in prod: api
// scanned with a fixable critical (blocking) and a low; web never given an
// SBOM.
func hostedFindings(sbomDigest string) *fakeVulnService {
	firstSeen := vulnTestNow.Add(-10 * 24 * time.Hour)
	return &fakeVulnService{
		features: []string{hostedVulnFeatureFindings},
		summary:  []map[string]any{{"environmentId": "env-prod", "environmentName": "prod", "releaseVersion": "v1.0.0"}},
		releaseVulns: map[string]map[string]any{"v1.0.0": {
			"releaseVersion": "v1.0.0",
			"artifacts": []map[string]any{
				{"artifactName": "api", "sbomDigest": sbomDigest, "referrer": "registry.test/acme/api@" + sha("f"), "scanState": "VULNERABILITY_SCAN_STATE_SCANNED", "lastScannedAt": vulnTestNow.Add(-time.Hour)},
				{"artifactName": "web", "scanState": "VULNERABILITY_SCAN_STATE_NO_SBOM"},
			},
			"findings": []map[string]any{
				{
					"advisory":      map[string]any{"id": "CVE-2026-1111", "aliases": []string{"GHSA-aaaa-bbbb-cccc"}, "severity": "VULNERABILITY_SEVERITY_CRITICAL", "cvssScore": 9.8, "kev": false},
					"package":       map[string]any{"name": "golang.org/x/net", "version": "v0.17.0", "type": "go-module", "purl": "pkg:golang/golang.org/x/net@v0.17.0"},
					"fixedVersions": []string{"v0.23.0"}, "fixState": "VULNERABILITY_FIX_STATE_FIXED",
					"artifactNames": []string{"api"}, "firstSeenAt": firstSeen, "blocking": true,
				},
				{
					"advisory":      map[string]any{"id": "CVE-2026-2222", "severity": "VULNERABILITY_SEVERITY_LOW"},
					"package":       map[string]any{"name": "busybox", "version": "1.36", "type": "apk", "purl": "pkg:apk/alpine/busybox@1.36"},
					"fixState":      "VULNERABILITY_FIX_STATE_NOT_FIXED",
					"artifactNames": []string{"api"},
				},
			},
			"database": map[string]any{"scanner": "grype", "scannerVersion": "v0.122.1", "schema": "v6.1.10", "builtAt": vulnTestNow.Add(-6 * time.Hour)},
		}},
		advisory: map[string]map[string]any{"CVE-2026-1111": {
			"advisory":     map[string]any{"id": "CVE-2026-1111", "summary": "HTTP/2 rapid reset", "references": []string{"https://nvd.nist.gov/vuln/detail/CVE-2026-1111"}},
			"observations": []map[string]any{{"environmentId": "env-prod", "purl": "pkg:golang/golang.org/x/net@v0.17.0", "firstSeenAt": firstSeen, "firstReleaseVersion": "v0.9.0"}},
		}},
	}
}

// hostedPatchFindingsFake is a control plane holding release v1.0.0 in prod
// with findings in the patch harness's tree: ws in web (fixable), a
// react-router finding held at release scope (no artifact_names), and an OS
// package; worker was never given an SBOM.
func hostedPatchFindingsFake() *fakeVulnService {
	fake := hostedFindings("sha256:" + strings.Repeat("a", 64))
	rv := fake.releaseVulns["v1.0.0"]
	rv["artifacts"] = []map[string]any{
		{"artifactName": "web", "sbomDigest": "sha256:" + strings.Repeat("a", 64), "scanState": "VULNERABILITY_SCAN_STATE_SCANNED", "lastScannedAt": vulnTestNow.Add(-time.Hour)},
		{"artifactName": "worker", "scanState": "VULNERABILITY_SCAN_STATE_NO_SBOM"},
	}
	rv["findings"] = []map[string]any{
		{
			"advisory":      map[string]any{"id": "GHSA-96hv-2xvq-fx4p", "severity": "VULNERABILITY_SEVERITY_HIGH"},
			"package":       map[string]any{"name": "ws", "version": "8.20.0", "type": "npm", "purl": "pkg:npm/ws@8.20.0", "location": "/web/package-lock.json"},
			"fixedVersions": []string{"8.21.0"}, "fixState": "VULNERABILITY_FIX_STATE_FIXED",
			"artifactNames": []string{"web"}, "blocking": true,
		},
		{
			"advisory":      map[string]any{"id": "GHSA-chx6-hx7r-mcp5", "severity": "VULNERABILITY_SEVERITY_HIGH"},
			"package":       map[string]any{"name": "react-router", "version": "7.14.0", "type": "npm", "purl": "pkg:npm/react-router@7.14.0"},
			"fixedVersions": []string{"7.18.0"}, "fixState": "VULNERABILITY_FIX_STATE_FIXED",
		},
		{
			"advisory":      map[string]any{"id": "CVE-2026-4738", "severity": "VULNERABILITY_SEVERITY_MEDIUM"},
			"package":       map[string]any{"name": "zlib", "version": "1.3", "type": "apk", "purl": "pkg:apk/alpine/zlib@1.3"},
			"fixedVersions": []string{"1.3.1"}, "fixState": "VULNERABILITY_FIX_STATE_FIXED",
			"artifactNames": []string{"web"},
		},
	}
	return fake
}

// hostedPatchHarness is the patch harness pointed at a control plane: prod
// records there, so the findings must come from fake, never this machine.
func hostedPatchHarness(t *testing.T, fake *fakeVulnService) *patchHarness {
	t.Helper()
	h := newPatchHarness(t)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	h.env.Endpoint = cloud.DefaultEndpoint
	h.env.Project = "acme"
	h.deps.hostedClient = func(context.Context, vulnEnv) (cloudCaller, error) {
		return cloud.NewClient(cloud.Endpoint{Env: "prod", URL: srv.URL}, cloud.Credential{Token: "rlat_test"}), nil
	}
	return h
}

// `forge vuln patch <env>` on an env that records on a control plane plans
// its bumps from the control plane's findings: the release it says the env
// runs, a release-level finding included, nothing scanned on this machine.
// It is gated the way list is: no vuln-findings capability is exit 2 with
// list's message, a refusal exit 4.
func TestVulnPatchReadsTheControlPlane(t *testing.T) {
	t.Parallel()
	h := hostedPatchHarness(t, hostedPatchFindingsFake())
	doc, _, stderr, code := h.patch("prod")
	if code != exitUndetermined {
		t.Fatalf("exit %d, want 2 (the OS package has no patcher): %s", code, stderr)
	}
	want := map[string]string{
		"CVE-2026-4738 zlib":               "skipped",
		"GHSA-96hv-2xvq-fx4p ws":           "bumped",
		"GHSA-chx6-hx7r-mcp5 react-router": "bumped",
	}
	if got := statuses(doc); !reflect.DeepEqual(got, want) {
		t.Errorf("statuses = %v, want %v", got, want)
	}
	if doc.Env != "prod" || doc.Release != "v1.0.0" || !doc.Changed {
		t.Errorf("doc head = %s %s changed=%v", doc.Env, doc.Release, doc.Changed)
	}
	if wantCmds := []string{"web: npm install", "web: npm install react-router@7.18.0"}; !reflect.DeepEqual(h.cmds, wantCmds) {
		t.Errorf("commands = %v, want %v", h.cmds, wantCmds)
	}
	pkg, _ := os.ReadFile(filepath.Join(h.dir, "web/package.json"))
	if !strings.Contains(string(pkg), `"ws": "8.21.0"`) {
		t.Errorf("package.json does not override ws:\n%s", pkg)
	}
	notes := strings.Join(doc.Notes, "\n")
	for _, n := range []string{"worker was not scanned", "findings: the control plane at " + cloud.DefaultEndpoint} {
		if !strings.Contains(notes, n) {
			t.Errorf("notes lack %q:\n%s", n, notes)
		}
	}
	if h.scans != 0 || len(h.installs) != 0 {
		t.Errorf("scanned (%d) or installed (%v) on this machine: the control plane's findings are the record", h.scans, h.installs)
	}
	if doc, _, stderr, code = hostedPatchHarness(t, hostedPatchFindingsFake()).patch("prod", "--ecosystem", "npm"); code != 0 || len(doc.Findings) != 2 {
		t.Errorf("--ecosystem npm: exit %d findings %d: %s", code, len(doc.Findings), stderr)
	}

	// No findings capability: list's "could not determine", nothing patched.
	fake := hostedPatchFindingsFake()
	fake.features = nil
	h = hostedPatchHarness(t, fake)
	if _, _, stderr, code := h.patch("prod"); code != exitUndetermined || !strings.Contains(stderr, hostedVulnFindingsNotServedMsg) || len(h.cmds) != 0 {
		t.Errorf("no vuln-findings: exit %d, commands %v\n%s", code, h.cmds, stderr)
	}
	// No vuln:read: refused, nothing patched.
	fake = hostedPatchFindingsFake()
	fake.refuse = true
	h = hostedPatchHarness(t, fake)
	if _, _, stderr, code := h.patch("prod"); code != exitRefused || !strings.Contains(stderr, "vuln:read") || len(h.cmds) != 0 {
		t.Errorf("refused: exit %d, commands %v\n%s", code, h.cmds, stderr)
	}
}

func TestVulnListReadsTheControlPlane(t *testing.T) {
	t.Parallel()
	fake := hostedFindings("sha256:" + strings.Repeat("a", 64))
	h := hostedVulnHarness(t, fake)
	out, stderr, code := h.exec("list", "prod", "--json")
	if code != exitWrong {
		t.Fatalf("exit %d, want 1 (a fixable critical blocks)\n%s\n%s", code, out, stderr)
	}
	doc := decodeVulnDoc(t, out)
	if doc.Env != "prod" || doc.Release != "v1.0.0" {
		t.Errorf("doc head = %s %s", doc.Env, doc.Release)
	}
	if doc.Coverage.Artifacts != 2 || doc.Coverage.WithSBOM != 1 || doc.Coverage.Scanned != 1 || len(doc.Coverage.Unscanned) != 1 {
		t.Errorf("coverage = %+v", doc.Coverage)
	}
	if doc.Gate.Verdict != vulnscan.VerdictBlock || strings.Join(doc.Gate.Blocking, ",") != "CVE-2026-1111" {
		t.Errorf("gate = %+v", doc.Gate)
	}
	if doc.DB.Version != "v0.122.1" || doc.DB.Schema != "v6.1.10" || doc.DB.Stale {
		t.Errorf("db = %+v", doc.DB)
	}
	if doc.Counts.Critical != 1 || doc.Counts.Low != 1 || len(doc.Findings) != 2 {
		t.Fatalf("counts %+v, findings %d", doc.Counts, len(doc.Findings))
	}
	f := doc.Findings[0]
	if f.ID != "CVE-2026-1111" || f.Severity != vulnscan.SeverityCritical || !f.Fixable() || strings.Join(f.FixedIn, ",") != "v0.23.0" ||
		f.Package.PURL != "pkg:golang/golang.org/x/net@v0.17.0" || f.CVSS == nil || *f.CVSS != 9.8 ||
		!f.FirstSeen.Equal(vulnTestNow.Add(-10*24*time.Hour)) || f.Action != vulnscan.ActionBlock || strings.Join(f.Artifacts, ",") != "api" {
		t.Errorf("finding = %+v", f)
	}
	if h.scans != 0 {
		t.Errorf("scanned on this machine: the control plane's findings are the record")
	}
	text, _, _ := h.exec("list", "prod")
	for _, want := range []string{"vuln_gate (audit): BLOCK: CVE-2026-1111", "not scanned: web (no SBOM recorded on the control plane)",
		"→ https://forge.reliantlabs.io/forge/p/acme/vulnerabilities"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(text), "/forge/p/acme/vulnerabilities") {
		t.Errorf("text does not end with the console link:\n%s", text)
	}
	// scan --deployed reads the same record.
	if out, _, code := h.exec("scan", "--deployed", "prod", "--json"); code != exitWrong || decodeVulnDoc(t, out).Release != "v1.0.0" {
		t.Errorf("scan --deployed: exit %d", code)
	}
	if h.scans != 0 || len(h.installs) != 0 {
		t.Errorf("scan --deployed scanned (%d) or installed (%v) on this machine", h.scans, h.installs)
	}
}

func TestVulnShowReadsTheAdvisoryFromTheControlPlane(t *testing.T) {
	t.Parallel()
	h := hostedVulnHarness(t, hostedFindings("sha256:"+strings.Repeat("a", 64)))
	out, _, code := h.exec("show", "prod", "GHSA-aaaa-bbbb-cccc")
	if code != exitWrong {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, want := range []string{"CVE-2026-1111", "golang.org/x/net v0.17.0", "fixed in v0.23.0"} {
		if !strings.Contains(out, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}
	out, _, _ = h.exec("show", "prod", "CVE-2026-1111")
	for _, want := range []string{"HTTP/2 rapid reset", "https://nvd.nist.gov/vuln/detail/CVE-2026-1111", "first seen 2026-11-10 (release v0.9.0)"} {
		if !strings.Contains(out, want) {
			t.Errorf("show by id lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "CVE-2026-2222") {
		t.Errorf("show listed an unrelated finding:\n%s", out)
	}
}

func TestVulnOnAControlPlaneWithoutVulnReadIsRefused(t *testing.T) {
	t.Parallel()
	fake := hostedFindings("")
	fake.refuse = true
	h := hostedVulnHarness(t, fake)
	for _, args := range [][]string{{"list", "prod"}, {"show", "prod", "CVE-1"}, {"sbom", "prod"}, {"list", "prod", "--release", "v1.0.0"}} {
		_, stderr, code := h.exec(args...)
		if code != exitRefused || !strings.Contains(stderr, "vuln:read") {
			t.Errorf("%v: exit %d, want 4\n%s", args, code, stderr)
		}
	}
}

func TestVulnOnAControlPlaneThatServesNoFindingsIsUndetermined(t *testing.T) {
	t.Parallel()
	for name, fake := range map[string]*fakeVulnService{
		"capability withheld": {features: []string{}},
		"predates it":         {noCapabilities: true},
		// Upload is not reads: a control plane that takes SBOMs but has
		// not said it serves findings is not read.
		"upload only":       {features: []string{hostedVulnFeatureSBOMUpload, hostedVulnFeatureEnforcesGate}},
		"rpc unimplemented": {features: []string{hostedVulnFeatureFindings}, unimplemented: true},
	} {
		h := hostedVulnHarness(t, fake)
		for _, args := range [][]string{{"list", "prod"}, {"show", "prod", "CVE-1"}, {"sbom", "prod"}, {"scan", "--deployed", "prod"}} {
			_, stderr, code := h.exec(args...)
			if code != exitUndetermined || !strings.Contains(stderr, hostedVulnFindingsNotServedMsg) {
				t.Errorf("%s %v: exit %d\n%s", name, args, code, stderr)
			}
		}
	}
}

func TestVulnSBOMOnAControlPlaneReadsThisMachinesCopy(t *testing.T) {
	t.Parallel()
	h := newVulnHarness(t)
	data := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6","components":[]}`)
	digest, err := h.store.PutSBOM(data)
	if err != nil {
		t.Fatal(err)
	}
	fake := hostedFindings(digest)
	h2 := hostedVulnHarness(t, fake)
	h2.store = h.store
	h2.deps.machineLedger = func(string) (*ledgerfile.Store, error) { return h.store, nil }
	out, stderr, code := h2.exec("sbom", "prod")
	if code != 0 || out != string(data) {
		t.Fatalf("exit %d\n%s\n%s", code, out, stderr)
	}
	// An SBOM this machine has no copy of names its referrer and exits 2.
	h3 := hostedVulnHarness(t, hostedFindings("sha256:"+strings.Repeat("d", 64)))
	_, stderr, code = h3.exec("sbom", "prod", "--artifact", "api")
	if code != exitUndetermined || !strings.Contains(stderr, "no copy") || !strings.Contains(stderr, "registry.test/acme/api@") {
		t.Fatalf("missing copy: exit %d\n%s", code, stderr)
	}
}

// hostedReferrerSBOM is a control plane holding release v1.0.0 whose one
// image, pushed to an in-process registry, has its SBOM beside it as an OCI
// referrer (attach=true) or none (attach=false), and a harness pointed at it
// whose machine ledger holds no copy: the release was cut elsewhere.
func hostedReferrerSBOM(t *testing.T, attach bool) (*vulnHarness, []byte) {
	t.Helper()
	prevCred := sbomRegistryCredential
	sbomRegistryCredential = func(string, string, *KCLEntities, string) (auth.CredentialFunc, error) {
		return func(context.Context, string) (auth.Credential, error) { return auth.EmptyCredential, nil }, nil
	}
	t.Cleanup(func() { sbomRegistryCredential = prevCred })

	reg := ocitest.New(t, nil)
	subject := reg.PutSubject("acme/api", "v1")
	repository, imageDigest, _ := strings.Cut(subject, "@")
	data := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6","components":[{"name":"x"}]}`)
	referrer := ""
	if attach {
		var err error
		referrer, err = sbomoci.Attach(context.Background(), subject, data, sbomoci.AttachOptions{
			Registry: sbomoci.RegistryOptions{Credential: func(context.Context, string) (auth.Credential, error) { return auth.EmptyCredential, nil }},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	fake := hostedFindings(sbom.Digest(data))
	deploy := newFakeDeployService(map[string]string{"prod": "env-prod"})
	fake.next = deploy
	rv := fake.releaseVulns["v1.0.0"]
	rv["artifacts"] = []map[string]any{{"artifactName": repository, "sbomDigest": sbom.Digest(data), "referrer": referrer, "scanState": "VULNERABILITY_SCAN_STATE_SCANNED"}}
	h := hostedVulnHarness(t, fake)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	cutter := hostedLedger(cloud.NewClient(cloud.Endpoint{Env: "prod", URL: srv.URL}, cloud.Credential{Token: "rlat_test"}), srv.URL, "acme", deploytarget.HostedEnvPersistent)
	if _, err := cutter.Releases.Cut(context.Background(), ociRelease("v1.0.0", map[string]string{repository: imageDigest})); err != nil {
		t.Fatal(err)
	}
	h.deps.hostedClient = func(context.Context, vulnEnv) (cloudCaller, error) {
		return cloud.NewClient(cloud.Endpoint{Env: "prod", URL: srv.URL}, cloud.Credential{Token: "rlat_test"}), nil
	}
	return h, data
}

// An SBOM cut on another machine is read from beside its image: the OCI
// referrer the control plane names, checked against the image digest the
// control plane's release records, verified against the recorded digest, and
// cached in this machine's ledger. Without a referrer, the answer is what it
// was: could not determine, naming why.
func TestVulnSBOMOnAControlPlaneReadsTheOCIReferrer(t *testing.T) {
	h, data := hostedReferrerSBOM(t, true)
	out, stderr, code := h.exec("sbom", "prod")
	if code != 0 || out != string(data) {
		t.Fatalf("exit %d, want the referrer's bytes\n%s\n%s", code, out, stderr)
	}
	if cached, err := h.store.GetSBOM(sbom.Digest(data)); err != nil || string(cached) != string(data) {
		t.Errorf("the referrer's bytes were not cached in this machine's ledger: %v", err)
	}

	h, _ = hostedReferrerSBOM(t, false)
	_, stderr, code = h.exec("sbom", "prod")
	if code != exitUndetermined || !strings.Contains(stderr, "no copy") || !strings.Contains(stderr, "could not be read from beside the image") {
		t.Fatalf("no referrer: exit %d\n%s", code, stderr)
	}
}

// The control plane's verdict wins over this checkout's vuln_gate, both
// ways: a finding the local policy would block does not when the control
// plane exempts it, and one the local policy would pass blocks when the
// control plane says it does (decision #4).
func TestVulnListTakesTheControlPlanesVerdictOverTheLocalPolicy(t *testing.T) {
	t.Parallel()
	fake := hostedFindings("sha256:" + strings.Repeat("a", 64))
	rv := fake.releaseVulns["v1.0.0"]
	findings := rv["findings"].([]map[string]any)
	// CVE-2026-1111 (fixable critical: the local rule blocks it) is exempt
	// on the control plane; CVE-2026-2222 (low, no fix: the local rule
	// passes it) blocks there. web is given a scanned SBOM so coverage is
	// complete.
	findings[0]["blocking"] = false
	findings[0]["exemption"] = map[string]any{"environmentId": "env-prod", "advisoryId": "CVE-2026-1111", "reason": "accepted on prod", "expiresAt": time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)}
	findings[1]["blocking"] = true
	rv["artifacts"].([]map[string]any)[1]["scanState"] = "VULNERABILITY_SCAN_STATE_SCANNED"
	rv["artifacts"].([]map[string]any)[1]["sbomDigest"] = "sha256:" + strings.Repeat("b", 64)
	h := hostedVulnHarness(t, fake)
	if a, _ := vulnscan.Judge(h.env.Gate, vulnscan.Finding{ID: "CVE-2026-1111", Severity: vulnscan.SeverityCritical, FixState: vulnscan.FixStateFixed}, vulnTestNow); a != vulnscan.ActionBlock {
		t.Fatalf("precondition: the local policy should block the fixable critical, got %s", a)
	}
	out, stderr, code := h.exec("list", "prod", "--include-exempt", "--json")
	if code != exitWrong {
		t.Fatalf("exit %d, want 1 (the control plane blocks CVE-2026-2222)\n%s\n%s", code, out, stderr)
	}
	doc := decodeVulnDoc(t, out)
	if strings.Join(doc.Gate.Blocking, ",") != "CVE-2026-2222" || doc.Gate.JudgedBy != vulnscan.JudgedByControlPlane {
		t.Fatalf("gate = %+v, want only the control plane's CVE-2026-2222, judged by it", doc.Gate)
	}
	for _, f := range doc.Findings {
		if f.ID == "CVE-2026-1111" && (f.Action != vulnscan.ActionNone || f.Exemption == nil || f.Exemption.Expires != "2026-12-31" || f.Exemption.Reason != "accepted on prod") {
			t.Errorf("CVE-2026-1111 = action %s exemption %+v, want the control plane's exemption through 2026-12-31", f.Action, f.Exemption)
		}
	}
	// And the reverse: nothing blocks on the control plane, so nothing
	// blocks here, though the local policy would block the critical.
	findings[1]["blocking"] = false
	if out, _, code := h.exec("list", "prod", "--json"); code != 0 || decodeVulnDoc(t, out).Gate.Verdict == vulnscan.VerdictBlock {
		t.Fatalf("exit %d, want 0: the control plane blocks nothing\n%s", code, out)
	}
}

// A control plane with no environment of the name asked for is never read
// release-wide: the read would carry neither the env's scope nor its
// vuln_gate. list (and show, scan --deployed) say could not determine, exit
// 2, and GetReleaseVulnerabilities is never called.
func TestVulnListOnAnEnvTheControlPlaneDoesNotHaveReadsNothing(t *testing.T) {
	t.Parallel()
	for name, summary := range map[string][]map[string]any{
		"no summary row":         nil,
		"a row naming no id":     {{"environmentName": "prod", "releaseVersion": "v1.0.0"}},
		"only another env's row": {{"environmentId": "env-staging", "environmentName": "staging", "releaseVersion": "v1.0.0"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := hostedFindings("sha256:" + strings.Repeat("a", 64))
			fake.summary = summary
			h := hostedVulnHarness(t, fake)
			out, stderr, code := h.exec("list", "prod", "--release", "v1.0.0", "--json")
			if code != exitUndetermined || out != "" || !strings.Contains(stderr, `env "prod" is not on the control plane`) {
				t.Fatalf("exit %d, want 2 and no document\n%s\n%s", code, out, stderr)
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			for _, c := range fake.calls {
				if strings.HasSuffix(c, "/GetReleaseVulnerabilities") {
					t.Errorf("read release-wide findings for an env the control plane does not have: %v", fake.calls)
				}
			}
		})
	}
}

// Whether a control-plane read is determined is the control plane's scan
// state, not this machine's clock: a database the control plane still calls
// fresh is fresh here at any age, an artifact it calls STALE (or a database
// it calls stale) makes the verdict "stale", and one it has not finished
// scanning makes it "unscanned". Both of those exit 2, gate.determined false.
func TestVulnListIsDeterminedByTheControlPlanesScanState(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		edit           func(rv map[string]any)
		wantVerdict    vulnscan.Verdict
		wantDetermined bool
		wantCode       int
		wantNote       string
	}{{
		name: "an old database the control plane calls fresh",
		edit: func(rv map[string]any) {
			rv["database"].(map[string]any)["builtAt"] = vulnTestNow.Add(-30 * 24 * time.Hour)
		},
		wantVerdict: vulnscan.VerdictPass, wantDetermined: true, wantCode: 0,
	}, {
		name:        "no database descriptor at all",
		edit:        func(rv map[string]any) { delete(rv, "database") },
		wantVerdict: vulnscan.VerdictPass, wantDetermined: true, wantCode: 0,
	}, {
		name: "a STALE artifact",
		edit: func(rv map[string]any) {
			rv["artifacts"].([]map[string]any)[0]["scanState"] = "VULNERABILITY_SCAN_STATE_STALE"
		},
		wantVerdict: vulnscan.VerdictStale, wantCode: exitUndetermined, wantNote: "judges its newest scan of api stale",
	}, {
		name:        "a stale database",
		edit:        func(rv map[string]any) { rv["database"].(map[string]any)["stale"] = true },
		wantVerdict: vulnscan.VerdictStale, wantCode: exitUndetermined, wantNote: "past its freshness bound",
	}, {
		name: "a scan still pending",
		edit: func(rv map[string]any) {
			a := rv["artifacts"].([]map[string]any)[0]
			a["scanState"] = "VULNERABILITY_SCAN_STATE_PENDING"
			delete(a, "lastScannedAt")
		},
		wantVerdict: vulnscan.VerdictUnscanned, wantCode: exitUndetermined,
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := hostedFindings("sha256:" + strings.Repeat("a", 64))
			rv := fake.releaseVulns["v1.0.0"]
			// Nothing blocks and every artifact has an SBOM, so the scan
			// state alone decides the verdict.
			rv["findings"] = []map[string]any{}
			rv["artifacts"] = rv["artifacts"].([]map[string]any)[:1]
			tc.edit(rv)
			h := hostedVulnHarness(t, fake)
			out, stderr, code := h.exec("list", "prod", "--json")
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\n%s\n%s", code, tc.wantCode, out, stderr)
			}
			doc := decodeVulnDoc(t, out)
			if doc.Gate.Verdict != tc.wantVerdict || doc.Gate.Determined != tc.wantDetermined {
				t.Errorf("gate = %+v, want %s determined=%v", doc.Gate, tc.wantVerdict, tc.wantDetermined)
			}
			if tc.wantNote != "" && !strings.Contains(strings.Join(doc.Notes, "\n"), tc.wantNote) {
				t.Errorf("notes %q lack %q", doc.Notes, tc.wantNote)
			}
		})
	}
}

// A finding the control plane holds at release scope (no artifact_names) is
// the release's: listed with no artifact, never spread onto every scanned
// artifact, and judged, counted and exited on exactly as an attributed one.
func TestVulnListKeepsAReleaseLevelFindingAtReleaseLevel(t *testing.T) {
	t.Parallel()
	run := func(t *testing.T, names []string) (vulnscan.Document, int, string) {
		fake := hostedFindings("sha256:" + strings.Repeat("a", 64))
		rv := fake.releaseVulns["v1.0.0"]
		// Two scanned artifacts, so spreading would be visible.
		rv["artifacts"] = []map[string]any{
			{"artifactName": "api", "sbomDigest": "sha256:" + strings.Repeat("a", 64), "scanState": "VULNERABILITY_SCAN_STATE_SCANNED", "lastScannedAt": vulnTestNow.Add(-time.Hour)},
			{"artifactName": "worker", "sbomDigest": "sha256:" + strings.Repeat("b", 64), "scanState": "VULNERABILITY_SCAN_STATE_SCANNED", "lastScannedAt": vulnTestNow.Add(-time.Hour)},
		}
		findings := rv["findings"].([]map[string]any)
		if names == nil {
			delete(findings[0], "artifactNames")
		} else {
			findings[0]["artifactNames"] = names
		}
		h := hostedVulnHarness(t, fake)
		out, _, code := h.exec("list", "prod", "--json")
		text, _, _ := h.exec("list", "prod")
		return decodeVulnDoc(t, out), code, text
	}
	doc, code, text := run(t, nil)
	var crit *vulnscan.DocumentFinding
	for i := range doc.Findings {
		if doc.Findings[i].ID == "CVE-2026-1111" {
			crit = &doc.Findings[i]
		}
	}
	if crit == nil {
		t.Fatalf("the release-level finding was dropped: %+v", doc.Findings)
	}
	if crit.Artifacts == nil || len(crit.Artifacts) != 0 {
		t.Errorf("artifacts = %#v, want an empty list: the control plane attributed it to no artifact", crit.Artifacts)
	}
	if crit.Action != vulnscan.ActionBlock || !crit.FirstSeen.Equal(vulnTestNow.Add(-10*24*time.Hour)) {
		t.Errorf("release-level finding = action %s first seen %s", crit.Action, crit.FirstSeen)
	}
	if !strings.Contains(text, "release") || strings.Contains(text, "api, worker") {
		t.Errorf("text does not render the finding at release level:\n%s", text)
	}
	// The same finding attributed to an artifact: the same verdict, counts
	// and exit code.
	attributed, attributedCode, _ := run(t, []string{"api"})
	if code != attributedCode || code != exitWrong {
		t.Errorf("exit %d (release level) vs %d (attributed), want both 1", code, attributedCode)
	}
	if doc.Counts != attributed.Counts || doc.Gate.Verdict != attributed.Gate.Verdict || strings.Join(doc.Gate.Blocking, ",") != strings.Join(attributed.Gate.Blocking, ",") {
		t.Errorf("release level: counts %+v gate %+v; attributed: counts %+v gate %+v", doc.Counts, doc.Gate, attributed.Counts, attributed.Gate)
	}
	// Attributed only to an artifact the control plane lists no scan of:
	// kept at release level, with a note, never dropped.
	doc, code, _ = run(t, []string{"ghost"})
	if code != exitWrong || doc.Counts.Critical != 1 || !strings.Contains(strings.Join(doc.Notes, "\n"), "ghost, which it lists no scan of") {
		t.Errorf("finding on an unlisted artifact: exit %d counts %+v notes %q", code, doc.Counts, doc.Notes)
	}
}

// Every VulnerabilityFixState and VulnerabilitySeverity value maps to a
// vulnscan value explicitly; UNKNOWN, UNSPECIFIED (absent on the wire) and a
// value this forge does not know are the unknown state, and an unknown
// severity is judged as critical.
func TestHostedEnumMappings(t *testing.T) {
	t.Parallel()
	for wire, want := range map[string]string{
		"VULNERABILITY_FIX_STATE_FIXED":           vulnscan.FixStateFixed,
		"VULNERABILITY_FIX_STATE_NOT_FIXED":       vulnscan.FixStateNotFixed,
		"VULNERABILITY_FIX_STATE_WONT_FIX":        vulnscan.FixStateWontFix,
		"VULNERABILITY_FIX_STATE_UNKNOWN":         vulnscan.FixStateUnknown,
		"VULNERABILITY_FIX_STATE_UNSPECIFIED":     vulnscan.FixStateUnknown,
		"":                                        vulnscan.FixStateUnknown,
		"VULNERABILITY_FIX_STATE_FROM_THE_FUTURE": vulnscan.FixStateUnknown,
	} {
		f := hostedFinding(wireVulnFinding{FixState: wire, FixedVersions: []string{"v2"}})
		if f.FixState != want || f.Fixable() != (want == vulnscan.FixStateFixed) {
			t.Errorf("fix state %q = %q (fixable %v), want %q", wire, f.FixState, f.Fixable(), want)
		}
	}
	for wire, want := range map[string]vulnscan.Severity{
		"VULNERABILITY_SEVERITY_CRITICAL":    vulnscan.SeverityCritical,
		"VULNERABILITY_SEVERITY_HIGH":        vulnscan.SeverityHigh,
		"VULNERABILITY_SEVERITY_MEDIUM":      vulnscan.SeverityMedium,
		"VULNERABILITY_SEVERITY_LOW":         vulnscan.SeverityLow,
		"VULNERABILITY_SEVERITY_NEGLIGIBLE":  vulnscan.SeverityNegligible,
		"VULNERABILITY_SEVERITY_UNKNOWN":     vulnscan.SeverityUnknown,
		"VULNERABILITY_SEVERITY_UNSPECIFIED": vulnscan.SeverityUnknown,
		"":                                   vulnscan.SeverityUnknown,
		"VULNERABILITY_SEVERITY_SEVERE":      vulnscan.SeverityUnknown,
	} {
		got := hostedFinding(wireVulnFinding{Advisory: wireVulnAdvisory{ID: "CVE-1", Severity: wire}}).Severity
		if got != want {
			t.Errorf("severity %q = %q, want %q", wire, got, want)
		}
		if want == vulnscan.SeverityUnknown && !got.AtLeast(string(vulnscan.SeverityCritical)) {
			t.Errorf("severity %q is not judged as critical", wire)
		}
	}
	// A missing severity on a fixable finding blocks under the default
	// gate, as a critical would.
	gate := release.DefaultDeliveryPolicy().VulnGate
	f := hostedFinding(wireVulnFinding{Advisory: wireVulnAdvisory{ID: "CVE-1"}, FixState: "VULNERABILITY_FIX_STATE_FIXED"})
	if a, _ := vulnscan.Judge(gate, f, vulnTestNow); a != vulnscan.ActionBlock {
		t.Errorf("a fixable finding with no severity: %s, want block", a)
	}
	for _, state := range []string{"VULNERABILITY_SCAN_STATE_UNSPECIFIED", "", "VULNERABILITY_SCAN_STATE_FROM_THE_FUTURE"} {
		scans, _, _, _ := hostedArtifactScans(wireReleaseVulns{Artifacts: []wireReleaseArtifactSBOM{{ArtifactName: "api", SBOMDigest: "sha256:aa", ScanState: state, LastScannedAt: vulnTestNow}}})
		if len(scans) != 1 || scans[0].Latest != nil || !strings.HasPrefix(scans[0].Reason, "not scanned") {
			t.Errorf("scan state %q = %+v, want unscanned", state, scans)
		}
	}
}

// oneRelease is a release ledger holding rel.
type oneRelease struct {
	emptyReleases
	rel *release.Release
}

func (o oneRelease) Get(_ context.Context, v string) (*release.Release, error) {
	if o.rel != nil && o.rel.Version == v {
		return o.rel, nil
	}
	return nil, nil
}

// ledgerOnlyVulnGates judges v1.0.0's promotion to a ledger-only prod (forge
// applies, the control plane converges nothing) under the default policy, with
// fake as the control plane. The release, as a control plane returns it,
// carries no SBOM refs.
func ledgerOnlyVulnGates(t *testing.T, fake *fakeVulnService) ([]release.Gate, string) {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	l := hostedLedger(cloud.NewClient(cloud.Endpoint{Env: "prod", URL: srv.URL}, cloud.Credential{Token: "rlat_test"}), srv.URL, "acme", deploytarget.HostedEnvSelfManaged)
	oci := func(d string) release.Artifact {
		return release.Artifact{Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{release.SharedVariant: sha(d)}}
	}
	// Cut by a forge that records SBOMs: the absence of one is judged.
	rel := &release.Release{Version: "v1.0.0", Artifacts: map[string]release.Artifact{"api": oci("1"), "web": oci("2")},
		Provenance: &release.Provenance{ForgeVersion: gatingPin}}
	ledger := envLedger{Bindings: l.Bindings, Releases: oneRelease{rel: rel}, Hosted: true, Mixed: true}
	if !ledger.ledgerOnly() {
		t.Fatal("the fixture is not a ledger-only env")
	}
	pol := release.DefaultDeliveryPolicy()
	var out bytes.Buffer
	gates, err := checkPromotionVulnGates(context.Background(), &out, t.TempDir(), ledger, &pol, promotionCheckInput{Env: "prod", Version: "v1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if rel.Artifacts["api"].SBOM != nil {
		t.Error("the ledger's release was changed in place")
	}
	return gates, out.String()
}

// The promote check of a ledger-only env (design §1.5) judges the control
// plane's findings with the control plane's verdict on each (Judged), not
// this checkout's rule, and judges provenance_sbom against the SBOMs the
// control plane records.
func TestLedgerOnlyPromoteJudgesTheControlPlanesFindings(t *testing.T) {
	t.Parallel()
	fake := hostedFindings("sha256:" + strings.Repeat("a", 64))
	findings := fake.releaseVulns["v1.0.0"]["findings"].([]map[string]any)
	// The control plane's verdict differs from the local rule both ways:
	// the fixable critical is exempt there, and the low blocks.
	findings[0]["blocking"] = false
	findings[0]["exemption"] = map[string]any{"environmentId": "env-prod", "advisoryId": "CVE-2026-1111", "reason": "accepted on prod", "expiresAt": "2027-01-01T00:00:00Z"}
	findings[1]["blocking"] = true
	gates, out := ledgerOnlyVulnGates(t, fake)

	vg := gateNamed(gates, vulnGateGateName)
	if vg == nil {
		t.Fatalf("no vuln_gate gate recorded: %+v\n%s", gates, out)
	}
	if b := vg.Details["blocking"].([]string); strings.Join(b, ",") != "CVE-2026-2222" || vg.Details["judged_by"] != vulnscan.JudgedByControlPlane || vg.Details["outcome"] != "audit" {
		t.Errorf("vuln_gate = %+v, want the control plane's blocking CVE-2026-2222, judged there, audited", vg.Details)
	}
	sg := gateNamed(gates, sbomGateName)
	if sg == nil || strings.Join(sg.Details["missing"].([]string), ",") != "web (no SBOM)" {
		t.Errorf("provenance_sbom = %+v, want only web missing (api's SBOM is the control plane's)", sg)
	}

	// A control plane that enforces vuln_gate itself judges in its own
	// transaction: forge records nothing and reads no findings.
	fake = hostedFindings("sha256:" + strings.Repeat("a", 64))
	fake.features = append(fake.features, hostedVulnFeatureEnforcesGate)
	if gates, _ := ledgerOnlyVulnGates(t, fake); len(gates) != 0 {
		t.Errorf("an enforcing control plane: forge recorded %+v", gates)
	}
	fake.mu.Lock()
	for _, c := range fake.calls {
		if strings.Contains(c, "GetReleaseVulnerabilities") {
			t.Errorf("an enforcing control plane's findings were read: %v", fake.calls)
		}
	}
	fake.mu.Unlock()

	// One with no environment of that name is never read release-wide:
	// every artifact is unscanned, with the reason.
	fake = hostedFindings("sha256:" + strings.Repeat("a", 64))
	fake.summary = nil
	gates, _ = ledgerOnlyVulnGates(t, fake)
	if vg := gateNamed(gates, vulnGateGateName); vg == nil || vg.Details["verdict"] != string(vulnscan.VerdictUnscanned) || vg.Details["determined"] != false ||
		!strings.Contains(vg.Summary, `env "prod" is not on the control plane`) {
		t.Errorf("env missing on the control plane: %+v", vg)
	}
	fake.mu.Lock()
	for _, c := range fake.calls {
		if strings.HasSuffix(c, "/GetReleaseVulnerabilities") {
			t.Errorf("the gate read release-wide findings for an env the control plane does not have: %v", fake.calls)
		}
	}
	fake.mu.Unlock()

	// One that serves no findings leaves every artifact unscanned.
	fake = hostedFindings("sha256:" + strings.Repeat("a", 64))
	fake.features = nil
	gates, _ = ledgerOnlyVulnGates(t, fake)
	if vg := gateNamed(gates, vulnGateGateName); vg == nil || vg.Details["verdict"] != string(vulnscan.VerdictUnscanned) || !strings.Contains(vg.Summary, "does not serve findings") {
		t.Errorf("no findings served: %+v", vg)
	}
}
