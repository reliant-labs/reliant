package cli

// The control plane's VulnerabilityService, as forge speaks it: the SBOM
// upload after a cut, the capability that says whether this control plane
// keeps findings, and the reads `forge vuln list|show|sbom` make of an env
// whose ledger is a control plane (design doc §1.3, §1.7).
//
// Like every hosted call (hosted_ledger.go), the request and response shapes
// are declared HERE in proto3 JSON, from control-plane's
// proto/services/vulnerability/v1/vulnerability.proto and
// proto/controlplane/v1/vulnerability.proto (control-plane#3147), and carried
// by cloud.Client.Call. forge does not vendor control-plane's protos
// (internal/cloud/client.go says why); a field forge does not read is a field
// forge does not break on.
//
// THE CONTROL PLANE IS AUTHORITATIVE on such an env (decision #4): it scans
// the uploaded SBOM and its findings are the record. forge's cut-time scan is
// advisory, and the machine ledger keeps its own copy of every SBOM whatever
// the control plane does with it.

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/vulnscan"
	"github.com/reliant-labs/forge/pkg/release"
)

const (
	procRecordReleaseSBOM          = "controlplane.v1.VulnerabilityService/RecordReleaseSBOM"
	procGetVulnerabilitySummary    = "controlplane.v1.VulnerabilityService/GetVulnerabilitySummary"
	procGetVulnerability           = "controlplane.v1.VulnerabilityService/GetVulnerability"
	procGetReleaseVulnerabilities  = "controlplane.v1.VulnerabilityService/GetReleaseVulnerabilities"
	sbomEncodingGzip               = "SBOM_ENCODING_GZIP"
	hostedReleaseVulnPageSize      = 1000
	hostedReleaseVulnMaxPages      = 200
	hostedVulnFeatureSBOMUpload    = "vuln-sbom-upload"
	hostedVulnFeatureFindings      = "vuln-findings"
	hostedVulnFeatureEnforcesGate  = "enforces-vuln-gate"
	hostedVulnFindingsNotServedMsg = "this control plane does not serve findings yet"
)

// ─── The capability ──────────────────────────────────────────────────────────

// hostedVulnFeatures is GetDeployCapabilitiesResponse.vulnerability_features.
// Each feature gates one thing forge does, and nothing else: an upload is not
// a read, so a control plane can accept SBOMs before it serves findings.
type hostedVulnFeatures struct {
	// Features is what the control plane named, verbatim.
	Features []string
	// SBOMUpload: "vuln-sbom-upload". RecordReleaseSBOM accepts and scans
	// an uploaded SBOM. Gates the upload after a cut.
	SBOMUpload bool
	// Findings: "vuln-findings". The VulnerabilityService reads answer from
	// those scans. Gates `forge vuln list|show|sbom` on such an env.
	Findings bool
	// EnforcesGate: "enforces-vuln-gate". Promote judges the release's
	// vuln_gate itself.
	EnforcesGate bool
}

// acceptsUploads reports whether RecordReleaseSBOM is live. "vuln-findings"
// counts too: findings are scans of uploaded SBOMs, so a control plane that
// serves them takes uploads, whether or not it names the newer feature (one
// that advertised "vuln-findings" before upload had its own name meant both).
func (f hostedVulnFeatures) acceptsUploads() bool { return f.SBOMUpload || f.Findings }

// readHostedVulnFeatures asks the control plane which vulnerability features
// are live. A control plane that does not serve GetCapabilities predates every
// feature; one that serves it without the field names none. Any other failure
// is returned: not knowing is not "none".
func readHostedVulnFeatures(ctx context.Context, client cloudCaller) (hostedVulnFeatures, error) {
	var caps struct {
		Features []string `json:"vulnerabilityFeatures"`
	}
	if err := client.Call(ctx, procDeployCapabilities, map[string]any{}, &caps); err != nil {
		if hostedUnimplemented(err) {
			return hostedVulnFeatures{}, nil
		}
		return hostedVulnFeatures{}, err
	}
	out := hostedVulnFeatures{Features: caps.Features}
	for _, f := range caps.Features {
		out.SBOMUpload = out.SBOMUpload || f == hostedVulnFeatureSBOMUpload
		out.Findings = out.Findings || f == hostedVulnFeatureFindings
		out.EnforcesGate = out.EnforcesGate || f == hostedVulnFeatureEnforcesGate
	}
	return out, nil
}

