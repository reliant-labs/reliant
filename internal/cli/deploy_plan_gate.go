package cli

// The SERVER-BOUND half of the approval gate (O-13, doc §8.6, §13 F-19).
//
// #408 shipped the client half: the plan is printed, and nothing is written
// until somebody says yes (prompt / --yes / exit 5 with no TTY / --plan-only).
// This completes it, and the difference is what makes an approval BINDING
// rather than a claim:
//
//   - On a control-plane env the plan comes from PlanDeploy, computed
//     server-side against the RECORDED bundle, with its digest verified
//     client-side. On a file-ledger env it comes from release.BuildPlan here.
//     Both sides run the same BuildPlan, so a disagreement means the world
//     moved, not that the two differ about rules.
//   - `--approve <digest>` proceeds only if the plan is EXACTLY this one, and
//     the digest travels on the write. The server recomputes the plan under
//     the env row lock and refuses a mismatch (exit 3, plan_stale) — which is
//     what makes the digest a guarantee: a client cannot approve a plan it
//     fabricated.
//   - `--acknowledge-destructive <code>[,…]` is required for every stop-class
//     finding, and `--yes` DOES NOT satisfy it.
//
// WHY A SECOND FLAG AND NOT A BIGGER --yes. `--yes` is the flag that ends up
// hard-coded in a CI workflow, and the moment it is, it would silently
// pre-approve every future destructive change to that env. A flag that NAMES
// the finding cannot be pre-approved in advance, because the code is not known
// until the plan is computed. That asymmetry is the entire point, and it is
// why a stop finding refuses even under --yes.

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/release"
)

// deployApproval is the approval half of the gate, beside deployConfirm's
// "did a human say yes".
//
// SEPARATE FROM deployConfirm on purpose: consent and BINDING are different
// questions. deployConfirm answers "may this proceed at all"; this answers
// "proceed only against exactly this plan, and only with these destructive
// findings accepted". A caller can give one without the other, and conflating
// them would make --yes imply --approve.
type deployApproval struct {
	// Digest is --approve: the plan the caller reviewed. Empty means "no
	// plan was named", which is legitimate — an interactive operator
	// approves the plan in front of them.
	Digest string
	// AcknowledgedFindings are the stop-class codes --acknowledge-destructive
	// named.
	AcknowledgedFindings []string
}

// gateDeployOnPlan renders the §8.6 plan and applies the server-binding
// checks, BEFORE the confirmation prompt.
//
// A NIL PLAN IS NOT AN APPROVAL. It means the §8.6 plan could not be computed
// — a never-built env, a control plane that predates bundles (F-15), a
// registry that could not be reached — so there are no findings to judge. The
// promote plan is still shown and still confirmed, because that gate is about
// consent and does not need a bundle.
//
// But --approve against a nil plan is REFUSED rather than ignored. A caller
// passing a digest is asserting "this exact change set", and silently
// admitting the write because forge could not compute one would turn the
// strongest form of approval into the weakest — exactly backwards, and
// invisible.
func gateDeployOnPlan(env string, plan *release.Plan, a deployApproval, confirm *deployConfirm, jsonMode bool) error {
	if plan == nil {
		if a.Digest != "" {
			return errNoPlanToApprove(env, a.Digest)
		}
		return nil
	}
	renderDeployPlan(progressWriter(jsonMode), env, *plan)
	// confirm nil is the "no gate" case the ledger-subject tests and
	// `forge release`'s fixtures use (see promoteOptions.Confirm). The
	// destructive check still runs: --yes not covering a stop finding is a
	// property of the DEPLOY, not of whether a prompt was configured.
	yes := confirm != nil && confirm.Yes
	return checkPlanApproval(env, *plan, a, yes)
}

// errNoPlanToApprove is --approve with no plan to compare against.
func errNoPlanToApprove(env, approved string) error {
	return exitCodeError{code: exitUndetermined, msg: fmt.Sprintf(
		"--approve named plan %s for env %q, but no deploy plan could be computed, so forge cannot tell whether it is that one.\n"+
			"  A plan needs a recorded bundle to diff against. The env may never have been built, or its control plane\n"+
			"  may predate bundles.\n"+
			"  This is exit 2 (undetermined), not a refusal: the approval was neither honoured nor judged.\n"+
			"  fix: build the env first — forge env build %s — then re-plan: forge env deploy %s --plan-only",
		approved, env, env, env)}
}

