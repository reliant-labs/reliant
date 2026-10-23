package cli

// THE PLAN OF AN ENV FORGE ITSELF APPLIES, against what forge last applied.
//
// A FORGE-PLANNED env (envLedger.forgePlans) is one whose control plane cannot
// see all of what is applied: a LEDGER-ONLY env (a hosted ledger, nothing bound
// to its control plane — control-plane prod before any unit moved), or an env
// that has moved only SOME delivery units to the hub, so forge still applies
// the rest. The hub observes its own units; nothing observes forge's.
//
// The ledger-only case is the clearest. It is converged by nothing but this
// machine's apply. Its control plane records promotions and observes nothing:
// it holds no deployment rows to carry an applied_bundle_id, and no Flux it
// could read a convergence from. So the server's PlanDeploy had no Live to
// diff against, ever — every prod plan on 2026-10-10 carried 177 `object_added`
// for objects that had existed for months, 28 `secret_needed` warns for Secrets
// that had not moved, and no way to show a removal at all. A real change was
// indistinguishable from the noise, and four releases were approved on it.
//
// THE ACTUATOR PLANS. The plan is computed by whoever applies the env, because
// only the applier can see what the plan has to read:
//
//   - LIVE is the bundle of this env's newest SUCCEEDED client-side apply. The
//     apply records it as evidence on the promotion it realized — the "apply"
//     gate (apply_outcome.go), whose details name the bundle digest — so the
//     baseline is on the env's own ledger, keyed by env and digest, readable
//     from any machine that can deploy, and attributed by the server to the
//     credential that applied.
//   - SECRET PRESENCE for an `external` secret is the target cluster's own
//     Secret, read keys-only (deploy_plan_secrets.go): the control plane
//     cannot see a self-managed cluster, and forge holds its kubectl context.
//     A `hosted` secret is still read from the control plane's store, as the
//     server's plan read it.
//
// It is the same release.BuildPlan the server and the file ledger run, over the
// same rules, so a finding means the same thing on every backend. What differs
// is only who could observe the inputs — which was the defect.
//
// THE DIGEST IS NOT SENT ON THE WRITE for such an env (serverPlanDigest). The
// control plane re-verifies a digest it is given by recomputing the plan, and
// it cannot recompute one over Live it never sees; it would refuse every
// approval as plan_stale. The approval is still binding where it can be: forge
// recomputes this plan immediately before the write and refuses `--approve`
// for any other digest (checkPlanApproval), the promotion's compare-and-set
// still guards the pointer, and the approved digest is recorded beside the
// applied bundle on the apply gate.

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/release"
)

// The apply gate's details keys: what was applied, and what review approved it.
const (
	applyGateBundleDigest = "bundle_digest"
	applyGatePlanDigest   = "plan_digest"
)

// appliedBaselineMaxPromotions bounds how far back the baseline walk reads. A
// run of failed applies that long means Live is genuinely unknown, and the
// plan says so rather than paging the whole ledger.
const appliedBaselineMaxPromotions = 200

// serverPlanDigest is the plan digest to send on the promotion write: the
// plan's, unless the plan is the actuator's own (a ledger-only env), which the
// control plane cannot recompute and would refuse as stale.
func serverPlanDigest(plan *release.Plan, ledger envLedger) string {
	if ledger.forgePlans() || plan == nil {
		return ""
	}
	// A server recomputes the plan from bundles alone and cannot see chart
	// findings, so it is sent the digest it can derive. The approval is still
	// binding on the full digest: forge refuses --approve for any plan but its
	// own recomputed one (checkPlanApproval), chart findings included.
	if plan.BaseDigest != "" {
		return plan.BaseDigest
	}
	return deployPlanDigest(plan)
}

// bundleByDigest is the one bundle read the baseline needs.
type bundleByDigest interface {
	GetBundleByDigest(ctx context.Context, env, environmentID, digest string) (*release.BundleRecord, error)
}

