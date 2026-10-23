package cli

// A SCOPED DEPLOY RECORDS WHAT IT MOVED (console v3, D5).
//
// `forge env deploy prod v2 --target api` used to append the same promotion an
// unscoped deploy of v2 does, so the ledger said prod runs v2 while every
// workload but api still ran v1 — and release detail, workload history and the
// activity stream all read it that way. The promotion now carries the scope as
// Targets: the --target names as given, or for --frontends-only the env's
// shippable frontends by name. Readers ask release.Promotion.Moved.
//
// A SCOPE IS ONLY RECORDED WHERE IT IS TRUE. Where the env's control plane
// converges promotions — a hub that applies the recorded bundle, or hosted
// tiers whose digests the converger moves — recording a promotion to a NEW
// release moves everything the control plane converges, whatever this machine
// applies. Recording that as "only api moved" would be the same lie in the
// other direction, so it is refused before anything is written, naming the
// two deploys that are honest. Re-applying the release the env already runs is
// still allowed: it moves no binding, so there is nothing to converge.

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/reliant-labs/forge/pkg/release"
)

// renderEntitiesForScope renders the env's declaration to name its frontends.
// A var so a test states the entities.
var renderEntitiesForScope = RenderKCL

// deployScope is what a release deploy's scope flags resolve to, from one read
// of the env's declaration.
type deployScope struct {
	// Targets is the scope the deploy records on its promotion: nil for an
	// unscoped deploy, the --target names (normalized) for a targeted one,
	// and the env's shippable frontends for --frontends-only.
	Targets []string
	// AppliesBundle reports whether the deploy applies any object of the
	// env's bundle. See scopeAppliesBundle.
	AppliesBundle bool
}

// resolveDeployScope resolves a release deploy's scope flags. The declaration
// is read at most once, and only for a scoped deploy.
func resolveDeployScope(ctx context.Context, projectDir, env string, targets []string, frontendsOnly bool) (deployScope, error) {
	if !frontendsOnly && len(targets) == 0 {
		return deployScope{AppliesBundle: true}, nil
	}
	entities, err := renderEntitiesForScope(ctx, projectDir, env)
	if !frontendsOnly {
		// A --target deploy has always recorded its names without reading
		// the declaration, so a declaration that does not render must not
		// start refusing it. Unread, it is assumed to apply bundle objects:
		// the bundle-of-record check then runs, as it always has.
		scope := deployScope{Targets: release.NormalizeTargets(targets), AppliesBundle: true}
		if err == nil {
			scope.AppliesBundle = scopeAppliesBundle(entities, scope.Targets, false)
		}
		return scope, nil
	}
	if err != nil {
		return deployScope{}, fmt.Errorf("--frontends-only: read env %q's declaration to name its frontends: %w", env, err)
	}
	var names []string
	if entities != nil {
		for _, f := range entities.Frontends {
			if f.Runtime.Ships() {
				names = append(names, f.Name)
			}
		}
	}
	if len(names) == 0 {
		return deployScope{}, fmt.Errorf("--frontends-only: environment %q declares no shippable frontend to deploy (a forge.FirebaseHosting or forge.StaticSite deploy block)", env)
	}
	return deployScope{Targets: release.NormalizeTargets(names), AppliesBundle: scopeAppliesBundle(entities, nil, true)}, nil
}

// scopeAppliesBundle reports whether a deploy scoped this way applies any
// object of the env's bundle — the rendered k8s objects a release's bundle of
// record seals.
//
// A FRONTEND SHIPPED TO A STATIC HOST IS NOT IN THE BUNDLE. Firebase Hosting
// and object storage are written by the frontend dispatch, after and outside
// the cluster apply, so a deploy that ships only such frontends applies no
// bundle object at all: --frontends-only, and a --target naming only them.
// For that deploy, rendering the env's bundle and comparing it with the
// release's bundle of record guards nothing it does — and it refused, from a
// newer checkout, to ship the frontend of a release whose backend had already
// shipped (2026-10-19, control-plane prod), because sixteen workloads the
// deploy would never touch rendered differently.
//
// A target that is ALSO the name of anything that renders cluster objects (a
// workload, a database, a chart, host infra) is assumed to apply them: the
// cluster apply selects objects by that name, so the check stays.
//
// A target naming nothing the declaration knows is assumed to apply objects
// too: it may name a group of raw manifests no entity owns.
func scopeAppliesBundle(entities *KCLEntities, targets []string, frontendsOnly bool) bool {
	if frontendsOnly {
		return false
	}
	if entities == nil || len(targets) == 0 {
		return true
	}
	rendersObjects := map[string]bool{}
	for _, w := range entities.Workloads {
		rendersObjects[w.Name] = true
	}
	for _, d := range entities.Databases {
		rendersObjects[d.Name] = true
	}
	for _, c := range entities.HelmCharts {
		rendersObjects[c.Name] = true
	}
	for _, i := range entities.Infra {
		rendersObjects[i.Name] = true
	}
	shipped := map[string]bool{}
	for _, f := range entities.Frontends {
		if f.Runtime.Ships() {
			shipped[f.Name] = true
		}
	}
	for _, target := range targets {
		if !shipped[target] || rendersObjects[target] {
			return true
		}
	}
	return false
}

