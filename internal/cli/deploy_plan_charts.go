package cli

// CHART-OWNED OBJECTS IN THE DEPLOY PLAN.
//
// A forge.HelmChart platform dep is rendered and applied on its own path and
// rides no bundle, so a plan built from bundle shapes (every backend's) never
// saw it: a cert-manager ConfigMap change and a chart prune both shipped with
// nothing in the plan a reviewer approved. The plan is therefore completed here,
// for every backend at once, from what the deploy would do to each chart's
// objects (cluster.PlanCharts). Findings use the plan's own codes and sections,
// are marked `[chart <name>]`, and are digested — so approving a digest approves
// the chart changes too.

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/release"
)

// observeDeployCharts is what the env's charts would change. A var so a plan
// test states it without rendering KCL, helm or reaching a cluster.
var observeDeployCharts = func(ctx context.Context, projectDir, env string, targets []string) release.ChartPlan {
	entities, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		return release.ChartPlan{Unplanned: []release.ChartUnplanned{{Chart: "(env)", Reason: "the env's declaration could not be rendered: " + err.Error()}}}
	}
	project := ""
	if store, serr := loadProjectStore(); serr == nil {
		project = store.Meta().Name
	}
	// What the apply's owned-object prune would delete (owned_prune.go): the
	// env's own objects, not a chart's, but an effect of the same apply that no
	// bundle diff shows, so it rides the same section of the plan.
	pruned := observeOwnedPrune(ctx, projectDir, env, project, targets, entities)
	if entities == nil || len(selectedHelmChartEntities(entities.HelmCharts, targets)) == 0 {
		return pruned
	}
	namespace := k8sClusterFieldFromEntities(entities, "namespace")
	if namespace == "" {
		namespace = project + "-" + env
	}
	groups, err := buildDeployGroupsForEnv(env, entities, namespace, "", false)
	if err != nil {
		return release.ChartPlan{Unplanned: []release.ChartUnplanned{{Chart: "(env)", Reason: err.Error()}}}
	}
	specs, err := resolveDeployHelmSpecs(ctx, entities, targets, cluster.ChartOwner(project, env))
	if err != nil {
		return release.ChartPlan{Unplanned: []release.ChartUnplanned{{Chart: "(env)", Reason: err.Error()}}}
	}
	primary := declaredEnvContext(entities, groups)
	if primary == "" {
		primary = helmPrimaryContext(entities, groups)
	}
	charts := cluster.PlanCharts(ctx, primary, specs)
	charts.Pruned, charts.Kept, charts.PruneUnplanned = pruned.Pruned, pruned.Kept, pruned.PruneUnplanned
	return charts
}

// withChartFindings folds the chart observation into plan. It never fails the
// plan: a chart that could not be observed is a warn finding on the plan.
func withChartFindings(ctx context.Context, projectDir, env string, targets []string, plan *release.Plan, errOut io.Writer) *release.Plan {
	if plan == nil {
		return nil
	}
	charts := observeDeployCharts(ctx, projectDir, env, targets)
	folded, err := plan.WithCharts(charts)
	if err != nil {
		fmt.Fprintf(errOut, "[plan] Note: env %s's chart changes could not be added to the plan (%v).\n", env, err)
		return plan
	}
	return &folded
}

// observeOwnedPrune lists, per cluster the deploy applies to, the env's own
// objects its owned-object prune would delete. It is the same decision the apply
// makes (cluster.PlanOwnedPrune over the same render and scope), read-only, so
// `--plan-only` and `--dry-run` show what a real deploy removes.
//
// A --target deploy prunes nothing (a subset proves nothing about what the env
// stopped rendering), so it observes nothing. A cluster it cannot read is named
// in the plan as unknown, never read as "nothing to delete".
func observeOwnedPrune(ctx context.Context, projectDir, env, project string, targets []string, entities *KCLEntities) release.ChartPlan {
	owner := cluster.ObjectOwner(project, env)
	if owner == "" || len(targets) > 0 || entities == nil {
		return release.ChartPlan{}
	}
	desired, err := renderedObjectsByCluster(ctx, projectDir, env, entities)
	if err != nil {
		return release.ChartPlan{PruneUnplanned: err.Error()}
	}
	var out release.ChartPlan
	var unread []string
	contexts := make([]string, 0, len(desired))
	for kctx := range desired {
		contexts = append(contexts, kctx)
	}
	sort.Strings(contexts)
	for _, kctx := range contexts {
		rep, perr := cluster.PlanOwnedPrune(ctx, kctx, desired[kctx], owner)
		if perr != nil {
			unread = append(unread, kctx+": "+perr.Error())
			continue
		}
		out.Pruned = append(out.Pruned, rep.OwnedPruneChanges(kctx)...)
		for _, k := range rep.Kept {
			out.Kept = append(out.Kept, release.OwnedKept{
				Key: release.ObjectKey{Cluster: kctx, Kind: k.Kind, Namespace: k.Namespace, Name: k.Name}, Why: k.Why,
			})
		}
	}
	out.PruneUnplanned = strings.Join(unread, "; ")
	return out
}

// renderedObjectsByCluster is the set of objects the deploy renders for each
// cluster it applies to, keyed by kubectl context: the env's render, scoped to
// each cluster group exactly as the apply scopes it.
func renderedObjectsByCluster(ctx context.Context, projectDir, env string, entities *KCLEntities) (map[string][]cluster.ObjectRef, error) {
	store, err := loadProjectStore()
	if err != nil {
		return nil, err
	}
	mainK, err := resolveEnvMainK(store, env)
	if err != nil {
		return nil, err
	}
	namespace := k8sClusterFieldFromEntities(entities, "namespace")
	if namespace == "" {
		namespace = store.Meta().Name + "-" + env
	}
	imageTag, _ := renderImageTag(ctx, projectDir, env, "")
	manifests, err := cluster.RenderManifests(ctx, mainK, imageTag, namespace, env, loadDeployEnvConfigKV(projectDir, env), nil)
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", mainK, err)
	}
	groups, err := buildDeployGroupsForEnv(env, entities, namespace, imageTag, false)
	if err != nil {
		return nil, err
	}
	scopeFor := clusterScopeForGroups(groups, entities)
	out := map[string][]cluster.ObjectRef{}
	for _, g := range groups {
		kctx := resolveGroupContext(g)
		if g.ProviderID != "k8s-cluster" || kctx == "" {
			continue
		}
		if _, done := out[kctx]; done {
			continue
		}
		stream := manifests
		if scope := scopeFor(g); scope != nil {
			stream = cluster.ScopeManifestsToGroup(manifests, *scope)
		}
		out[kctx] = cluster.ObjectRefsOf(stream)
	}
	return out, nil
}
