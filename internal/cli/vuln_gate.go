package cli

// vuln_gate made a real gate, warn first (design doc §1.4, §1.8).
//
// Three places judge a release against an env's delivery_policy.vuln_gate:
//
//   - THE CUT (this file, scanReleaseAtCut): right after the SBOMs are
//     written, grype scans each one and the cut prints a summary and the
//     gate's verdict for the env it was cut for. Advisory everywhere: it never
//     fails a cut.
//   - A SELF-HOSTED PROMOTE (vuln_gate_promote.go): forge judges the
//     promotion before its own write, where it judges required_gates, and
//     records the verdict on the promotion. A control-plane env is judged by
//     its control plane (C6); forge does nothing for it there.
//   - THE vuln-sbom GATE STEP (vuln_gate_ci.go): `forge ci vuln-sbom` scans
//     the source tree's lockfiles in `task ci`.
//
// WARN FIRST. Until this release an `enforce` blocked nothing, so nobody who
// wrote one opted in to being blocked. Each place grades the declared mode
// through the lint rollout rows vuln-gate and sbom-required
// (internal/cli/lint/rollout_grade.go): while the project's forge pin is below
// the rule's GatesFrom release, enforce is judged as audit and says "would
// block (new in v1.10.0; gates from v1.11.0)". lint.rules dials it the way it
// dials every rule ({vuln-gate: error} gates now, {vuln-gate: warn} keeps it a
// warning). Both schema defaults are audit — vuln_gate (owner decision 5)
// and provenance_sbom (owner, 2026-11-20) — so a project that declares
// nothing records findings and blocks nothing, at any pin.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/cli/lint"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/vulnscan"
	"github.com/reliant-labs/forge/pkg/release"
)

// vulnGrades are both rules, graded for one project.
type vulnGrades struct {
	// Vuln grades vuln_gate = enforce.
	Vuln lint.RuleGrade
	// SBOM grades provenance_sbom = enforce.
	SBOM lint.RuleGrade
}

// gradeVulnRules grades both rules for the project at projectDir. A project
// whose forge.yaml cannot be read grades as unpinned: nothing says it is
// behind, so the rules gate, as `forge lint` grades it.
func gradeVulnRules(projectDir string) vulnGrades {
	cfg, err := config.LoadProjectDir(projectDir)
	if err != nil {
		cfg = nil
	}
	return gradeVulnRulesFor(cfg)
}

func gradeVulnRulesFor(cfg *config.ProjectConfig) vulnGrades {
	return vulnGrades{
		Vuln: lint.GradeRule(cfg, lint.RuleVulnGate),
		SBOM: lint.GradeRule(cfg, lint.RuleSBOMRequired),
	}
}

// gradedMode is the mode a declared control is judged at under its rule's
// grade: enforce is audit while the rule does not gate. off and audit are
// what they say at any pin.
func gradedMode(declared release.DeliveryMode, g lint.RuleGrade) release.DeliveryMode {
	if declared == "" {
		declared = release.DeliveryAudit
	}
	if declared == release.DeliveryEnforce && !g.Gates {
		return release.DeliveryAudit
	}
	return declared
}

// wouldBlockNote is the suffix a verdict that would refuse, but does not,
// carries: why it does not, and how to make it.
func wouldBlockNote(declared release.DeliveryMode, g lint.RuleGrade) string {
	switch {
	case declared == release.DeliveryEnforce && !g.Gates:
		if g.Dial != "" {
			return "(" + g.Note() + ")"
		}
		return fmt.Sprintf("(%s; gate now with %s)", g.Note(), g.OptIn())
	case declared == release.DeliveryAudit || declared == "":
		return "(mode audit: recorded, never refused)"
	}
	return ""
}

// releaseForgeVersion is the forge that cut rel, "" when the release does not
// say.
func releaseForgeVersion(rel *release.Release) string {
	if rel == nil || rel.Provenance == nil {
		return ""
	}
	return rel.Provenance.ForgeVersion
}

