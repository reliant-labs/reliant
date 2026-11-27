package cli

// vuln_gate and provenance_sbom at a self-hosted promote (design doc §1.8).
//
// WHERE. checkPromotionPolicy (promote_policy_checks.go) runs these beside
// required_gates and the approval hook, for an env whose ledger is not a
// control plane, before forge's own promotion write — the only write such an
// env has. A control-plane env is judged by its control plane in the
// promotion's transaction (C6), against its own findings for the release's
// SBOM digests (owner decision 4); forge judges nothing for it here.
//
// THE LEDGER-ONLY ROW (§1.5): an env with a control plane whose apply forge
// does and the control plane converges nothing (envLedger.ledgerOnly). Its
// findings are the control plane's, and so is the verdict on each finding:
// checkPromotionVulnGates reads them through hostedVulnSource.releaseFindings
// and judges them with the control plane's per-finding judgement (Judged),
// its freshness (Stale, FreshnessFromSource) and its release-level findings,
// exactly as `forge vuln list` reads them. A control plane that enforces
// vuln_gate itself (enforces-vuln-gate) judges the promotion in its own
// transaction, so forge judges nothing for it here; one that serves no
// findings leaves every artifact unscanned (audit notes it, enforce refuses:
// could not determine is never a pass).
//
// WHAT IS RECORDED. Each check that ran leaves a gate on the promotion, under
// the names it judges (vulnGateGateName, sbomGateName), the way required_gates
// does: the declared and graded modes, the verdict, the blocking advisories
// and the outcome (passed, audit, refused, overridden). An override records
// who, why and which findings.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/vulnscan"
	"github.com/reliant-labs/forge/pkg/release"
)

// The names the two checks record their verdicts under on a promotion, and
// their refusal reasons: pkg/release's, shared with the control plane that
// enforces the same checks for its envs. A caller-supplied gate of either
// name is refused while the check runs: it would forge the check's own
// verdict.
const (
	vulnGateGateName = release.VulnGateGateName
	sbomGateName     = release.ProvenanceSBOMGateName

	// reasonVulnGateBlocked: a finding the env's vuln_gate blocks.
	// exitRefused.
	reasonVulnGateBlocked = release.RefusalVulnGateBlocked
	// reasonVulnGateUnscanned: no SBOM, no scan, or a stale database
	// under enforce: could not determine is never a pass. exitRefused.
	reasonVulnGateUnscanned = release.RefusalVulnGateUnscanned
	// reasonSBOMMissing: provenance_sbom = enforce, and an artifact the
	// release ships has no SBOM. exitRefused.
	reasonSBOMMissing = release.RefusalSBOMMissing
)

// vulnGateFindings is what a findings source hands the gate: the latest scan
// of every shipping artifact, and, from a ledger that judges its own findings
// (a control plane), its verdict on each, its freshness and the findings it
// holds at release scope. They go into vulnscan.DocumentInput as they are.
type vulnGateFindings struct {
	Scans []vulnscan.ArtifactScan
	// Release are findings attributed to the release and to no artifact
	// (DocumentInput.ReleaseFindings).
	Release []vulnscan.ReleaseFinding
	Notes   []string
	// Stale and FreshnessFromSource: the source judged the findings'
	// freshness itself (DocumentInput).
	Stale, FreshnessFromSource bool
	// Judged is the source's verdict on each finding, nil when it gave
	// none; JudgedBy says whose verdict the gate is (GateResult.JudgedBy).
	Judged   map[string]vulnscan.Judgement
	JudgedBy string
}

// vulnFindingsSource returns a release's findings for the gate. fetch allows
// it to refresh (or download) the vulnerability database first; without it,
// it scans only with what this machine has.
type vulnFindingsSource interface {
	gateFindings(ctx context.Context, rel *release.Release, fetch bool) (vulnGateFindings, error)
}

// machineVulnSource is the machine ledger's findings: the SBOMs the cut
// stored, scanned again when this machine's database is newer than the last
// scan (the machine ledger's rescan on read).
type machineVulnSource struct {
	deps  vulnDeps
	store vulnscan.FindingsStore
	log   io.Writer
}