// ledgerOnlyDeployPlan builds the plan here for an env forge alone applies:
// the candidate bundle the control plane recorded, against the bundle of the
// env's newest succeeded apply.
//
// A nil plan with a nil error is "no recorded candidate" — the same real state
// hostedDeployPlan reports — and the deploy continues behind the confirmation
// gate.
func ledgerOnlyDeployPlan(ctx context.Context, projectDir, env string, ledger envLedger, bundleDigest string, errOut io.Writer) (*release.Plan, error) {
	store, err := hostedRecordStoreForDeploy(ctx, projectDir, env)
	if err != nil {
		return nil, err
	}
	envID, err := store.envID(ctx, env)
	if err != nil {
		return nil, err
	}
	bundles := store.Bundles()
	candidate, err := bundles.GetBundleByDigest(ctx, env, envID, bundleDigest)
	if err != nil {
		if plan, handled := planUnavailable(err, env, errOut); handled {
			return plan, nil
		}
		return nil, err
	}
	if candidate == nil {
		fmt.Fprintf(errOut,
			"[plan] env %s has no recorded bundle for %s, so no deploy plan could be computed.\n"+
				"[plan]   The deploy continues behind the confirmation gate; `forge env build %s` records one.\n",
			env, shortDigest(bundleDigest), env)
		return nil, nil
	}

	applied, err := appliedBaseline(ctx, ledger.Bindings, bundles, env, envID)
	if err != nil {
		return nil, fmt.Errorf("read env %s's last applied bundle: %w", env, err)
	}
	var live *release.Shape
	var basis release.PlanBasis
	if applied != nil {
		shape := applied.Shape
		live = &shape
		basis = release.PlanBasis{AppliedBundleID: applied.ID, AppliedConfigDigest: applied.ConfigDigest}
	}
	if current, bound, cerr := ledger.Bindings.Current(ctx, env); cerr == nil && bound {
		basis.CurrentPromotionID = current.ID
	}

	// Presence from both places a secret can live: the cluster for an
	// `external` one, the control plane's own store for a `hosted` one —
	// the half the server's plan read, kept so moving the plan here loses
	// nothing it used to say.
	presence := deployPlanSecretPresence(ctx, projectDir, env, candidate.Shape)
	for name, set := range managedSecretPresence(ctx, store.client, envID, candidate.Shape, live) {
		if presence == nil {
			presence = map[string]bool{}
		}
		presence[name] = set
	}

	plan, err := release.BuildPlan(release.PlanInput{
		EnvironmentID:         envID,
		BundleID:              candidate.ID,
		ReleaseVersion:        candidate.Release,
		Candidate:             candidate.Shape,
		CandidateConfigDigest: candidate.ConfigDigest,
		Live:                  live,
		Basis:                 basis,
		SecretPresence:        presence,
		// Drift: nil — nothing here compares the live objects with the
		// applied bundle yet, so BuildPlan says drift is not observable
		// rather than claiming a clean cluster forge never looked at.
	})
	if err != nil {
		return nil, err
	}
	plan.ComputedAt = time.Now().UTC()
	if applied != nil {
		annotateChangedFields(ctx, projectDir, env, &plan, applied.Reference, candidate.Reference)
	}
	noteDirectApplyRemovals(&plan)
	return &plan, nil
}

// directApplyRemovalNote is what a removal means when forge applies the env
// itself: `kubectl apply` adds and updates, and nothing reconciles the set, so
// an object the render stopped producing stays in the cluster.
const directApplyRemovalNote = "this deploy does NOT delete it (forge applies directly; only --prune removes Deployments, and a forge.HelmChart's own objects are pruned after the chart applies) — if it still exists, delete it by hand; the next plan will not list it again"

// directApplyJobRemovalNote is the same finding for a hash-named one-shot Job.
// A new spec is a new Job name, so every release that changes one reports the
// previous Job as removed; the apply prunes those itself (internal/cluster
// job_prune.go), so "delete it by hand" would be wrong advice.
var directApplyJobRemovalNote = fmt.Sprintf("this deploy leaves it until it is superseded: forge's direct apply keeps the newest %d finished Jobs of each one-shot name (the current release's included) and deletes the older finished ones after the apply; a running Job is never deleted", cluster.KeepFinishedJobs)

// removalNote picks the detail for an object_removed finding by the kind in its
// subject ("<cluster>/<kind>/<ns>/<name>", read from the right so a cluster
// name cannot shift it).
func removalNote(subject string) string {
	parts := strings.Split(subject, "/")
	if len(parts) >= 4 && parts[len(parts)-3] == "Job" {
		return directApplyJobRemovalNote
	}
	return directApplyRemovalNote
}