// hostedUnimplemented reports a control plane that does not serve the
// procedure: the Connect code, or a bare 404 from a server (or a proxy in
// front of it) that sent no Connect envelope.
func hostedUnimplemented(err error) bool {
	var cerr *cloud.Error
	return errors.As(err, &cerr) && (cerr.HasCode(cloud.CodeUnimplemented) || (cerr.Code == "" && cerr.HTTPStatus == http.StatusNotFound))
}

// vulnFeatures is readHostedVulnFeatures, asked once per store: a cut that
// uploads several SBOMs, and the plan line, ask once between them.
func (s *hostedStore) vulnFeatures(ctx context.Context) (hostedVulnFeatures, error) {
	s.mu.Lock()
	known := s.vulnCaps
	s.mu.Unlock()
	if known != nil {
		return *known, nil
	}
	f, err := readHostedVulnFeatures(ctx, s.client)
	if err != nil {
		return f, err
	}
	s.mu.Lock()
	s.vulnCaps = &f
	s.mu.Unlock()
	return f, nil
}

// promotePlanVulns is the deploy plan's lines about the env's control plane
// and vulnerabilities: whether it takes this release's SBOMs, whether it
// serves their findings, and whether it enforces vuln_gate itself.
type promotePlanVulns struct {
	// Features is GetCapabilities.vulnerability_features, verbatim.
	Features []string `json:"features"`
	// SBOMUpload: the control plane accepts and scans the release's SBOMs
	// (vuln-sbom-upload, or vuln-findings).
	SBOMUpload bool `json:"sbom_upload"`
	// Findings: the control plane serves the findings of those scans
	// (vuln-findings): `forge vuln list` reads them.
	Findings bool `json:"findings"`
	// EnforcesGate: the control plane judges vuln_gate at promote.
	EnforcesGate bool `json:"enforces_gate"`
	// Upload and Note say what the reader should know about the upload and
	// about findings and the gate, one sentence each.
	Upload string `json:"upload"`
	Note   string `json:"note"`
}

// hostedVulnPlan is the plan's vulnerability lines for an env whose ledger is
// a control plane; nil for any other. Never an error: a control plane that
// could not be asked is a note, and the deploy goes on.
func hostedVulnPlan(ctx context.Context, ledger envLedger) *promotePlanVulns {
	hs, ok := ledger.Bindings.(*hostedStore)
	if !ok {
		return nil
	}
	f, err := hs.vulnFeatures(ctx)
	if err != nil {
		return &promotePlanVulns{Features: []string{}, Note: fmt.Sprintf("could not ask %s which vulnerability features it serves: %v", hs.endpoint, err)}
	}
	v := &promotePlanVulns{Features: f.Features, SBOMUpload: f.acceptsUploads(), Findings: f.Findings, EnforcesGate: f.EnforcesGate}
	if v.Features == nil {
		v.Features = []string{}
	}
	if v.SBOMUpload {
		v.Upload = "the control plane keeps and scans this release's SBOMs"
	} else {
		v.Upload = "this control plane does not take SBOMs yet: they stay in this machine's ledger"
	}
	switch {
	case f.Findings && f.EnforcesGate:
		v.Note = "it serves their findings (forge vuln list) and judges vuln_gate at promote"
	case f.Findings:
		v.Note = "it serves their findings (forge vuln list); it does not judge vuln_gate at promote yet"
	default:
		v.Note = "it does not serve findings yet (forge vuln list cannot read them from it)"
	}
	return v
}

// renderPlanVulns prints the plan's vulnerability lines: the upload, then
// findings and the gate.
func renderPlanVulns(out io.Writer, v *promotePlanVulns) {
	if v == nil {
		return
	}
	if !v.SBOMUpload && !v.Findings && cloud.ClaimableActive() {
		// What the control plane does not offer is not something an
		// anonymous deploy can act on (as the floor line).
		return
	}
	if v.Upload != "" {
		fmt.Fprintf(out, "  sboms     %s\n", v.Upload)
	}
	fmt.Fprintf(out, "  vulns     %s\n", v.Note)
}

// ─── The upload ──────────────────────────────────────────────────────────────

// sbomBytesReader reads an SBOM from the machine ledger by digest
// (ledgerfile.Store.GetSBOM).
type sbomBytesReader interface {
	GetSBOM(digest string) ([]byte, error)
}