// deployPlanDigest is the digest to send on the write, or "" when no plan was
// computed.
func deployPlanDigest(plan *release.Plan) string {
	if plan == nil {
		return ""
	}
	return plan.Digest
}

// checkPlanApproval is the gate's server-binding half, run AFTER the plan is
// computed and BEFORE anything is written — beside confirmDeployPlan, not
// instead of it.
//
// The order of the two checks is the policy, and it is deliberate:
//
//  1. --approve mismatch first. A caller that named a plan is asserting "this
//     exact change set"; if the plan is not that one, nothing else about the
//     request is meaningful, including which findings it acknowledged.
//  2. Then the stop-class findings. These refuse even with --yes.
func checkPlanApproval(env string, plan release.Plan, a deployApproval, yes bool) error {
	if a.Digest != "" && a.Digest != plan.Digest {
		return errPlanNotTheOneApproved(env, plan, a.Digest)
	}
	stops := plan.StopCodes()
	if len(stops) == 0 {
		return nil
	}
	if missing := unacknowledgedStops(stops, a.AcknowledgedFindings); len(missing) > 0 {
		return errStopFindingsUnacknowledged(env, plan, missing, yes)
	}
	return nil
}

// unacknowledgedStops is the stop codes the request did not name, in the
// plan's order.
//
// A code acknowledged that the plan does not carry is NOT an error: a
// two-stage pipeline may pass the codes from stage one's plan, and a finding
// that has since disappeared means the dangerous change is gone. Refusing
// there would fail a job for having become safer.
func unacknowledgedStops(stops, acknowledged []string) []string {
	named := map[string]bool{}
	for _, code := range acknowledged {
		named[strings.TrimSpace(code)] = true
	}
	var missing []string
	for _, code := range stops {
		if !named[code] {
			missing = append(missing, code)
		}
	}
	return missing
}

// errPlanNotTheOneApproved is --approve naming a different plan.
//
// Exit 3, the same code a CAS mismatch and a server plan_stale produce.
// Deliberately: to the operator these are one situation — the world moved
// under me — with one response, which is to re-plan and look at what changed.
// Splitting them would ask every pipeline to handle a distinction it does not
// act on.
func errPlanNotTheOneApproved(env string, plan release.Plan, approved string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "the plan for env %q is not the one --approve named (plan_stale), so NO promotion was written.\n", env)
	fmt.Fprintf(&b, "  approved: %s\n", approved)
	fmt.Fprintf(&b, "  current:  %s\n", plan.Digest)
	fmt.Fprintf(&b, "  Live moved between the plan and the approval — another deploy landed, a new bundle was\n"+
		"  applied, or drift appeared. The approval is bound to a change set that no longer describes this deploy.\n")
	fmt.Fprintf(&b, "  fix: re-plan and re-approve — forge env deploy %s --plan-only", env)
	return exitCodeError{code: exitConflict, msg: b.String()}
}

// errStopFindingsUnacknowledged is a stop-class finding nobody named.
//
// Exit 4 (refused), not 3 (conflict): nothing moved and nothing was lost. The
// write was declined pending a decision, and re-running it with the finding
// acknowledged is the legitimate next step — which is exactly the shape a
// pipeline should treat as "a human must look", not "retry".
func errStopFindingsUnacknowledged(env string, plan release.Plan, missing []string, yes bool) error {
	var b strings.Builder
	fmt.Fprintf(&b, "env %q: this deploy has %s nobody acknowledged (plan_unacknowledged), so NO promotion was written.\n",
		env, pluralVerb(len(missing), "a destructive change", "destructive changes"))
	for _, code := range missing {
		for _, f := range plan.Findings {
			if f.Code == code && f.Class == release.ClassStop {
				fmt.Fprintf(&b, "    %s  %s%s\n", code, f.Subject, detailSuffix(f.Detail))
			}
		}
	}
	if yes {
		// The sentence that stops this reading as a bug report. --yes was
		// given and the deploy was still refused, which is the design.
		fmt.Fprintf(&b, "  --yes means \"I read the plan\". It deliberately does NOT cover these: --yes is the flag\n"+
			"  that ends up hard-coded in a CI workflow, and a --yes that covered destructive changes would\n"+
			"  silently pre-approve every future one against this env.\n")
	}
	fmt.Fprintf(&b, "  Each must be named, which cannot be done in advance — the code is not known until the plan is computed.\n")
	fmt.Fprintf(&b, "  fix: forge env deploy %s --acknowledge-destructive %s", env, strings.Join(missing, ","))
	return exitCodeError{code: exitRefused, msg: b.String()}
}

