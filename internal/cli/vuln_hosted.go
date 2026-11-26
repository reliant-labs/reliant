package cli

// `forge vuln list|show|sbom|patch` and `scan --deployed` for an env whose
// ledger is a control plane: the control plane's findings (hosted_vuln.go),
// mapped into the same forge.dev/vuln-list/v1 document the machine ledger
// produces and judged by the same rule, so the output and the exit codes do
// not depend on which ledger answered.
//
// Exit codes on top of vuln.go's: 4 when the control plane refuses (not signed
// in, no vuln:read); 2 when it does not serve findings yet (no "vuln-findings"
// capability, or the rpc is Unimplemented).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/internal/sbomoci"
	"github.com/reliant-labs/forge/internal/vulnscan"
	"github.com/reliant-labs/forge/pkg/release"
)

// hostedVulnClient is the client of env's control plane, with the credential
// every hosted verb resolves. No credential is a refusal (exit 4): the
// findings are there, this caller may not read them.
func hostedVulnClient(_ context.Context, env vulnEnv) (cloudCaller, error) {
	ep, err := projectEndpoint(env.Name, env.decl)
	if err != nil {
		return nil, undeterminedf("env %q: %v", env.Name, err)
	}
	cred, err := cloud.ResolveCredential("", ep)
	if err != nil {
		return nil, &exitCodeError{code: exitRefused, msg: fmt.Sprintf("env %q keeps its findings on the control plane at %s, and reading them needs a sign-in: %v", env.Name, ep.URL, err)}
	}
	return cloud.NewClient(ep, cred), nil
}

// hostedVulnSourceFor opens env's control plane and checks it serves
// findings: the reader every hosted verb uses.
func hostedVulnSourceFor(ctx context.Context, deps vulnDeps, env vulnEnv) (hostedVulnSource, error) {
	if deps.hostedClient == nil {
		return hostedVulnSource{}, undeterminedf("env %q keeps its findings on the control plane at %s, which this command cannot reach", env.Name, env.Endpoint)
	}
	client, err := deps.hostedClient(ctx, env)
	if err != nil {
		return hostedVulnSource{}, err
	}
	f, err := readHostedVulnFeatures(ctx, client)
	if err != nil {
		return hostedVulnSource{}, hostedVulnReadError(env, err)
	}
	if !f.Findings {
		return hostedVulnSource{}, hostedFindingsNotServed(env)
	}
	return hostedVulnSource{reader: hostedVulnReader{client: client, project: env.Project}, env: env.Name}, nil
}

func hostedFindingsNotServed(env vulnEnv) error {
	return undeterminedf("could not determine: %s (%s, env %q's ledger); nothing was read. "+
		"To scan an image of that env on this machine: forge vuln scan --image <ref@sha256:…>", hostedVulnFindingsNotServedMsg, env.Endpoint, env.Name)
}

// hostedVulnReadError classifies a failed read: a refusal is exit 4, a control
// plane without the rpc and anything else is exit 2. Never a pass.
func hostedVulnReadError(env vulnEnv, err error) error {
	var ec *exitCodeError
	if errors.As(err, &ec) {
		return err
	}
	switch {
	case hostedErrorHasCode(err, cloud.CodePermissionDenied), hostedErrorHasCode(err, cloud.CodeUnauthenticated):
		return &exitCodeError{code: exitRefused, msg: fmt.Sprintf("refused: reading env %q's vulnerabilities on %s needs the vuln:read scope "+
			"(a token minted with vuln:read, or a grant of it on the env): %v", env.Name, env.Endpoint, err)}
	case hostedUnimplemented(err):
		return hostedFindingsNotServed(env)
	}
	return undeterminedf("could not determine: read env %q's vulnerabilities from %s: %v", env.Name, env.Endpoint, err)
}

// hostedReleaseOf is the release a hosted verb reads: --release, else the one
// the control plane says the env runs.
func hostedReleaseOf(ctx context.Context, src hostedVulnSource, env vulnEnv, version string) (string, error) {
	if version != "" {
		return version, nil
	}
	sum, ok, err := src.reader.envSummary(ctx, env.Name)
	if err != nil {
		return "", hostedVulnReadError(env, err)
	}
	if !ok || sum.ReleaseVersion == "" {
		return "", undeterminedf("env %q has no promotion on the control plane at %s; name a release with --release <v>", env.Name, env.Endpoint)
	}
	return sum.ReleaseVersion, nil
}