// recordReleaseSBOMs uploads the SBOM of every artifact of r that has one to
// the control plane, after r was cut there (or re-cut: the server is
// idempotent on (release, artifact, digest), and records an SBOM for a
// release cut before it had one, which is the hosted half of the re-cut
// backfill).
//
// NEVER FAILS THE CUT. The release is recorded; the SBOMs are evidence about
// it, and the machine ledger keeps its copy whatever happens here. A control
// plane that does not advertise "vuln-sbom-upload" (or "vuln-findings") is
// skipped silently (it would discard what it was sent); one that advertises
// it and then answers Unimplemented gets one note; any other failure is a
// warning per artifact.
//
// A version-only record's source SBOM (Release.SourceSBOM) describes no
// artifact, and RecordReleaseSBOM files every SBOM under one: it stays in
// this machine's ledger, and the cut says so when the control plane would
// otherwise have taken it.
func (s *hostedStore) recordReleaseSBOMs(ctx context.Context, r release.Release) {
	var names []string
	for name, a := range r.Artifacts {
		if a.SBOM.HasSBOM() {
			names = append(names, name)
		}
	}
	source := r.SourceSBOM.HasSBOM()
	if len(names) == 0 && !source {
		return
	}
	sort.Strings(names)
	warn := s.warn
	if warn == nil {
		warn = os.Stderr
	}
	if cloud.ClaimableActive() {
		// An anonymous deploy prints only what its human must act on
		// (generateReleaseSBOMs keeps the same silence).
		warn = io.Discard
	}
	f, err := s.vulnFeatures(ctx)
	if err != nil {
		fmt.Fprintf(warn, "[ledger] warning: could not ask %s whether it keeps SBOMs (GetCapabilities): %v; release %s's SBOMs stay in this machine's ledger\n", s.endpoint, err, r.Version)
		return
	}
	if !f.acceptsUploads() {
		return
	}
	if source {
		fmt.Fprintf(warn, "[ledger] note: release %s's source SBOM (%s) stays in this machine's ledger: %s keeps SBOMs per artifact, and it describes none\n", r.Version, r.SourceSBOM.Digest, s.endpoint)
	}
	if len(names) == 0 {
		return
	}
	if s.sboms == nil {
		fmt.Fprintf(warn, "[ledger] warning: release %s's SBOMs were not uploaded to %s: this ledger has no machine ledger to read them from\n", r.Version, s.endpoint)
		return
	}
	store, err := s.sboms()
	if err != nil {
		fmt.Fprintf(warn, "[ledger] warning: release %s's SBOMs were not uploaded to %s: open this machine's ledger: %v\n", r.Version, s.endpoint, err)
		return
	}
	project := r.Project
	if project == "" {
		project = s.project
	}
	for _, name := range names {
		ref := r.Artifacts[name].SBOM
		data, err := store.GetSBOM(ref.Digest)
		if err != nil {
			fmt.Fprintf(warn, "[ledger] warning: the SBOM of %s in release %s was not uploaded to %s: %v\n", name, r.Version, s.endpoint, err)
			continue
		}
		gz, err := gzipBytes(data)
		if err != nil {
			fmt.Fprintf(warn, "[ledger] warning: the SBOM of %s in release %s was not uploaded to %s: compress it: %v\n", name, r.Version, s.endpoint, err)
			continue
		}
		req := map[string]any{
			"version":      r.Version,
			"artifactName": name,
			"sbom":         gz, // []byte: proto3 JSON's base64
			"encoding":     sbomEncodingGzip,
			"format":       ref.Format,
		}
		// The org is the credential's (and Forge-Project-ID's), exactly as
		// on every DeployService call: org_id is left unset.
		if project != "" {
			req["project"] = project
		}
		for k, v := range map[string]string{"generator": ref.Generator, "source": ref.Source, "referrer": ref.Referrer} {
			if v != "" {
				req[k] = v
			}
		}
		var resp struct {
			Digest         string `json:"digest"`
			Created        bool   `json:"created"`
			ScanState      string `json:"scanState"`
			ComponentCount int32  `json:"componentCount"`
		}
		if err := s.client.Call(cloud.WithRequestTimeout(ctx, sbomUploadTimeout(len(gz))), procRecordReleaseSBOM, req, &resp); err != nil {
			switch {
			case hostedUnimplemented(err):
				// One note for the whole cut: every other artifact
				// would be answered the same way.
				fmt.Fprintf(warn, "[ledger] note: this control plane does not store SBOMs yet (%s); release %s's SBOMs stay in this machine's ledger\n", s.endpoint, r.Version)
				return
			case hostedErrorHasCode(err, cloud.CodeAlreadyExists):
				fmt.Fprintf(warn, "[ledger] warning: %s already records a different SBOM for %s in release %s and keeps it; this machine's copy is %s\n", s.endpoint, name, r.Version, ref.Digest)
			default:
				fmt.Fprintf(warn, "[ledger] warning: the SBOM of %s in release %s was not uploaded to %s: %v\n", name, r.Version, s.endpoint, err)
			}
			continue
		}
		if resp.Digest != ref.Digest {
			fmt.Fprintf(warn, "[ledger] warning: %s recorded the SBOM of %s in release %s as %s, but the release records %s: the two copies differ\n", s.endpoint, name, r.Version, resp.Digest, ref.Digest)
		}
	}
}

