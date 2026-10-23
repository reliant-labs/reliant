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
	if entities == nil || len(selectedHelmChartEntities(entities.HelmCharts, targets)) == 0 {
		return release.ChartPlan{}
	}
	project := ""
	if store, serr := loadProjectStore(); serr == nil {
		project = store.Meta().Name
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
	return cluster.PlanCharts(ctx, primary, specs)
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