// hostedReleaseDocument reads release version's findings (the env's current
// release when version is "") from the control plane src reads, and builds
// the document every control-plane verb reads: list, show, scan --deployed
// and patch. The verdict is the control plane's when it gave one (Judged);
// env.Gate then supplies only the mode label and the warn band. Whether the
// verdict is determined is the control plane's scan state too
// (FreshnessFromSource; the mapping is hostedArtifactScans'), and a finding
// it holds at release scope stays there (ReleaseFindings).
func hostedReleaseDocument(ctx context.Context, src hostedVulnSource, env vulnEnv, version string, filter vulnscan.Filter, now time.Time) (vulnscan.Document, error) {
	version, err := hostedReleaseOf(ctx, src, env, version)
	if err != nil {
		return vulnscan.Document{}, err
	}
	f, err := src.releaseFindings(ctx, version)
	if err != nil {
		if hostedErrorHasCode(err, cloud.CodeNotFound) {
			return vulnscan.Document{}, undeterminedf("release %s is not on the control plane at %s (project %q)", version, env.Endpoint, env.Project)
		}
		if errors.Is(err, errHostedVulnEnvNotFound) {
			return vulnscan.Document{}, undeterminedf("could not determine: env %q is not on the control plane at %s (project %q), so it has no scope or vuln_gate to read release %s's findings under; nothing was read", env.Name, env.Endpoint, env.Project, version)
		}
		return vulnscan.Document{}, hostedVulnReadError(env, err)
	}
	return vulnscan.BuildDocument(vulnscan.DocumentInput{
		Env: env.Name, Release: version, Gate: env.Gate, Artifacts: f.Scans, ReleaseFindings: f.Release, Filter: filter, Now: now, Notes: f.Notes,
		Stale: f.Stale, FreshnessFromSource: true, Judged: f.Judged, JudgedBy: f.JudgedBy,
	}), nil
}

// runVulnListHosted is runVulnList for an env whose ledger is a control plane.
func runVulnListHosted(ctx context.Context, out, errOut io.Writer, deps vulnDeps, env vulnEnv, version string, filter vulnscan.Filter, asJSON bool) error {
	src, err := hostedVulnSourceFor(ctx, deps, env)
	if err != nil {
		return err
	}
	now := deps.now()
	doc, err := hostedReleaseDocument(ctx, src, env, version, filter, now)
	if err != nil {
		return err
	}
	if asJSON {
		if err := writeVulnJSON(out, doc); err != nil {
			return err
		}
		return vulnExit(doc)
	}
	if filter.Match != "" && len(doc.Findings) == 0 {
		_, _ = fmt.Fprintf(out, "no finding in %s release %s matches %q\n", env.Name, doc.Release, filter.Match)
	}
	writeVulnDocument(out, doc, now, "")
	if filter.Match != "" {
		writeHostedAdvisory(ctx, out, src, filter.Match)
	}
	_, _ = fmt.Fprintf(out, "findings: the control plane at %s, judged by the control plane\n", env.Endpoint)
	if link := hostedVulnConsoleLink(env); link != "" {
		_, _ = fmt.Fprintf(out, "→ %s\n", link)
	}
	return vulnExit(doc)
}

// hostedPatchFindings is machineLedgerPatchFindings for an env whose ledger
// is a control plane (`forge vuln patch <env>`): the findings of the release
// the control plane says the env runs, read exactly as `forge vuln list`
// reads them (hostedReleaseDocument), exempt ones included and marked, and
// release-level ones included. It is gated like list: no vuln-findings
// capability is exit 2 with list's message, a refusal (no vuln:read) exit 4.
// Nothing is scanned on this machine.
func hostedPatchFindings(ctx context.Context, deps vulnDeps, env vulnEnv) (patchFindings, error) {
	var out patchFindings
	src, err := hostedVulnSourceFor(ctx, deps, env)
	if err != nil {
		return out, err
	}
	doc, err := hostedReleaseDocument(ctx, src, env, "", vulnscan.Filter{IncludeExempt: true}, deps.now())
	if err != nil {
		return out, err
	}
	if doc.Coverage.Scanned == 0 && len(doc.Findings) == 0 {
		return out, undeterminedf("nothing to patch from: the control plane at %s has no scan of any artifact of %s release %s yet", env.Endpoint, env.Name, doc.Release)
	}
	// As on the machine ledger, the patcher reads findings, never the
	// gate's verdict: stale findings are still worth bumping, and the
	// document's notes (carried as they are) say when they are stale.
	out.Release, out.Findings = doc.Release, doc.Findings
	out.Notes = append(out.Notes, doc.Notes...)
	for _, u := range doc.Coverage.Unscanned {
		out.Notes = append(out.Notes, fmt.Sprintf("%s was not scanned (%s): its findings are not patched", u.Artifact, u.Reason))
	}
	out.Notes = append(out.Notes, "findings: the control plane at "+env.Endpoint)
	return out, nil
}

