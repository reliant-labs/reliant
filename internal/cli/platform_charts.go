package cli

// ONE INSTALL PATH for an env's declared bootstrap platform charts (Flux,
// Envoy Gateway, CNPG, cert-manager — every forge.HelmChart with the default
// `delivery = "bootstrap"`).
//
// WHY THIS EXISTS. A bundle needs these charts' CRDs and controllers already in
// the cluster. Bootstrap charts stay OUT of the bundle on purpose (a prune must
// never own platform state), so nothing that applies the bundle installs them.
// A fresh cluster would otherwise fail on the first object of a kind whose CRD
// was never installed (`no matches for kind ...`). That includes a Flux the env
// declares for its own reasons: forge installs no Flux of its own.
//
// THE FIX IS CLUSTER BOOTSTRAP, not a second deploy mechanism: rendering with
// `helm template` and applying directly is how these charts have always
// landed, and it sits outside Flux, so it is allowed for any env. Two verbs
// reach it, both through [installPlatformCharts]:
//
//   - `forge cluster up <env>` installs every bootstrap chart whose cluster is
//     a forge-managed k3d cluster of the env;
//   - `forge env deploy <env> <ver> --target <chart>` installs just that chart
//     and applies nothing else.
//
// ORDER IS LOAD-BEARING: Flux first, then charts that ship a CRD bundle, then
// the rest, each group in declaration order. A chart's Manifests (a
// GatewayClass, a ClusterIssuer) are applied by the chart's own install after
// its controllers, so the only cross-chart dependency to honour is "providers
// of CRDs before consumers of them".

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/flux"
)

// platformChartInstall is one chart bound to the kubectl context it lands in.
type platformChartInstall struct {
	Chart   HelmChartEntity
	Context string
}

// platformChartContext is the context a chart installs into: its own `cluster`,
// else the env's primary (the declared cluster target, else the first declared
// cluster).
func platformChartContext(entities *KCLEntities, c HelmChartEntity) string {
	if kctx := strings.TrimSpace(c.Cluster); kctx != "" {
		return kctx
	}
	if kctx := strings.TrimSpace(entities.ClusterTarget.field("cluster")); kctx != "" {
		return kctx
	}
	for _, declared := range entities.Clusters {
		if kctx := strings.TrimSpace(declared.Context); kctx != "" {
			return kctx
		}
	}
	return ""
}

// platformChartRank orders installs: Flux, then CRD-bundle providers, then the
// rest.
func platformChartRank(c HelmChartEntity) int {
	switch {
	case flux.ChartProvidedBy(c.OCI, c.Repo):
		return 0
	case c.CRDs != "":
		return 1
	default:
		return 2
	}
}

// platformChartInstalls resolves which charts install where, in install order.
//
// names selects by chart name (the `--target` rule); empty means every chart.
// onlyContexts, when non-nil, restricts to those contexts — `cluster up` passes
// the env's k3d clusters, because a cluster on another provider is one forge
// was only pointed at and does not bootstrap.
func platformChartInstalls(entities *KCLEntities, names []string, onlyContexts map[string]bool) []platformChartInstall {
	if entities == nil {
		return nil
	}
	var out []platformChartInstall
	for _, c := range selectedHelmChartEntities(entities.HelmCharts, names) {
		kctx := platformChartContext(entities, c)
		if kctx == "" || (onlyContexts != nil && !onlyContexts[kctx]) {
			continue
		}
		out = append(out, platformChartInstall{Chart: c, Context: kctx})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return platformChartRank(out[i].Chart) < platformChartRank(out[j].Chart)
	})
	return out
}

// applyPlatformChartFn renders one chart and applies it, CRDs first. A seam so
// the order and the targeting are testable without helm or a cluster.
var applyPlatformChartFn = cluster.ApplyHelmChart

// installPlatformCharts installs the given charts in order. Idempotent: the
// install is a render-and-apply of a version-pinned chart.
func installPlatformCharts(ctx context.Context, entities *KCLEntities, installs []platformChartInstall, owner string) error {
	for _, in := range installs {
		specs, err := helmChartSpecsFromEntities(ctx, []HelmChartEntity{in.Chart}, entities.Clusters, owner)
		if err != nil {
			return err
		}
		fmt.Printf("  installing platform chart %s %s into %s...\n", in.Chart.Name, in.Chart.Version, in.Context)
		for _, spec := range specs {
			spec.Cluster = in.Context
			if err := applyPlatformChartFn(ctx, in.Context, spec); err != nil {
				return fmt.Errorf("install platform chart %q into %q: %w", in.Chart.Name, in.Context, err)
			}
		}
	}
	return nil
}

// ensureEnvPlatformChartsInstalled is the `forge cluster up` half: bootstrap
// every declared platform chart that lands in one of the env's k3d clusters.
// Silent no-op when the env declares none.
func ensureEnvPlatformChartsInstalled(ctx context.Context, env, project string, entities *KCLEntities, clusters []ClusterEntity) error {
	k3dContexts := map[string]bool{}
	for _, c := range clusters {
		if c.Provider != "" && c.Provider != "k3d" {
			continue
		}
		if kctx := strings.TrimSpace(c.Context); kctx != "" {
			k3dContexts[kctx] = true
		}
	}
	installs := platformChartInstalls(entities, nil, k3dContexts)
	if len(installs) == 0 {
		return nil
	}
	fmt.Printf("\n[up] platform phase — installing %d declared chart(s) for env %s\n", len(installs), env)
	return installPlatformCharts(ctx, entities, installs, cluster.ChartOwner(project, env))
}

// splitPlatformChartTargets separates `--target` names into declared platform
// charts and everything else.
func splitPlatformChartTargets(entities *KCLEntities, targets []string) (charts, rest []string) {
	declared := map[string]bool{}
	if entities != nil {
		for _, c := range entities.HelmCharts {
			declared[c.Name] = true
		}
	}
	for _, t := range targets {
		if declared[t] {
			charts = append(charts, t)
		} else {
			rest = append(rest, t)
		}
	}
	return charts, rest
}