// The upload's request timeout: the client's default 30s, plus a second per
// 512 KiB of the request body, up to ten minutes. The body is the gzipped
// SBOM as proto3 JSON's base64 (4/3 its size); the cap's 32 MiB (≈43 MiB on
// the wire) gets ~2m, a link of ~350 KiB/s.
const (
	sbomUploadBaseTimeout = 30 * time.Second
	sbomUploadPerChunk    = time.Second
	sbomUploadChunk       = 512 << 10
	sbomUploadMaxTimeout  = 10 * time.Minute
)

// sbomUploadTimeout is the timeout of an upload of gzipped bytes.
func sbomUploadTimeout(gzipped int) time.Duration {
	wire := (gzipped + 2) / 3 * 4
	d := sbomUploadBaseTimeout + time.Duration(wire/sbomUploadChunk)*sbomUploadPerChunk
	if d > sbomUploadMaxTimeout {
		d = sbomUploadMaxTimeout
	}
	return d
}

func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ─── The reads ───────────────────────────────────────────────────────────────

// wireVulnDatabase is controlplane.v1.VulnerabilityDatabase.
type wireVulnDatabase struct {
	Scanner        string    `json:"scanner"`
	ScannerVersion string    `json:"scannerVersion"`
	Schema         string    `json:"schema"`
	BuiltAt        time.Time `json:"builtAt"`
	Stale          bool      `json:"stale"`
}

// wireEnvVulnSummary is EnvironmentVulnerabilitySummary, the fields forge reads.
type wireEnvVulnSummary struct {
	EnvironmentID   string `json:"environmentId"`
	EnvironmentName string `json:"environmentName"`
	ReleaseVersion  string `json:"releaseVersion"`
	ScanState       string `json:"scanState"`
}

// wireVulnAdvisory is VulnerabilityAdvisory.
type wireVulnAdvisory struct {
	ID         string   `json:"id"`
	Aliases    []string `json:"aliases"`
	Severity   string   `json:"severity"`
	CVSSScore  *float64 `json:"cvssScore"`
	KEV        bool     `json:"kev"`
	EPSS       *float64 `json:"epss"`
	Summary    string   `json:"summary"`
	References []string `json:"references"`
}

// wireVulnPackage is VulnerablePackage.
type wireVulnPackage struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Type     string `json:"type"`
	PURL     string `json:"purl"`
	Location string `json:"location"`
}

// wireVulnExemption is VulnerabilityExemption.
type wireVulnExemption struct {
	EnvironmentID string `json:"environmentId"`
	AdvisoryID    string `json:"advisoryId"`
	Reason        string `json:"reason"`
	// ExpiresAt is when it stops covering: the end of its expiry date, UTC.
	ExpiresAt time.Time `json:"expiresAt"`
}

// exemption is the exemption as the env declared it (forge.VulnExemption):
// expiresAt is the end of the declared date, so the date is the instant
// before it.
func (w wireVulnExemption) exemption() *vulnscan.Exemption {
	e := &vulnscan.Exemption{ID: w.AdvisoryID, Reason: w.Reason}
	if !w.ExpiresAt.IsZero() {
		e.Expires = w.ExpiresAt.Add(-time.Nanosecond).UTC().Format("2006-01-02")
	}
	return e
}

// wireVulnFinding is VulnerabilityFinding.
type wireVulnFinding struct {
	Advisory       wireVulnAdvisory   `json:"advisory"`
	Package        wireVulnPackage    `json:"package"`
	FixedVersions  []string           `json:"fixedVersions"`
	FixState       string             `json:"fixState"`
	ArtifactNames  []string           `json:"artifactNames"`
	EnvironmentIDs []string           `json:"environmentIds"`
	FirstSeenAt    time.Time          `json:"firstSeenAt"`
	Exemption      *wireVulnExemption `json:"exemption"`
	// Blocking is the control plane's verdict on the finding: whether the
	// scope environment's vuln_gate blocks it, whatever the gate's mode.
	// Judged only when the request named an environment.
	Blocking bool `json:"blocking"`
}