func (m machineVulnSource) gateFindings(ctx context.Context, rel *release.Release, fetch bool) (vulnGateFindings, error) {
	var notes []string
	var scanner *vulnscan.Scanner
	var local vulnscan.Status
	db, err := m.deps.openDB(m.log, fetch)
	if err == nil {
		if fetch {
			local, err = db.Ensure(ctx, vulnscan.EnsureOptions{Fetch: true})
		} else {
			local, err = db.Status(ctx)
		}
	}
	switch {
	case err != nil:
		notes = append(notes, "nothing rescanned: "+err.Error())
	case !local.Present:
		notes = append(notes, "nothing rescanned: no vulnerability database on this machine (`forge vuln db pull`)")
	default:
		scanner = &vulnscan.Scanner{DB: db}
	}
	rs, err := vulnscan.ScanRelease(ctx, m.store, scanner, local, rel, vulnscan.RescanIfNewer)
	if err != nil {
		return vulnGateFindings{Notes: notes}, err
	}
	return vulnGateFindings{Scans: rs.Artifacts, Notes: append(notes, rs.Notes...)}, nil
}

// readVulnFindings is findings already read: the control plane's, read once
// for both checks (hostedPromoteVulnSource).
type readVulnFindings vulnGateFindings

func (r readVulnFindings) gateFindings(context.Context, *release.Release, bool) (vulnGateFindings, error) {
	return vulnGateFindings(r), nil
}

// unreadVulnFindings is a ledger whose findings could not be read: every
// shipping artifact unscanned, for the reason given.
type unreadVulnFindings struct{ reason string }

func (u unreadVulnFindings) gateFindings(_ context.Context, rel *release.Release, _ bool) (vulnGateFindings, error) {
	var out vulnGateFindings
	for _, name := range shippingArtifacts(rel) {
		out.Scans = append(out.Scans, vulnscan.ArtifactScan{Artifact: name, Reason: u.reason})
	}
	return out, nil
}

// vulnPromoteInput is the promotion the two checks judge.
type vulnPromoteInput struct {
	Env, Version, Promoter string
	// Policy is the env's delivery policy, resolved.
	Policy release.ShapeDeliveryPolicy
	// Release is the release being promoted.
	Release *release.Release
	Grades  vulnGrades
	// Override is --override-vuln-gate's reason; "" for none.
	Override string
	// Gates are the gates the caller passed (--gate).
	Gates []release.Gate
	// Ask is false for a run that writes nothing (--plan): the database is
	// then never downloaded.
	Ask bool
	// Now dates exemptions and staleness; nil is time.Now.
	Now func() time.Time
}

// judgeVulnGates judges the release against provenance_sbom and vuln_gate, and
// returns the gates to record. src may be nil (no findings to read), which
// judges every artifact unscanned.
func judgeVulnGates(ctx context.Context, out io.Writer, in vulnPromoteInput, src vulnFindingsSource) ([]release.Gate, error) {
	sbomMode, vulnMode := in.Policy.ProvenanceSBOM.Mode, in.Policy.VulnGate.Mode
	if sbomMode == release.DeliveryOff && vulnMode == release.DeliveryOff {
		return nil, nil
	}
	if in.Release == nil {
		return nil, nil
	}
	for _, g := range in.Gates {
		if g.Name == vulnGateGateName || g.Name == sbomGateName {
			return nil, exitCodeError{code: exitWrong, msg: fmt.Sprintf(
				"--gate %s: %q is the name env %s's delivery policy records its own verdict under; name your check something else",
				g.Name, g.Name, in.Env)}
		}
	}
	override := strings.TrimSpace(in.Override)
	used := false
	var recorded []release.Gate

	if sbomMode != release.DeliveryOff {
		gate, err := judgeSBOMRequired(out, in, override, &used)
		if err != nil {
			return nil, err
		}
		if gate != nil {
			recorded = append(recorded, *gate)
		}
	}
	if vulnMode != release.DeliveryOff {
		gate, err := judgeVulnGate(ctx, out, in, src, override, &used)
		if err != nil {
			return nil, err
		}
		if gate != nil {
			recorded = append(recorded, *gate)
		}
	}
	if override != "" && !used {
		fmt.Fprintf(out, "[deploy] Note: --override-vuln-gate: %s's vuln_gate and provenance_sbom refuse nothing here, so there was nothing to override\n", in.Env)
	}
	return recorded, nil
}