// writeHostedAdvisory prints what the control plane knows of advisory id
// beyond the release's findings: its summary, references and history per
// environment. Best effort: a package name, or an id the control plane does
// not know, prints nothing.
func writeHostedAdvisory(ctx context.Context, out io.Writer, src hostedVulnSource, id string) {
	v, ok, err := src.reader.vulnerability(ctx, id)
	if err != nil || !ok {
		return
	}
	if s := strings.TrimSpace(v.Advisory.Summary); s != "" {
		_, _ = fmt.Fprintf(out, "\n%s: %s\n", v.Advisory.ID, oneLine(s, 300))
	}
	for _, r := range v.Advisory.References {
		_, _ = fmt.Fprintf(out, "  %s\n", r)
	}
	for _, o := range v.Observations {
		line := fmt.Sprintf("  env %s: %s first seen %s", o.EnvironmentID, o.PURL, o.FirstSeenAt.UTC().Format("2006-01-02"))
		if o.FirstReleaseVersion != "" {
			line += " (release " + o.FirstReleaseVersion + ")"
		}
		if !o.ResolvedAt.IsZero() {
			line += ", resolved " + o.ResolvedAt.UTC().Format("2006-01-02")
		}
		_, _ = fmt.Fprintln(out, line)
	}
	for _, g := range v.GateVerdicts {
		line := fmt.Sprintf("  promotion %s of %s to env %s: vuln_gate (%s) %s", g.PromotionID, g.ReleaseVersion, g.EnvironmentID, g.Mode, strings.ToUpper(g.Verdict))
		if g.OverrideReason != "" {
			line += ": " + oneLine(g.OverrideReason, 200)
		}
		_, _ = fmt.Fprintln(out, line)
	}
}

// hostedVulnConsoleLink is the project's Vulnerabilities page in the forge
// console, "" when no console is known for the env's control plane.
func hostedVulnConsoleLink(env vulnEnv) string {
	base := cloud.WebAppBase(env.Endpoint)
	if base == "" {
		return ""
	}
	return cloud.NewConsole(base, env.Project).Vulnerabilities()
}