// wireReleaseArtifactSBOM is ReleaseArtifactSBOM.
type wireReleaseArtifactSBOM struct {
	ArtifactName  string    `json:"artifactName"`
	SBOMDigest    string    `json:"sbomDigest"`
	Format        string    `json:"format"`
	Referrer      string    `json:"referrer"`
	ScanState     string    `json:"scanState"`
	LastScannedAt time.Time `json:"lastScannedAt"`
	ScanError     string    `json:"scanError"`
}

// wireReleaseVulns is GetReleaseVulnerabilitiesResponse, every page merged.
type wireReleaseVulns struct {
	ReleaseVersion string                    `json:"releaseVersion"`
	Artifacts      []wireReleaseArtifactSBOM `json:"artifacts"`
	Findings       []wireVulnFinding         `json:"findings"`
	NextPageToken  string                    `json:"nextPageToken"`
	Database       *wireVulnDatabase         `json:"database"`
}

// wireVulnObservation is VulnerabilityObservation.
type wireVulnObservation struct {
	EnvironmentID       string    `json:"environmentId"`
	PURL                string    `json:"purl"`
	FirstSeenAt         time.Time `json:"firstSeenAt"`
	ResolvedAt          time.Time `json:"resolvedAt"`
	FirstReleaseVersion string    `json:"firstReleaseVersion"`
}

// wireVulnOccurrence is VulnerabilityOccurrence.
type wireVulnOccurrence struct {
	ReleaseID      string          `json:"releaseId"`
	ReleaseVersion string          `json:"releaseVersion"`
	ArtifactName   string          `json:"artifactName"`
	SBOMDigest     string          `json:"sbomDigest"`
	Package        wireVulnPackage `json:"package"`
	FixedVersions  []string        `json:"fixedVersions"`
	FixState       string          `json:"fixState"`
	EnvironmentIDs []string        `json:"environmentIds"`
}

// wireVulnGateVerdict is VulnerabilityGateVerdict: what one promotion's
// vuln_gate, judged by the control plane, decided about the advisory.
type wireVulnGateVerdict struct {
	PromotionID    string    `json:"promotionId"`
	EnvironmentID  string    `json:"environmentId"`
	ReleaseVersion string    `json:"releaseVersion"`
	Mode           string    `json:"mode"`
	Verdict        string    `json:"verdict"`
	OverrideReason string    `json:"overrideReason"`
	CreatedAt      time.Time `json:"createdAt"`
}

// wireVulnerability is GetVulnerabilityResponse.
type wireVulnerability struct {
	Advisory     wireVulnAdvisory      `json:"advisory"`
	Occurrences  []wireVulnOccurrence  `json:"occurrences"`
	Observations []wireVulnObservation `json:"observations"`
	Exemptions   []wireVulnExemption   `json:"exemptions"`
	GateVerdicts []wireVulnGateVerdict `json:"gateVerdicts"`
}

// The VulnerabilityScanState values.
const (
	wireScanNoSBOM  = "VULNERABILITY_SCAN_STATE_NO_SBOM"
	wireScanPending = "VULNERABILITY_SCAN_STATE_PENDING"
	wireScanScanned = "VULNERABILITY_SCAN_STATE_SCANNED"
	wireScanFailed  = "VULNERABILITY_SCAN_STATE_FAILED"
	wireScanStale   = "VULNERABILITY_SCAN_STATE_STALE"
	// wireScanUnspecified is the zero value; proto3 JSON omits it, so it
	// arrives as "" too.
	wireScanUnspecified = "VULNERABILITY_SCAN_STATE_UNSPECIFIED"
)

// hostedVulnReader reads one project's findings from a control plane.
type hostedVulnReader struct {
	client  cloudCaller
	project string
}

// envSummary is env's row of GetVulnerabilitySummary: its control-plane id
// and the release in its newest promotion. ok is false for an env the control
// plane has no row for (never created there, or not readable by the caller).
func (h hostedVulnReader) envSummary(ctx context.Context, env string) (wireEnvVulnSummary, bool, error) {
	var resp struct {
		Environments []wireEnvVulnSummary `json:"environments"`
	}
	if err := h.client.Call(ctx, procGetVulnerabilitySummary, map[string]any{"project": h.project}, &resp); err != nil {
		return wireEnvVulnSummary{}, false, err
	}
	for _, e := range resp.Environments {
		if e.EnvironmentName == env {
			return e, true, nil
		}
	}
	return wireEnvVulnSummary{}, false, nil
}