// noteDirectApplyRemovals says so on every object_removed finding. Detail is
// not digested, so this changes what a reviewer reads, never the approval.
func noteDirectApplyRemovals(plan *release.Plan) {
	for i := range plan.Findings {
		if f := &plan.Findings[i]; f.Code == release.FindingObjectRemoved && f.Detail == "" {
			f.Detail = removalNote(f.Subject)
		}
	}
}

// appliedBaseline is the Live side for a ledger-only env: the bundle named by
// the env's NEWEST SUCCEEDED apply gate, walking promotions newest first. nil
// means Live is unknown.
//
// NEWEST SUCCEEDED, for appliedBundleShape's reason: a failed apply does not
// describe what is running, and diffing against it would show its changes as
// already applied and hide them from the plan about to apply them for real. A
// promotion with no apply gate (a pointer move nothing applied) is skipped for
// the same reason — the cluster still runs what was last applied.
//
// THE WALK STOPS AT THAT GATE, even when it cannot name a bundle (one recorded
// before apply gates carried a digest, or a bundle the control plane no longer
// holds). Falling through to an older, identifiable apply would diff against
// something the cluster no longer runs — and that can UNDER-report: an object
// the unnamed apply changed and this candidate reverts would read as
// unchanged. Unknown is the honest answer, and BuildPlan says so.
func appliedBaseline(ctx context.Context, bindings bindingStore, bundles bundleByDigest, env, envID string) (*release.BundleRecord, error) {
	reader, ok := bindings.(bindingHistoryReader)
	if !ok {
		return nil, nil
	}
	before := ""
	for seen := 0; seen < appliedBaselineMaxPromotions; {
		page, err := reader.HistoryPage(ctx, env, historyQuery{Limit: 50, Before: before})
		if err != nil {
			return nil, err
		}
		for _, p := range page.Promotions {
			seen++
			gate, applied := newestSucceededApply(p)
			if !applied {
				continue
			}
			digest := gateDetail(gate, applyGateBundleDigest)
			if digest == "" {
				return nil, nil
			}
			return bundles.GetBundleByDigest(ctx, env, envID, digest)
		}
		if page.Next == "" || len(page.Promotions) == 0 {
			break
		}
		before = page.Next
	}
	return nil, nil
}

// managedSecretPresence reads the control plane's own secret store for the
// secrets either side declares with the managed provider — names only
// (ListSecrets has no value field). Every name it could read is an explicit
// true or false; a failed read is nil, which leaves them unverifiable.
func managedSecretPresence(ctx context.Context, client cloudCaller, envID string, candidate release.Shape, live *release.Shape) map[string]bool {
	managed := map[string]bool{}
	shapes := []release.Shape{candidate}
	if live != nil {
		shapes = append(shapes, *live)
	}
	for _, shape := range shapes {
		for _, s := range shape.Secrets {
			if s.Provider == "hosted" || s.Provider == "managed" {
				managed[s.Name] = true
			}
		}
	}
	if len(managed) == 0 || client == nil {
		return nil
	}
	summaries, err := cloudSecretWriter{client: client, environmentID: envID}.List(ctx)
	if err != nil {
		return nil
	}
	set := map[string]bool{}
	for _, s := range summaries {
		set[s.Name] = s.present()
	}
	presence := make(map[string]bool, len(managed))
	for name := range managed {
		presence[name] = set[name]
	}
	return presence
}

// newestSucceededApply is the newest passed "apply" gate on p. Recorded gates
// are in append order, so it walks backwards.
func newestSucceededApply(p release.Promotion) (release.Gate, bool) {
	for i := len(p.RecordedGates) - 1; i >= 0; i-- {
		g := p.RecordedGates[i]
		if g.Name == applyGateName && g.Status == release.GateStatusPassed {
			return g, true
		}
	}
	return release.Gate{}, false
}

// gateDetail reads one string from a gate's details, "" when absent.
func gateDetail(g release.Gate, key string) string {
	s, _ := g.Details[key].(string)
	return s
}
