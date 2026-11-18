// File: internal/cli/env_render_charts.go
//
// Platform dependencies (forge.HelmChart) in `forge env render`.
//
// A chart's objects are not in the env's KCL stream: KCL only DECLARES the
// chart, and `forge env deploy` expands it with `helm template` at apply time
// (internal/cluster.renderSelectedCharts). So a render that printed only the
// KCL stream showed 0 objects for flux, cert-manager and envoy-gateway while
// the deploy applied hundreds — the render was not a preview of the deploy,
// and no command could show a chart's manifests without contacting a cluster.
//
// The fix renders charts through the deploy's OWN function
// (cluster.RenderChartStreams wraps renderSelectedCharts), with the deploy's
// own spec resolution (resolveDeployHelmSpecs: target selection, the declared-
// cluster refusal, the pinned CRD bundles) and the deploy's own choice of the
// cluster a chart without `cluster` lands on (helmPrimaryContext). Nothing
// here models what a chart renders; it only labels and attributes it.
package cli

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/deploytarget"
)

// renderedCharts is what the chart pass contributes to a render.
type renderedCharts struct {
	// objects are every chart document, labelled with its chart and
	// attributed to the cluster the deploy applies it to.
	objects []renderedObject
	// total counts chart documents BEFORE the --kind/--name/--cluster
	// filters, so the summary's "N of M rendered" still sizes the env.
	total int
	// clusters are the contexts charts land on, which an env whose only
	// cluster-shaped declaration is a chart would otherwise never report.
	clusters []string
	// perChart is each rendered chart's name and document count, in
	// declaration order, for the summary.
	perChart []chartCount
	// skipped names charts --no-charts left out, so the summary can say the
	// render is incomplete instead of letting a short count pass as whole.
	skipped []string
}

type chartCount struct {
	name  string
	count int
}

// chartSourceLabel is the per-document marker in the stream and the --list
// APP column. A comment in the stream (legal YAML every consumer ignores)
// keeps `forge env render | kubectl diff -f -` working.
func chartSourceLabel(name string) string { return "helm chart " + name }

// renderEnvCharts templates the env's selected charts the way a deploy would
// and returns their documents. An env with no charts costs nothing: no helm,
// no network.
func renderEnvCharts(ctx context.Context, entities *KCLEntities, groups []deploytarget.ServiceGroup, opts envRenderOptions, owner string) (renderedCharts, error) {
	var out renderedCharts
	if entities == nil {
		return out, nil
	}
	selected := selectedHelmChartEntities(entities.HelmCharts, opts.targets)
	if len(selected) == 0 {
		return out, nil
	}
	if opts.noCharts {
		for _, c := range selected {
			out.skipped = append(out.skipped, c.Name)
		}
		return out, nil
	}
	if _, err := exec.LookPath("helm"); err != nil {
		names := make([]string, 0, len(selected))
		for _, c := range selected {
			names = append(names, c.Name)
		}
		return out, fmt.Errorf("the environment declares helm chart(s) %s, which a deploy expands with `helm template`, and helm is not on PATH — install helm, or pass --no-charts to render without them (the summary will say they were left out)",
			strings.Join(names, ", "))
	}

	specs, err := resolveDeployHelmSpecs(ctx, entities, opts.targets, owner)
	if err != nil {
		return out, err
	}
	streams, err := cluster.RenderChartStreams(ctx, specs)
	if err != nil {
		return out, fmt.Errorf("render helm charts (pass --no-charts to render without them): %w", err)
	}

	primary := helmPrimaryContext(entities, groups)
	seen := map[string]bool{}
	for _, cs := range streams {
		lands := cs.Cluster
		if lands == "" {
			lands = primary
		}
		var clusters []string
		if lands != "" {
			clusters = []string{lands}
			if !seen[lands] {
				seen[lands] = true
				out.clusters = append(out.clusters, lands)
			}
		}
		docs := cluster.SplitManifestDocs(cs.Stream)
		for _, doc := range docs {
			obj := renderedObject{Doc: doc, Clusters: clusters, Chart: cs.Name}
			var meta renderedObjectMeta
			if yerr := yaml.Unmarshal([]byte(doc), &meta); yerr == nil {
				obj.Kind = meta.Kind
				obj.Name = meta.Metadata.Name
				obj.Namespace = meta.Metadata.Namespace
				obj.App = cluster.ManifestGroup(meta.Metadata.Labels)
			}
			out.objects = append(out.objects, obj)
		}
		out.total += len(docs)
		out.perChart = append(out.perChart, chartCount{name: cs.Name, count: len(docs)})
	}
	return out, nil
}

// mergeClusters unions two cluster lists, sorted, for the same determinism
// reason envClusterOrder sorts: this output is diffed.
func mergeClusters(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	seen := map[string]bool{}
	var out []string
	for _, c := range append(append([]string{}, a...), b...) {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// writeChartSummary adds the chart provenance to the stderr summary: which
// charts were templated (and how many objects each contributed), and — the
// line that matters most — which were NOT, when --no-charts left them out.
func writeChartSummary(w io.Writer, charts renderedCharts) {
	for _, c := range charts.perChart {
		fmt.Fprintf(w, "[render]   chart:     %-24s %d object(s)  (helm template, as deploy applies it)\n", c.name, c.count)
	}
	if len(charts.skipped) > 0 {
		fmt.Fprintf(w, "[render]   NOT RENDERED (--no-charts): helm chart(s) %s — a deploy applies their objects too\n",
			strings.Join(charts.skipped, ", "))
	}
}