// releaseVulns is GetReleaseVulnerabilities, every page. envID applies that
// environment's exemptions and first-seen times; "" applies none. findings
// false asks for the artifacts alone (one finding per page, one page).
func (h hostedVulnReader) releaseVulns(ctx context.Context, version, envID string, findings bool) (wireReleaseVulns, error) {
	var out wireReleaseVulns
	token := ""
	size := hostedReleaseVulnPageSize
	if !findings {
		size = 1
	}
	for page := 0; ; page++ {
		if page >= hostedReleaseVulnMaxPages {
			return out, fmt.Errorf("release %s: the control plane returned more than %d pages of findings", version, hostedReleaseVulnMaxPages)
		}
		req := map[string]any{"version": version, "pageSize": size}
		if h.project != "" {
			req["project"] = h.project
		}
		if envID != "" {
			req["environmentId"] = envID
		}
		if token != "" {
			req["pageToken"] = token
		}
		var resp wireReleaseVulns
		if err := h.client.Call(ctx, procGetReleaseVulnerabilities, req, &resp); err != nil {
			return out, err
		}
		if page == 0 {
			out = resp
			out.Findings = nil
		}
		out.Findings = append(out.Findings, resp.Findings...)
		if !findings || resp.NextPageToken == "" || resp.NextPageToken == token {
			return out, nil
		}
		token = resp.NextPageToken
	}
}

// vulnerability is GetVulnerability. ok is false when the control plane knows
// no advisory by that id in the project.
func (h hostedVulnReader) vulnerability(ctx context.Context, id string) (wireVulnerability, bool, error) {
	var resp wireVulnerability
	err := h.client.Call(ctx, procGetVulnerability, map[string]any{"project": h.project, "advisoryId": id}, &resp)
	if hostedErrorHasCode(err, cloud.CodeNotFound) {
		return resp, false, nil
	}
	return resp, err == nil, err
}

// hostedArtifactScans maps a release's findings, as the control plane holds
// them, onto the artifacts BuildDocument merges: the same document, judged by
// the same rule, whichever ledger the env records in. stale reports that the
// control plane judged any of it stale; notes say what the document cannot.
//
// Whether the verdict is determined (gate.determined) follows the control
// plane's own scan state, never this machine's clock: the document is built
// with DocumentInput.FreshnessFromSource, so the database build time is
// reported but not re-judged against vulnscan.StaleAfter. Per artifact:
//
//	SCANNED                        scanned; current unless database.stale
//	STALE                          scanned, stale: verdict "stale", undetermined
//	FAILED, an earlier scan        scanned with that scan's findings, and a note
//	FAILED, never scanned          unscanned
//	PENDING, NO_SBOM, unknown      unscanned
//	database.stale                 every scan stale: verdict "stale", undetermined
//
// An unscanned artifact makes the verdict "unscanned" (undetermined) unless a
// finding blocks, which the precedence of vulnscan.Evaluate puts first; a
// stale one makes it "stale" whatever blocks.
//
// A finding with no artifact_names is the release's (returned in release): it
// is kept at release level, attributed to no artifact, and judged like the
// rest.
func hostedArtifactScans(rv wireReleaseVulns) (scans []vulnscan.ArtifactScan, release []vulnscan.ReleaseFinding, stale bool, notes []string) {
	db := wireVulnDatabase{Scanner: vulnscan.ScannerGrype}
	if rv.Database != nil {
		db = *rv.Database
		stale = db.Stale
	}
	if stale {
		notes = append(notes, "the control plane's vulnerability database is past its freshness bound, so it judges every scan of this release stale")
	}
	byName := map[string]int{}
	for _, a := range rv.Artifacts {
		s := vulnscan.ArtifactScan{Artifact: a.ArtifactName, SBOMDigest: a.SBOMDigest}
		scanned := func() *vulnscan.Scan {
			return &vulnscan.Scan{
				Schema: vulnscan.ScanSchema, Scanner: db.Scanner, ScannerVersion: db.ScannerVersion,
				DBSchema: db.Schema, DBBuiltAt: db.BuiltAt, ScannedAt: a.LastScannedAt, Findings: []vulnscan.Finding{},
			}
		}
		switch a.ScanState {
		case wireScanScanned:
			s.Latest = scanned()
		case wireScanStale:
			s.Latest = scanned()
			stale = true
			notes = append(notes, fmt.Sprintf("the control plane judges its newest scan of %s stale", a.ArtifactName))
		case wireScanFailed:
			if !a.LastScannedAt.IsZero() {
				s.Latest = scanned()
				notes = append(notes, fmt.Sprintf("the control plane's newest scan of %s failed (%s); its findings are from the scan of %s", a.ArtifactName, oneLine(a.ScanError, 200), a.LastScannedAt.UTC().Format("2006-01-02 15:04Z")))
			} else {
				s.Reason = "the control plane's scan failed: " + oneLine(a.ScanError, 200)
			}
		case wireScanPending:
			s.Reason = "SBOM recorded; the control plane has not finished scanning it"
		case wireScanNoSBOM:
			s.Reason = "no SBOM recorded on the control plane"
		case wireScanUnspecified, "":
			s.Reason = "not scanned (the control plane gave no scan state)"
		default:
			// A state this forge does not know: never read as scanned.
			s.Reason = fmt.Sprintf("not scanned (unknown scan state %s)", a.ScanState)
		}
		byName[a.ArtifactName] = len(scans)
		scans = append(scans, s)
	}
	for _, w := range rv.Findings {
		f := hostedFinding(w)
		// The artifacts the finding is in, as the control plane names
		// them, that carry a scan here.
		var carried, missing []string
		for _, name := range w.ArtifactNames {
			if i, ok := byName[name]; ok && scans[i].Latest != nil {
				carried = append(carried, name)
			} else {
				missing = append(missing, name)
			}
		}
		if len(carried) == 0 {
			// No artifact names (the control plane holds it at release
			// scope), or only artifacts it lists no scan of: the finding is
			// the release's, attributed to no artifact. Spreading it onto
			// every artifact would claim each contains the package;
			// dropping it would hide a finding from the gate.
			if len(missing) > 0 {
				notes = append(notes, fmt.Sprintf("the control plane attributes %s in %s to %s, which it lists no scan of; it is listed at release level", f.ID, f.Package.Name, strings.Join(missing, ", ")))
			}
			release = append(release, vulnscan.ReleaseFinding{Finding: f, FirstSeen: w.FirstSeenAt})
			continue
		}
		for _, name := range carried {
			i := byName[name]
			scans[i].Latest.Findings = append(scans[i].Latest.Findings, f)
			if !w.FirstSeenAt.IsZero() {
				if scans[i].FirstSeen == nil {
					scans[i].FirstSeen = map[string]time.Time{}
				}
				scans[i].FirstSeen[f.Key()] = w.FirstSeenAt
			}
		}
	}
	return scans, release, stale, notes
}