func detailSuffix(detail string) string {
	if detail == "" {
		return ""
	}
	return " — " + detail
}

// ─── Rendering (§8.6) ────────────────────────────────────────────────────────

// renderDeployPlan prints the plan's §8.6 sections, in the doc's order.
//
// SECTION ORDER IS THE DOC'S, not severity's, and that is on purpose: an
// operator reads the same shape every time, so the per-object diff is always
// first and the stop-class sections are always in the same place. Sorting by
// severity would move things around run to run, which is how a reader starts
// skipping.
//
// A section with no findings is OMITTED, except that the whole plan says so
// when nothing was found at all — "no differences" and "not computed" must
// never render alike (F-20), so an `unknown` finding is shown like any other
// and is classed warn.
func renderDeployPlan(out io.Writer, env string, plan release.Plan) {
	fmt.Fprintf(out, "\nPlan for env %s", env)
	if plan.ReleaseVersion != "" {
		fmt.Fprintf(out, " (release %s)", plan.ReleaseVersion)
	}
	fmt.Fprintf(out, "\n")
	fmt.Fprintf(out, "  computed against: %s\n", describePlanBasis(plan.LiveBasis))

	if plan.ConfigIdentical {
		// The short-circuit, stated rather than rendered as an empty
		// diff. A no-op deploy that manufactured a diff to approve is
		// how a gate becomes click-through noise.
		fmt.Fprintf(out, "  config identical — this deploy changes images at most\n")
	}

	for _, section := range planSectionOrder {
		findings := findingsInSection(plan, section.key)
		if len(findings) == 0 {
			continue
		}
		fmt.Fprintf(out, "\n  %s\n", section.label)
		for _, f := range findings {
			fmt.Fprintf(out, "    [%s] %s%s\n", f.Class, f.Subject, detailSuffix(f.Detail))
		}
	}

	if len(plan.Findings) == 0 {
		fmt.Fprintf(out, "\n  no changes found\n")
	}
	if stops := plan.StopCodes(); len(stops) > 0 {
		fmt.Fprintf(out, "\n  DESTRUCTIVE: %s — each needs --acknowledge-destructive\n", strings.Join(stops, ", "))
	}
	fmt.Fprintf(out, "\n  plan digest: %s\n", plan.Digest)
}

// planSectionOrder is §8.6's table, in its order.
var planSectionOrder = []struct {
	key   string
	label string
}{
	{release.SectionObjects, "per-object diff"},
	{release.SectionStateful, "stateful deletions (DESTRUCTIVE)"},
	{release.SectionLB, "load-balancer identity (DESTRUCTIVE)"},
	{release.SectionImages, "image changes"},
	{release.SectionConfig, "config / flag changes"},
	{release.SectionSecrets, "new secrets needed"},
	{release.SectionDrift, "drift (changed outside forge)"},
}

// findingsInSection is one section's findings, stop first so the thing that
// gates the deploy is read before the thing that informs it.
func findingsInSection(plan release.Plan, section string) []release.PlanFinding {
	var out []release.PlanFinding
	for _, f := range plan.Findings {
		if f.Section == section {
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return classRank(out[i].Class) < classRank(out[j].Class)
	})
	return out
}

func classRank(c release.FindingClass) int {
	switch c {
	case release.ClassStop:
		return 0
	case release.ClassWarn:
		return 1
	default:
		return 2
	}
}

// describePlanBasis says what Live looked like when the plan was computed —
// the state the digest covers, and therefore the state whose change makes an
// approval stale.
//
// An UNKNOWN basis is named as such rather than rendered blank. A plan
// computed against no applied bundle is a legitimate plan (a first deploy),
// and a reader must be able to tell that from "we could not see Live".
func describePlanBasis(b release.PlanBasis) string {
	var parts []string
	if b.CurrentPromotionID != "" {
		parts = append(parts, "promotion "+b.CurrentPromotionID)
	}
	switch {
	case b.AppliedBundleID != "":
		parts = append(parts, "applied bundle "+shortDigest(b.AppliedBundleID))
	default:
		parts = append(parts, "no applied bundle (nothing recorded to diff against)")
	}
	if b.DriftObserved {
		parts = append(parts, "DRIFT observed")
	}
	if len(parts) == 0 {
		return "unknown"
	}
	return strings.Join(parts, ", ")
}