// judgeSBOMRequired is provenance_sbom: every artifact the release ships has
// an SBOM. Nil gate when there is nothing to judge (a release cut by a forge
// that never recorded SBOMs).
func judgeSBOMRequired(out io.Writer, in vulnPromoteInput, override string, used *bool) (*release.Gate, error) {
	rel, g := in.Release, in.Grades.SBOM
	if !sbomEra(rel, g) {
		return nil, nil
	}
	declared := in.Policy.ProvenanceSBOM.Mode
	graded := gradedMode(declared, g)
	var missing []string
	ships := shippingArtifacts(rel)
	for _, name := range ships {
		ref := rel.Artifacts[name].SBOM
		if ref.HasSBOM() {
			continue
		}
		why := "no SBOM"
		if ref != nil && ref.Reason != "" {
			why = ref.Reason
		}
		missing = append(missing, name+" ("+why+")")
	}
	gate := release.Gate{
		Name: sbomGateName, Status: release.GateStatusPassed,
		Summary: fmt.Sprintf("every artifact has an SBOM (%d)", len(ships)),
		Details: map[string]any{"mode": string(declared), "graded_as": string(graded), "outcome": "passed"},
	}
	if len(missing) == 0 {
		return &gate, nil
	}
	gate.Status = release.GateStatusFailed
	gate.Details["missing"] = missing
	summary := fmt.Sprintf("%d of %d artifact(s) have no SBOM: %s", len(missing), len(ships), strings.Join(missing, ", "))
	if graded == release.DeliveryEnforce {
		if override == "" {
			return nil, &promoteRefusedError{Reason: reasonSBOMMissing, Detail: fmt.Sprintf(
				"%s's delivery_policy.provenance_sbom is enforce, and release %s has %s.\n"+
					"  Re-cut it with SBOMs (forge env build <env> --release <new version>, without --no-sbom), "+
					"or override this one promotion: --override-vuln-gate \"<why>\" (recorded)",
				in.Env, in.Version, summary)}
		}
		*used = true
		gate.Summary = "OVERRIDDEN by " + in.Promoter + ": " + override + "; would refuse: " + summary
		gate.Details["outcome"] = "overridden"
		gate.Details["override"] = map[string]any{"by": in.Promoter, "reason": override, "missing": missing}
		fmt.Fprintf(out, "[deploy] Note: %s's provenance_sbom would refuse this promotion (%s); overridden by %s: %s\n", in.Env, summary, in.Promoter, override)
		return &gate, nil
	}
	note := wouldBlockNote(declared, g)
	gate.Summary = "AUDIT (would refuse): " + summary + " " + note
	gate.Details["outcome"] = "audit"
	if !g.Gates && declared == release.DeliveryEnforce {
		gate.Details["grade"] = g.Note()
	}
	fmt.Fprintf(out, "[deploy] Note: %s's provenance_sbom would refuse this promotion: %s %s\n", in.Env, summary, note)
	return &gate, nil
}