// shippingArtifacts are rel's artifacts an SBOM describes, sorted by name.
func shippingArtifacts(rel *release.Release) []string {
	var out []string
	for name, art := range rel.Artifacts {
		if vulnscan.ShipsCode(name, art) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// sbomEra reports whether rel was cut by a forge that records SBOMs, so the
// ABSENCE of one is something to judge: an artifact names an SBOM ref (ok or
// unavailable), or the forge that cut it is known and does not predate the
// rule. A release an older forge cut is never refused for lacking what that
// forge could not make (§5: "a release cut by an older forge ... is never
// refused by vuln_gate_unscanned until its project's pin gates" — and not
// then either, for an SBOM it could never have).
func sbomEra(rel *release.Release, g lint.RuleGrade) bool {
	for _, name := range shippingArtifacts(rel) {
		if rel.Artifacts[name].SBOM != nil {
			return true
		}
	}
	v := releaseForgeVersion(rel)
	if _, ok := lint.ForgeRelease(v); !ok {
		return false
	}
	return !g.Predates(v)
}

// ─── The cut ────────────────────────────────────────────────────────────────

// cutVulnDeps are the seams the cut's scan uses; tests replace them.
var cutVulnDeps = defaultVulnDeps

// cutVulnInput is what one cut's scan needs.
type cutVulnInput struct {
	ProjectDir, Env string
	Entities        *KCLEntities
	// Release is the release about to be recorded, its SBOMs set.
	Release *release.Release
	// Hosted reports that the env's ledger is a control plane, whose own
	// scan is authoritative for it.
	Hosted bool
	// Log receives progress (a database refresh).
	Log io.Writer
}

// cutVulnVerdict is what the cut prints under its release line.
type cutVulnVerdict struct {
	Env    string
	Hosted bool
	// Declared is the env's vuln_gate mode; Grade the vuln-gate rule's.
	Declared release.DeliveryMode
	Grade    lint.RuleGrade
	// NotScanned says why nothing was scanned; Doc is nil then.
	NotScanned string
	Doc        *vulnscan.Document
}

// scanReleaseAtCut scans the SBOMs a cut just generated and judges them by
// the env's vuln_gate. It never fails the cut: anything that keeps it from
// scanning is a "not scanned" line, never an error.
//
// THE DATABASE AT THE CUT. The first download is 205 MB and unpacks to 3.3
// GiB with two minutes of import; a cut is a CLI verb, but it is also every
// CI pipeline's release step, and a cut that silently grew by that much — or
// filled a runner's disk — would be a regression in the one verb that must
// stay predictable. So a cut NEVER downloads the first database: on a machine
// that has none it prints "not scanned" and the one command that fetches it
// (`forge vuln db pull`, said with its size). A machine that already has one
// opted in: the cut refreshes it when it is more than a day old, with
// grype's own progress line, exactly as `forge vuln scan` does — because a
// cut that judged against a five-day-old database would print "stale" on
// every run. The verdict at the cut is advisory either way (§1.8): the
// promotion gate judges again, and refuses an unscanned release under a
// graded enforce.
func scanReleaseAtCut(ctx context.Context, in cutVulnInput) *cutVulnVerdict {
	if in.Release == nil || in.Entities == nil || cloud.ClaimableActive() {
		return nil
	}
	pol := in.Entities.DeliveryPolicy.Resolved()
	if pol.ProvenanceSBOM.Mode == release.DeliveryOff || pol.VulnGate.Mode == release.DeliveryOff {
		return nil
	}
	deps := cutVulnDeps()
	grades := gradeVulnRules(in.ProjectDir)
	v := &cutVulnVerdict{Env: in.Env, Hosted: in.Hosted, Declared: pol.VulnGate.Mode, Grade: grades.Vuln}
	withSBOM := 0
	for _, name := range shippingArtifacts(in.Release) {
		if in.Release.Artifacts[name].SBOM.HasSBOM() {
			withSBOM++
		}
	}
	if withSBOM == 0 {
		v.NotScanned = "no artifact has an SBOM"
		return v
	}
	log := in.Log
	if log == nil {
		log = io.Discard
	}
	db, err := deps.openDB(log, true)
	if err != nil {
		v.NotScanned = err.Error()
		return v
	}
	if !db.OnDisk() {
		v.NotScanned = "no vulnerability database on this machine; `forge vuln db pull` downloads it once (~200 MB, shared by every project here)"
		return v
	}
	local, err := db.Ensure(ctx, vulnscan.EnsureOptions{Fetch: true})
	if err != nil {
		v.NotScanned = cutScanProblem(err)
		return v
	}
	store, err := deps.machineLedger(in.ProjectDir)
	if err != nil {
		v.NotScanned = "open this machine's ledger: " + err.Error()
		return v
	}
	_, _ = fmt.Fprintf(log, "[build] scanning %d SBOM(s) for known vulnerabilities (grype, database built %s)\n", withSBOM, local.BuiltAt.Format("2006-01-02"))
	rs, err := vulnscan.ScanRelease(ctx, store, &vulnscan.Scanner{DB: db}, local, in.Release, vulnscan.RescanIfNewer)
	if err != nil {
		v.NotScanned = err.Error()
		return v
	}
	doc := vulnscan.BuildDocument(vulnscan.DocumentInput{
		Env: in.Env, Release: in.Release.Version, Gate: pol.VulnGate, Notes: rs.Notes,
		GradedAs: gradedMode(pol.VulnGate.Mode, grades.Vuln), Artifacts: rs.Artifacts, NotApplicable: rs.NotApplicable, Now: deps.now(),
	})
	v.Doc = &doc
	return v
}

// cutScanProblem is one line for a database or scanner that kept the cut
// from scanning.
func cutScanProblem(err error) string {
	switch {
	case errors.Is(err, vulnscan.ErrScannerMissing):
		return "grype is unavailable on this platform: " + err.Error()
	case errors.Is(err, vulnscan.ErrStale), errors.Is(err, vulnscan.ErrNoDB):
		return err.Error()
	}
	return err.Error()
}

// printCutVulnVerdict prints the verdict under the release line:
//
//	[build] vulnerabilities: 1 critical, 4 high, 11 medium, 0 low (3 fixable, 1 KEV) · grype v0.122.1, db 2026-11-20
//	[build]   vuln_gate (prod, enforce): would block: CVE-2026-1234 in golang.org/x/net v0.30.0 (fixed in 0.33.0) (new in v1.10.0; gates from v1.11.0; …)
//	[build]   → forge vuln show prod CVE-2026-1234 · forge vuln list prod --release v1.7.0
func printCutVulnVerdict(w io.Writer, v *cutVulnVerdict) {
	if v == nil || cloud.ClaimableActive() {
		return
	}
	mode := string(v.Declared)
	if mode == "" {
		mode = string(release.DeliveryAudit)
	}
	if v.Doc == nil {
		_, _ = fmt.Fprintf(w, "[build] vulnerabilities: not scanned: %s\n", v.NotScanned)
		_, _ = fmt.Fprintf(w, "[build]   vuln_gate (%s, %s): could not determine%s\n", v.Env, mode, advisorySuffix(v))
		return
	}
	doc := v.Doc
	c := doc.Counts
	fixable, kev := 0, 0
	for _, f := range doc.Findings {
		if f.Fixable() {
			fixable++
		}
		if f.KEV {
			kev++
		}
	}
	counts := fmt.Sprintf("%d critical, %d high, %d medium, %d low", c.Critical, c.High, c.Medium, c.Low)
	if c.Unknown > 0 {
		counts += fmt.Sprintf(", %d unknown", c.Unknown)
	}
	if c.Total() > 0 {
		counts += fmt.Sprintf(" (%d fixable, %d KEV)", fixable, kev)
	}
	db := ""
	if !doc.DB.BuiltAt.IsZero() {
		db = fmt.Sprintf(" · %s %s, db %s", doc.DB.Scanner, doc.DB.Version, doc.DB.BuiltAt.Format("2006-01-02"))
	}
	_, _ = fmt.Fprintf(w, "[build] vulnerabilities: %s%s\n", counts, db)
	_, _ = fmt.Fprintf(w, "[build]   vuln_gate (%s, %s): %s%s\n", v.Env, mode, cutVerdictText(v), advisorySuffix(v))
	for _, u := range doc.Coverage.Unscanned {
		_, _ = fmt.Fprintf(w, "[build]   not scanned: %s (%s)\n", u.Artifact, u.Reason)
	}
	for _, n := range doc.Notes {
		_, _ = fmt.Fprintf(w, "[build]   note: %s\n", n)
	}
	if len(doc.Gate.Blocking) > 0 {
		_, _ = fmt.Fprintf(w, "[build]   → forge vuln show %s %s · forge vuln list %s --release %s\n", v.Env, doc.Gate.Blocking[0], v.Env, doc.Release)
	}
}

// cutVerdictText is the verdict clause of the gate line.
func cutVerdictText(v *cutVulnVerdict) string {
	doc := v.Doc
	refuses := doc.Gate.GradedAs == string(release.DeliveryEnforce)
	if !doc.Gate.Determined {
		what := "could not determine (" + string(doc.Gate.Verdict) + ")"
		if refuses {
			return what + " — a promotion is refused (" + reasonVulnGateUnscanned + ")"
		}
		return what
	}
	switch doc.Gate.Verdict {
	case vulnscan.VerdictBlock:
		what := describeBlocking(*doc, 3)
		if refuses {
			return "blocks: " + what + " — a promotion is refused (" + reasonVulnGateBlocked + ")"
		}
		return "would block: " + what + " " + wouldBlockNote(v.Declared, v.Grade)
	case vulnscan.VerdictWarn:
		return "warn: findings at or above warn_severity, none blocking"
	default:
		return string(doc.Gate.Verdict)
	}
}

// advisorySuffix says who decides for an env whose control plane scans its
// releases itself.
func advisorySuffix(v *cutVulnVerdict) string {
	if v.Hosted {
		return " (advisory: the env's control plane scans this SBOM and its verdict is the one a promotion meets)"
	}
	return ""
}

// describeBlocking names up to n blocking advisories with the package each is
// in and the version that fixes it.
func describeBlocking(doc vulnscan.Document, n int) string {
	var parts []string
	for _, id := range doc.Gate.Blocking {
		if len(parts) == n {
			break
		}
		parts = append(parts, describeAdvisory(doc, id))
	}
	s := strings.Join(parts, "; ")
	if extra := len(doc.Gate.Blocking) - len(parts); extra > 0 {
		s += fmt.Sprintf("; +%d more", extra)
	}
	return s
}

func describeAdvisory(doc vulnscan.Document, id string) string {
	for _, f := range doc.Findings {
		if f.ID != id {
			continue
		}
		s := id + " in " + f.Package.Name
		if f.Package.Version != "" {
			s += " " + f.Package.Version
		}
		if f.Fixable() && len(f.FixedIn) > 0 {
			s += " (fixed in " + strings.Join(f.FixedIn, ", ") + ")"
		}
		if f.KEV {
			s += " [KEV]"
		}
		return s
	}
	return id
}