// hostedFinding is one VulnerabilityFinding as a vulnscan.Finding.
func hostedFinding(w wireVulnFinding) vulnscan.Finding {
	f := vulnscan.Finding{
		ID:       w.Advisory.ID,
		Aliases:  w.Advisory.Aliases,
		Severity: hostedSeverity(w.Advisory.Severity),
		CVSS:     w.Advisory.CVSSScore,
		KEV:      w.Advisory.KEV,
		EPSS:     w.Advisory.EPSS,
		Package:  vulnscan.Package{Name: w.Package.Name, Version: w.Package.Version, PURL: w.Package.PURL, Type: w.Package.Type},
		FixedIn:  w.FixedVersions,
		FixState: hostedFixState(w.FixState),
	}
	if w.Package.Location != "" {
		f.Locations = []string{w.Package.Location}
	}
	if len(w.Advisory.References) > 0 {
		f.URL = w.Advisory.References[0]
	}
	return f
}

// hostedSeverity maps a VulnerabilitySeverity. UNKNOWN, UNSPECIFIED (which
// proto3 JSON omits, so it arrives as ""), and any value this forge does not
// know are vulnscan.SeverityUnknown, which the gate counts as critical (the
// design's fail-closed rule): a missing severity never reads as low.
func hostedSeverity(wire string) vulnscan.Severity {
	switch wire {
	case "VULNERABILITY_SEVERITY_CRITICAL":
		return vulnscan.SeverityCritical
	case "VULNERABILITY_SEVERITY_HIGH":
		return vulnscan.SeverityHigh
	case "VULNERABILITY_SEVERITY_MEDIUM":
		return vulnscan.SeverityMedium
	case "VULNERABILITY_SEVERITY_LOW":
		return vulnscan.SeverityLow
	case "VULNERABILITY_SEVERITY_NEGLIGIBLE":
		return vulnscan.SeverityNegligible
	case "VULNERABILITY_SEVERITY_UNKNOWN", "VULNERABILITY_SEVERITY_UNSPECIFIED", "":
		return vulnscan.SeverityUnknown
	default:
		return vulnscan.SeverityUnknown
	}
}