// judgeVulnGate is vuln_gate: the rule over the release's findings.
func judgeVulnGate(ctx context.Context, out io.Writer, in vulnPromoteInput, src vulnFindingsSource, override string, used *bool) (*release.Gate, error) {
	rel, g := in.Release, in.Grades.Vuln
	declared := in.Policy.VulnGate.Mode
	graded := gradedMode(declared, g)
	era := sbomEra(rel, in.Grades.SBOM)
	// An audit gate over a release with no SBOM era has nothing to say and
	// is not worth a scan; an enforce is judged whatever the release, and
	// says why an old one is not refused.
	if declared != release.DeliveryEnforce && !era {
		return nil, nil
	}

	var found vulnGateFindings
	if src != nil {
		var err error
		found, err = src.gateFindings(ctx, rel, in.Ask && graded == release.DeliveryEnforce)
		if err != nil {
			return nil, undeterminedf("vuln_gate: read the findings of %s: %v", in.Version, err)
		}
	} else {
		for _, name := range shippingArtifacts(rel) {
			found.Scans = append(found.Scans, vulnscan.ArtifactScan{Artifact: name, Reason: "no findings to read for this env's ledger"})
		}
	}
	scans, notes := found.Scans, found.Notes
	if len(shippingArtifacts(rel)) == 0 {
		// A release with no artifact an SBOM describes (a version-only
		// record) has nothing scanned, which is "0 of 0 scanned" to the
		// coverage arithmetic, and a pass. It is not: could not determine
		// is never a pass. (A version-only record's source SBOM, forge
		// release record --sbom-source, is what will cover it.)
		scans = []vulnscan.ArtifactScan{{Artifact: "release " + in.Version, Reason: "it names no artifact an SBOM describes, and no source SBOM"}}
	}
	now := time.Now()
	if in.Now != nil {
		now = in.Now()
	}
	doc := vulnscan.BuildDocument(vulnscan.DocumentInput{
		Env: in.Env, Release: in.Version, Gate: in.Policy.VulnGate, GradedAs: graded, Artifacts: scans, NotApplicable: vulnscan.NotApplicableOf(rel), Now: now,
		ReleaseFindings: found.Release, Stale: found.Stale, FreshnessFromSource: found.FreshnessFromSource,
		Judged: found.Judged, JudgedBy: found.JudgedBy,
	})
	for _, n := range notes {
		fmt.Fprintf(out, "[deploy] vuln_gate: %s\n", n)
	}

	verdict := doc.Gate.Verdict
	gate := release.Gate{
		Name: vulnGateGateName, Status: release.GateStatusPassed,
		Summary: fmt.Sprintf("%s (%d critical, %d high; %d of %d artifact(s) scanned)",
			verdict, doc.Counts.Critical, doc.Counts.High, doc.Coverage.Scanned, doc.Coverage.Artifacts),
		Details: map[string]any{
			"mode": string(declared), "graded_as": string(graded), "verdict": string(verdict),
			"blocking": doc.Gate.Blocking, "outcome": "passed",
		},
	}
	if !doc.DB.BuiltAt.IsZero() {
		gate.Details["db_built_at"] = doc.DB.BuiltAt.Format("2006-01-02T15:04:05Z")
	}
	gate.Details["determined"] = doc.Gate.Determined
	if doc.Gate.JudgedBy != "" {
		gate.Details["judged_by"] = doc.Gate.JudgedBy
	}
	if doc.Gate.Determined && verdict != vulnscan.VerdictBlock {
		return &gate, nil
	}

	// Block, or could not determine: what would refuse. The freshness rule
	// is the document's (vulnscan.ScanIsCurrent, through BuildDocument): a
	// verdict counts as determined only when the newest stored scan of
	// every artifact used a database younger than five days, and stale
	// outranks block — findings an old database reports are not the
	// release's findings today.
	absence := !doc.Gate.Determined
	var what, reason string
	if absence {
		what = vulnUndeterminedSummary(doc)
		reason = reasonVulnGateUnscanned
		gate.Status = release.GateStatusSkipped
		if !era {
			// A release a forge before SBOMs cut: its absence is never
			// refused, at any pin.
			gate.Summary = fmt.Sprintf("not scanned: release %s was cut by forge %s, before SBOMs", in.Version, orUnknown(releaseForgeVersion(rel)))
			gate.Details["outcome"] = "not-applicable"
			return &gate, nil
		}
	} else {
		what = describeBlocking(doc, 5)
		reason = reasonVulnGateBlocked
		gate.Status = release.GateStatusFailed
	}
	if graded == release.DeliveryEnforce {
		if override == "" {
			return nil, &promoteRefusedError{Reason: reason, Detail: vulnRefusalDetail(in, doc, what, absence)}
		}
		*used = true
		gate.Summary = "OVERRIDDEN by " + in.Promoter + ": " + override + "; would refuse: " + what
		gate.Details["outcome"] = "overridden"
		gate.Details["override"] = map[string]any{"by": in.Promoter, "reason": override, "findings": doc.Gate.Blocking, "verdict": string(verdict)}
		fmt.Fprintf(out, "[deploy] Note: %s's vuln_gate would refuse this promotion (%s); overridden by %s: %s\n", in.Env, what, in.Promoter, override)
		return &gate, nil
	}
	note := wouldBlockNote(declared, g)
	gate.Summary = "AUDIT (would refuse): " + what + " " + note
	gate.Details["outcome"] = "audit"
	if declared == release.DeliveryEnforce && !g.Gates {
		gate.Details["grade"] = g.Note()
	}
	fmt.Fprintf(out, "[deploy] Note: %s's vuln_gate would block this promotion: %s %s\n", in.Env, what, note)
	return &gate, nil
}