// releaseBundleAndPlan is a release deploy's bundle step and its §8.6 plan,
// sized to what the deploy applies.
//
// A deploy that applies bundle objects records the release's bundle (when its
// ledger is hosted) and plans against it, exactly as before. A deploy that
// applies none — see scopeAppliesBundle — does neither: it renders no bundle,
// compares none with the release's bundle of record, and computes no plan,
// because a plan is a diff of bundle objects and this deploy changes none of
// them. Its promotion is still confirmed, compare-and-set and recorded with
// its scope; its frontends still build from the release's recorded sources.
func releaseBundleAndPlan(ctx context.Context, in releaseBundleStep, scope deployScope) (*release.Plan, error) {
	if !scope.AppliesBundle {
		if in.Version != "" {
			fmt.Fprintf(in.ErrOut, "[bundle] This deploy ships only %s, which no bundle carries, so release %s's bundle for env %s is not rendered or compared.\n",
				strings.Join(scope.Targets, ", "), in.Version, in.Env)
		}
		return nil, nil
	}
	if err := ensureHostedReleaseBundle(ctx, in.ProjectDir, in.Env, in.Version, in.Ledger, in.Rerecord, in.ErrOut); err != nil {
		return nil, err
	}
	return planForDeploy(ctx, in.ProjectDir, in.Env, in.Version, in.Targets, in.Ledger, in.ErrOut), nil
}

// releaseBundleStep is what a release deploy's bundle step reads.
type releaseBundleStep struct {
	ProjectDir, Env, Version string
	// Targets are the --target names as given, for the plan's chart findings.
	Targets []string
	Ledger  envLedger
	// Rerecord is --rerecord-bundle.
	Rerecord bool
	ErrOut   io.Writer
}

// scopePromotePlan puts the deploy's scope on the plan and refuses one the
// env's control plane would widen. It returns the plan rather than writing
// through a pointer, so computePromotePlan can return it in one expression
// with no evaluation-order question.
func scopePromotePlan(plan promotePlan, scope []string, ledger envLedger) (promotePlan, error) {
	applyPlanScope(&plan, scope)
	if err := refuseConvergedScope(ledger, plan); err != nil {
		return plan, err
	}
	return plan, nil
}

// applyPlanScope puts the deploy's scope on the plan, and corrects the
// direction line for the one case scoping changes it: re-promoting the bound
// release is "no move" only when the current entry already moved everything
// this one would.
func applyPlanScope(plan *promotePlan, scope []string) {
	plan.Scope = scope
	if plan.Direction != promoteDirectionSame || len(plan.Current.Targets) == 0 {
		return
	}
	current := release.Promotion{Release: plan.Current.Release, Targets: plan.Current.Targets}
	requested := release.Promotion{Release: plan.Target.Release, Targets: scope}
	if existing, _ := release.Decide([]release.Promotion{current}, requested); existing != nil {
		return
	}
	moving := "the rest of it"
	if len(scope) > 0 {
		moving = strings.Join(scope, ", ")
	}
	plan.DirectionDetail = fmt.Sprintf("COMPLETES A SCOPED DEPLOY — %s runs %s only for %s; this moves %s",
		plan.Env, plan.Current.Release, strings.Join(plan.Current.Targets, ", "), moving)
}

// refuseConvergedScope refuses a scoped deploy that moves the binding of an env
// whose control plane converges promotions. See the file comment.
func refuseConvergedScope(ledger envLedger, plan promotePlan) error {
	if len(plan.Scope) == 0 || !ledger.Hosted || ledger.ledgerOnly() {
		return nil
	}
	if plan.Current.Bound && plan.Current.Release == plan.Target.Release {
		return nil
	}
	what := "its hosted tiers"
	if ledger.hubUnits() {
		what = "the delivery units its hub converges (" + strings.Join(ledger.Units.Hub, ", ") + ")"
	}
	runs := "nothing yet"
	if plan.Current.Bound {
		runs = plan.Current.Release
	}
	return fmt.Errorf("a scoped deploy cannot move env %q to a new release: its control plane converges every promotion, "+
		"so recording %s would also move %s, and the promotion's scope (%s) would be false.\n\n"+
		"NOTHING WAS RECORDED. Either deploy the whole release:\n"+
		"    forge env deploy %s %s\n"+
		"  or scope a re-apply of the release %s runs (%s)",
		plan.Env, plan.Target.Release, what, strings.Join(plan.Scope, ", "),
		plan.Env, plan.Target.Release, plan.Env, runs)
}