// hostedVulnSBOM is the SBOM `forge vuln sbom` prints for a hosted env: the
// control plane names the artifact's SBOM digest, and the bytes are this
// machine's ledger copy (every cut keeps one). An SBOM cut on another machine
// is not here; reading it from its OCI referrer is not in this forge.
func hostedVulnSBOM(ctx context.Context, deps vulnDeps, env vulnEnv, opts vulnSBOMOptions) ([]byte, error) {
	src, err := hostedVulnSourceFor(ctx, deps, env)
	if err != nil {
		return nil, err
	}
	version, err := hostedReleaseOf(ctx, src, env, opts.release)
	if err != nil {
		return nil, err
	}
	rv, err := src.reader.releaseVulns(ctx, version, "", false)
	if err != nil {
		if hostedErrorHasCode(err, cloud.CodeNotFound) {
			return nil, undeterminedf("release %s is not on the control plane at %s (project %q)", version, env.Endpoint, env.Project)
		}
		return nil, hostedVulnReadError(env, err)
	}
	var withSBOM []string
	byName := map[string]wireReleaseArtifactSBOM{}
	for _, a := range rv.Artifacts {
		byName[a.ArtifactName] = a
		if a.SBOMDigest != "" {
			withSBOM = append(withSBOM, a.ArtifactName)
		}
	}
	sort.Strings(withSBOM)
	name := opts.artifact
	switch {
	case name == "" && len(withSBOM) == 1:
		name = withSBOM[0]
	case name == "" && len(withSBOM) == 0:
		return nil, undeterminedf("the control plane records no SBOM for any artifact of release %s", version)
	case name == "":
		return nil, fmt.Errorf("release %s has SBOMs for %s: name one with --artifact", version, strings.Join(withSBOM, ", "))
	}
	a, ok := byName[name]
	if !ok {
		return nil, fmt.Errorf("release %s has no artifact %q (artifacts with an SBOM: %s)", version, name, strings.Join(withSBOM, ", "))
	}
	if a.SBOMDigest == "" {
		return nil, undeterminedf("the control plane records no SBOM for %s in release %s", name, version)
	}
	store, err := deps.machineLedger(deps.projectDir())
	if err != nil {
		return nil, undeterminedf("open this machine's ledger: %v", err)
	}
	data, err := store.GetSBOM(a.SBOMDigest)
	if err == nil {
		return data, nil
	}
	if errors.Is(err, ledgerfile.ErrSBOMCorrupt) {
		return nil, undeterminedf("the SBOM of %s in release %s: %v", name, version, err)
	}
	if !errors.Is(err, ledgerfile.ErrSBOMNotFound) {
		return nil, err
	}
	// Cut on another machine: the bytes are beside the image, as its OCI
	// referrer. The control plane names the SBOM's digest and referrer; the
	// release it holds names the image's digest, which is what proves a
	// referrer describes this artifact (sbomoci.Fetch). The bytes are
	// verified against the recorded digest and cached into this machine's
	// ledger, so the next read (and every scan) needs no network.
	subject, why := hostedSBOMSubject(ctx, src, env, version, name, a)
	if subject != "" {
		art := release.Artifact{SBOM: &release.SBOMRef{Digest: a.SBOMDigest, Referrer: a.Referrer, Status: release.SBOMStatusOK}}
		host, _, _ := strings.Cut(subject, "/")
		credential, cerr := sbomRegistryCredential(deps.projectDir(), env.Name, env.entities, host)
		if cerr == nil {
			data, _, ferr := sbomReferrerFetch(ctx, store, art, subject, sbomoci.RegistryOptions{Credential: credential})
			if ferr == nil {
				return data, nil
			}
			why = ferr.Error()
		} else {
			why = "registry credential for " + host + ": " + cerr.Error()
		}
	}
	where := ""
	if a.Referrer != "" {
		where = " Its OCI referrer is " + a.Referrer + "."
	}
	return nil, undeterminedf("the control plane records SBOM %s for %s in release %s, and this machine's ledger has no copy "+
		"(the release was cut on another machine), and it could not be read from beside the image: %s.%s", a.SBOMDigest, name, version, why, where)
}

// sbomReferrerFetch reads an SBOM from the machine ledger or its OCI
// referrer. A var so a test states the registry.
var sbomReferrerFetch = sbomoci.FetchWith

// hostedSBOMSubject is `<repository>@<digest>` of artifact name in release
// version, as the control plane holds the release: the subject an OCI
// referrer of its SBOM is checked against. The repository is where the cut
// pushed it (sbomSubjectRepository), else the recorded referrer's own
// repository (a referrer lives beside its subject). "" with the reason when
// there is none: an artifact that is not one shared OCI image, or a release
// the control plane would not return.
func hostedSBOMSubject(ctx context.Context, src hostedVulnSource, env vulnEnv, version, name string, a wireReleaseArtifactSBOM) (subject, why string) {
	rels := hostedLedger(src.reader.client, env.Endpoint, env.Project, "").Releases
	rel, err := rels.Get(ctx, version)
	switch {
	case err != nil:
		return "", "read release " + version + " from the control plane: " + err.Error()
	case rel == nil:
		return "", "the control plane returned no release " + version
	}
	art, ok := rel.Artifacts[name]
	if !ok {
		return "", "the control plane's release " + version + " has no artifact " + name
	}
	digest, ok := art.SharedDigest()
	if !ok {
		return "", name + " is not one shared OCI image, so it has no referrer to read"
	}
	repository := sbomSubjectRepository(name, art, env.entities)
	if repository == "" && a.Referrer != "" {
		repository, _, _ = strings.Cut(a.Referrer, "@")
	}
	if repository == "" {
		return "", "nothing names the registry repository of " + name
	}
	return repository + "@" + digest, ""
}