// vulnUndeterminedSummary says why the verdict could not be determined.
func vulnUndeterminedSummary(doc vulnscan.Document) string {
	if doc.Gate.Verdict == vulnscan.VerdictStale {
		s := fmt.Sprintf("could not determine: the findings come from a vulnerability database built %s, older than 5 days", doc.DB.BuiltAt.Format("2006-01-02"))
		if len(doc.Gate.Blocking) > 0 {
			s += " (they include " + describeBlocking(doc, 3) + ")"
		}
		return s
	}
	var parts []string
	for _, u := range doc.Coverage.Unscanned {
		parts = append(parts, u.Artifact+" ("+u.Reason+")")
	}
	return "could not determine: not scanned: " + strings.Join(parts, ", ")
}

func vulnRefusalDetail(in vulnPromoteInput, doc vulnscan.Document, what string, absence bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s's delivery_policy.vuln_gate is enforce, and release %s %s", in.Env, in.Version, map[bool]string{true: "cannot be judged: ", false: "has a finding it blocks: "}[absence])
	b.WriteString(what)
	if absence {
		b.WriteString(".\n  Scan it: forge vuln db pull, then forge vuln scan --release " + in.Version)
	} else {
		fmt.Fprintf(&b, ".\n  → forge vuln show %s %s --release %s\n", in.Env, doc.Gate.Blocking[0], in.Version)
		fmt.Fprintf(&b, "  Fix forward in a new release; or accept one advisory, reviewed and time-boxed: forge vuln exempt %s %s --reason \"<why>\" --expires YYYY-MM-DD", in.Env, doc.Gate.Blocking[0])
	}
	b.WriteString("\n  Or override this one promotion: --override-vuln-gate \"<why>\" (recorded with who and which findings)")
	return b.String()
}

// promoteVulnDeps are the seams the promote's findings source uses; tests
// replace them.
var promoteVulnDeps = defaultVulnDeps

// checkPromotionVulnGates is the promote path's entry: the env's resolved
// policy, the release from the env's ledger, and that ledger's findings —
// this machine's, or, for a ledger-only env, its control plane's
// (hostedPromoteVulnSource).
func checkPromotionVulnGates(ctx context.Context, out io.Writer, projectDir string, ledger envLedger, pol *release.ShapeDeliveryPolicy, in promotionCheckInput) ([]release.Gate, error) {
	resolved := pol.Resolved()
	if resolved.ProvenanceSBOM.Mode == release.DeliveryOff && resolved.VulnGate.Mode == release.DeliveryOff {
		return nil, nil
	}
	if ledger.Releases == nil {
		return nil, nil
	}
	rel, err := ledger.Releases.Get(ctx, in.Version)
	if err != nil || rel == nil {
		// The promotion's own read of the release reports this on its
		// terms; the checks have nothing to judge.
		return nil, nil //nolint:nilerr // computePromotePlan reports an unreadable release
	}
	var src vulnFindingsSource
	if m, ok := ledger.Releases.(machineReleaseLedger); ok {
		src = machineVulnSource{deps: promoteVulnDeps(), store: m.store, log: out}
	}
	if hs, ok := ledger.Bindings.(*hostedStore); ok {
		var judgedThere bool
		if rel, src, judgedThere = hostedPromoteVulnSource(ctx, hs, in.Env, rel); judgedThere {
			return nil, nil
		}
	}
	return judgeVulnGates(ctx, out, vulnPromoteInput{
		Env: in.Env, Version: in.Version, Promoter: in.Promoter, Policy: resolved, Release: rel,
		Grades: gradeVulnRules(projectDir), Override: in.OverrideVulnGate, Gates: in.Gates, Ask: in.Ask,
	}, src)
}

