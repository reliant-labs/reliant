package cli

import (
	"fmt"
	"sort"
	"strings"
)

// enableBackendHelmValue is the Envoy Gateway controller config that turns on
// the Backend extension API. Spelled as the `--set` key a reader can paste,
// because naming the concept without the key leaves them the expensive half of
// the search.
const enableBackendHelmValue = "config.envoyGateway.extensionApis.enableBackend=true"

// enableBackendValuePath is the same setting as a path into a chart's `values`
// tree, used to detect that the project already set it.
var enableBackendValuePath = []string{"config", "envoyGateway", "extensionApis", "enableBackend"}

// routeBackendPrereqFindings reports the out-of-band prerequisite a route
// carries when it targets a workload running as a HOST process: Envoy Gateway's
// Backend extension API has to be enabled on the controller.
//
// WHY THIS IS A FINDING. A route to an OnHost workload cannot reach it through
// a Service — the process is not in the cluster — so forge renders an Envoy
// Gateway `Backend` with an FQDN endpoint. Envoy Gateway refuses to resolve one
// unless the Backend API is explicitly enabled, and it refuses it LATE:
//
//	ResolvedRefs=False(UnsupportedValue) Failed to process route rule 0
//	  backendRef 0: Backend is disabled in Envoy Gateway configuration.
//
// The apply is clean and the listener returns 500. Nothing in that signal
// points at a helm value on a chart declared in another file, so the cost is
// entirely in the time spent suspecting the route, the port, and the app first.
//
// WHY NOT A FORGE-RENDERED DEFAULT. The Envoy Gateway chart is PROJECT-OWNED —
// a forge.HelmChart the project declares, carrying its own `values`. forge
// writing `enableBackend` into it would be forge editing a declaration the
// project owns, and editing it INVISIBLY: the project's file would say one
// thing and the applied controller config another, so the next person to debug
// the chart would read the wrong source. Naming the value and leaving it to the
// owner keeps one source of truth for a chart whose values are the whole point
// of declaring it. (Where forge owns the chart itself, the default is the right
// answer — the ownership is what decides, not the convenience.)
//
// Returns nil when nothing renders a Backend, and nil when the prerequisite is
// already satisfied: a finding that keeps firing after it has been addressed
// teaches people to ignore the channel it arrives on.
func routeBackendPrereqFindings(entities *KCLEntities) []string {
	if entities == nil {
		return nil
	}
	routes := routesRenderingAHostBackend(entities)
	if len(routes) == 0 {
		return nil
	}

	// An env may declare the controller chart (and so can set the value in the
	// declaration) or install it some other way. Either way the prerequisite
	// holds; only the place to set it differs.
	chart, declared := envoyGatewayChart(entities)
	if declared && chartEnablesBackendAPI(chart) {
		return nil
	}

	sort.Strings(routes)
	where := fmt.Sprintf("the %q chart's `values`", chart.Name)
	if !declared {
		where = "the Envoy Gateway controller's config, wherever this env installs it"
	}
	return []string{fmt.Sprintf(
		"envoy-backend-api: %s target a workload that runs as a host process, so forge renders "+
			"an Envoy Gateway Backend. Envoy Gateway refuses to resolve one unless the Backend "+
			"extension API is enabled — set `%s` in %s. Without it the deploy applies cleanly and "+
			"the route then fails with \"Backend is disabled in Envoy Gateway configuration\". "+
			"The controller reads this at start, so an already-running one needs a restart.",
		describeRoutes(routes), enableBackendHelmValue, where)}
}

// routesRenderingAHostBackend returns the labelled routes whose backend is a
// workload this env runs as a host process — exactly the routes that render an
// Envoy Gateway Backend. A route naming a `service`, or a workload that runs
// in-cluster, resolves to an ordinary Service and carries no prerequisite.
func routesRenderingAHostBackend(entities *KCLEntities) []string {
	onHost := map[string]bool{}
	for _, w := range entities.WorkloadsOn(RuntimeHost) {
		onHost[w.Name] = true
	}
	if len(onHost) == 0 {
		return nil
	}
	var out []string
	for _, r := range entities.HTTPRoutes {
		if r.Workload != "" && onHost[r.Workload] {
			out = append(out, "HTTPRoute "+r.Name)
		}
	}
	for _, r := range entities.GRPCRoutes {
		if r.Workload != "" && onHost[r.Workload] {
			out = append(out, "GRPCRoute "+r.Name)
		}
	}
	return out
}

// envoyGatewayChart returns the declared chart that installs the Envoy Gateway
// controller, identified by the CRD bundle it delegates to forge
// (crds="gateway-api") rather than by chart name — the same key the rest of the
// helm path uses to decide Gateway API ownership, so the two cannot disagree
// about which chart this is.
func envoyGatewayChart(entities *KCLEntities) (HelmChartEntity, bool) {
	for _, c := range entities.HelmCharts {
		if c.CRDs == "gateway-api" {
			return c, true
		}
	}
	return HelmChartEntity{}, false
}

// chartEnablesBackendAPI reports whether a chart's declared values already
// enable the Backend extension API, in EITHER spelling a project may use: a
// nested values tree, or the flattened dotted key that mirrors `--set`. Both
// reach the same chart value, so recognising only one would keep warning a
// project that had already done the work.
func chartEnablesBackendAPI(chart HelmChartEntity) bool {
	if len(chart.Values) == 0 {
		return false
	}
	if truthyValue(chart.Values[strings.Join(enableBackendValuePath, ".")]) {
		return true
	}
	var cur any = chart.Values
	for _, key := range enableBackendValuePath {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		cur, ok = m[key]
		if !ok {
			return false
		}
	}
	return truthyValue(cur)
}

// truthyValue reads a helm value that may have survived a YAML/JSON round trip
// as a bool or as its string spelling.
func truthyValue(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(strings.TrimSpace(t), "true")
	default:
		return false
	}
}

// describeRoutes renders a route list as prose, so one route reads "HTTPRoute x
// targets" rather than a bracketed slice.
func describeRoutes(routes []string) string {
	if len(routes) == 1 {
		return routes[0] + " targets"
	}
	return strings.Join(routes, ", ") + " each"
}