// hostedFixState maps a VulnerabilityFixState. UNKNOWN, UNSPECIFIED (omitted
// by proto3 JSON, so ""), and any value this forge does not know are
// vulnscan.FixStateUnknown: not fixable, never mistaken for a known state.
func hostedFixState(wire string) string {
	switch wire {
	case "VULNERABILITY_FIX_STATE_FIXED":
		return vulnscan.FixStateFixed
	case "VULNERABILITY_FIX_STATE_NOT_FIXED":
		return vulnscan.FixStateNotFixed
	case "VULNERABILITY_FIX_STATE_WONT_FIX":
		return vulnscan.FixStateWontFix
	case "VULNERABILITY_FIX_STATE_UNKNOWN", "VULNERABILITY_FIX_STATE_UNSPECIFIED", "":
		return vulnscan.FixStateUnknown
	default:
		return vulnscan.FixStateUnknown
	}
}

// hostedReleaseFindings is one release's findings as the control plane holds
// them, mapped for vulnscan.BuildDocument.
type hostedReleaseFindings struct {
	// Version is the release read.
	Version string
	// Scans is one entry per artifact of the release (DocumentInput.Artifacts).
	Scans []vulnscan.ArtifactScan
	// Release are the findings attributed to the release and to no
	// artifact (DocumentInput.ReleaseFindings).
	Release []vulnscan.ReleaseFinding
	// Stale is the control plane judging the findings stale
	// (DocumentInput.Stale).
	Stale bool
	// Notes are what the document cannot say (a failing rescan).
	Notes []string
	// Artifacts is the release's SBOM per artifact, as the control plane
	// records it (`forge vuln sbom`).
	Artifacts []wireReleaseArtifactSBOM
	// Judged is the control plane's verdict on each finding, by
	// Finding.Key() (DocumentInput.Judged): nil when it returned none, and
	// the findings are then judged locally (JudgedBy says which).
	Judged   map[string]vulnscan.Judgement
	JudgedBy string
}

// hostedVulnSource is the control plane's findings of the releases of ONE env:
// "findings for release X in env Y", with Y's exemptions and first-seen times
// applied by the control plane. It is what `forge vuln list|show|patch` read
// for such an env, and what the promote check reads (hostedPromoteVulnSource)
// for an env whose apply forge does but whose ledger is a control plane
// (design §1.5).
type hostedVulnSource struct {
	reader hostedVulnReader
	env    string
}

// errHostedVulnEnvNotFound is a control plane with no environment of the name a
// read asked for: it holds no scope and no policy to read findings under.
var errHostedVulnEnvNotFound = errors.New("the control plane has no such environment")

// releaseFindings reads release version's findings in the source's env.
//
// THE VERDICT IS THE CONTROL PLANE'S (decision #4). Asked with the env's id,
// the control plane judges every finding against the vuln_gate the env
// recorded there and says which block (VulnerabilityFinding.blocking) and
// which an exemption covers; those decide the document's gate and its exit
// code, whatever this checkout's vuln_gate says.
//
// ALWAYS SCOPED TO THE ENV. A control plane with no environment of that name
// (no summary row, or one with no id) is errHostedVulnEnvNotFound, and nothing is
// read: a release-wide read would carry neither the env's exemptions nor its
// policy, and is not what the caller may have been granted. Callers make it
// "could not determine" (exit 2), and the promote gate makes it unscanned,
// never clean.
//
// proto3 JSON omits a false bool, so a response cannot say "not blocking"
// apart from "not judged": the rule is that a read naming the environment is
// judged, which every control plane serving these reads does (C1's contract).
func (h hostedVulnSource) releaseFindings(ctx context.Context, version string) (hostedReleaseFindings, error) {
	sum, ok, err := h.reader.envSummary(ctx, h.env)
	if err != nil {
		return hostedReleaseFindings{}, err
	}
	if !ok || sum.EnvironmentID == "" {
		return hostedReleaseFindings{}, fmt.Errorf("env %q: %w", h.env, errHostedVulnEnvNotFound)
	}
	rv, err := h.reader.releaseVulns(ctx, version, sum.EnvironmentID, true)
	if err != nil {
		return hostedReleaseFindings{}, err
	}
	scans, rel, stale, notes := hostedArtifactScans(rv)
	out := hostedReleaseFindings{Version: version, Scans: scans, Release: rel, Stale: stale, Notes: notes, Artifacts: rv.Artifacts, JudgedBy: vulnscan.JudgedByControlPlane}
	out.Judged = map[string]vulnscan.Judgement{}
	for _, w := range rv.Findings {
		j := vulnscan.Judgement{Blocking: w.Blocking}
		if w.Exemption != nil {
			j.Exemption = w.Exemption.exemption()
		}
		out.Judged[hostedFinding(w).Key()] = j
	}
	return out, nil
}