// hostedPromoteVulnSource is the findings source of a ledger-only env (§1.5):
// the control plane's findings of rel in env, read once through
// hostedVulnSource.releaseFindings, with its verdict on each finding (Judged)
// and its freshness, exactly as `forge vuln list` reads them. It returns rel
// with each artifact's SBOM as the control plane records it (a control
// plane's release carries no SBOM refs: they travel by RecordReleaseSBOM),
// so provenance_sbom judges what the control plane holds.
//
// judgedThere reports a control plane that enforces vuln_gate itself: it
// judges the promotion in its own transaction, and forge judges nothing. A
// control plane that serves no findings, or whose findings could not be read,
// leaves every shipping artifact unscanned, with the reason.
func hostedPromoteVulnSource(ctx context.Context, hs *hostedStore, env string, rel *release.Release) (_ *release.Release, _ vulnFindingsSource, judgedThere bool) {
	f, err := hs.vulnFeatures(ctx)
	switch {
	case err != nil:
		return rel, unreadVulnFindings{reason: fmt.Sprintf("could not ask %s which vulnerability features it serves: %v", hs.endpoint, err)}, false
	case f.EnforcesGate:
		return rel, nil, true
	case !f.Findings:
		return rel, unreadVulnFindings{reason: "the control plane at " + hs.endpoint + " does not serve findings yet"}, false
	}
	declareVulnReadScope(hs.client)
	src := hostedVulnSource{reader: hostedVulnReader{client: hs.client, project: hs.project}, env: env}
	read, err := src.releaseFindings(ctx, rel.Version)
	if errors.Is(err, errHostedVulnEnvNotFound) {
		return rel, unreadVulnFindings{reason: fmt.Sprintf("env %q is not on the control plane at %s, so it has no scope or vuln_gate to read findings under", env, hs.endpoint)}, false
	}
	if err != nil {
		return rel, unreadVulnFindings{reason: fmt.Sprintf("the control plane's findings could not be read: %v", err)}, false
	}
	found := vulnGateFindings{
		Scans: read.Scans, Release: read.Release, Notes: read.Notes,
		Stale: read.Stale, FreshnessFromSource: true, Judged: read.Judged, JudgedBy: read.JudgedBy,
	}
	return hostedReleaseWithSBOMs(rel, read.Artifacts), readVulnFindings(found), false
}

// hostedReleaseWithSBOMs is rel with each artifact that has no SBOM ref given
// the one the control plane records for it. rel itself is not changed.
func hostedReleaseWithSBOMs(rel *release.Release, sboms []wireReleaseArtifactSBOM) *release.Release {
	out := *rel
	out.Artifacts = make(map[string]release.Artifact, len(rel.Artifacts))
	for name, a := range rel.Artifacts {
		out.Artifacts[name] = a
	}
	for _, w := range sboms {
		a, ok := out.Artifacts[w.ArtifactName]
		if !ok || a.SBOM != nil || w.SBOMDigest == "" {
			continue
		}
		a.SBOM = &release.SBOMRef{Digest: w.SBOMDigest, Format: w.Format, Referrer: w.Referrer, Status: release.SBOMStatusOK}
		out.Artifacts[w.ArtifactName] = a
	}
	return &out
}
