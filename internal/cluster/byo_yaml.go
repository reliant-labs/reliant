package cluster

// Bring-your-own YAML: the two render-time sources whose objects ship IN the
// env's bundle beside forge's own — an app Helm chart (`HelmChart.delivery =
// "bundle"`) and a command that prints manifests (`forge.Generated`).
//
// ONE HOOK. Both are expanded in ExtractManifestsWithOverrides, between the
// Workload-record expansion and Bundle.overrides, which is the same seam
// forge.Manifests flows through. `forge env render`, `env shape`, the direct
// apply of a local env and the bundle all read that stream, so none of them
// has a call site of its own — and an override can address an object from
// either source.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// byoSource is one `output.generated` entry.
type byoGenerated struct {
	Name      string   `json:"name"`
	Command   []string `json:"command"`
	Dir       string   `json:"dir"`
	Cluster   string   `json:"cluster"`
	Namespace string   `json:"namespace"`
}

// byoChart is one `output.bundled_charts` entry (a HelmChart whose delivery
// is "bundle"), in the projection _render_helm_chart emits.
type byoChart struct {
	Name      string         `json:"name"`
	Chart     string         `json:"chart"`
	Repo      string         `json:"repo"`
	OCI       string         `json:"oci"`
	Version   string         `json:"version"`
	Namespace string         `json:"namespace"`
	Values    map[string]any `json:"values"`
	Cluster   string         `json:"cluster"`
}

// clusterScopedKinds never receive a defaulted namespace. Mirrors
// kcl/render.k's _CLUSTER_SCOPED, which places forge.Manifests the same way.
var clusterScopedKinds = map[string]bool{
	"Namespace": true, "CustomResourceDefinition": true, "ClusterRole": true,
	"ClusterRoleBinding": true, "ClusterIssuer": true, "StorageClass": true,
	"PriorityClass": true, "RuntimeClass": true,
	"MutatingWebhookConfiguration": true, "ValidatingWebhookConfiguration": true,
	"APIService": true, "PersistentVolume": true, "GatewayClass": true,
	"IngressClass": true, "CSIDriver": true, "Node": true,
}

// byoPrimary is the env's primary placement, the default for a source that
// names none (the same default forge.Manifests takes).
type byoPrimary struct{ cluster, namespace string }

func decodeBYOPrimary(raw any) byoPrimary {
	m, _ := raw.(map[string]any)
	c, _ := m["cluster"].(string)
	n, _ := m["namespace"].(string)
	return byoPrimary{cluster: c, namespace: n}
}

func decodeBYO[T any](raw any, key string) ([]T, error) {
	if raw == nil {
		return nil, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("output.%s: %w", key, err)
	}
	var out []T
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("output.%s: %w", key, err)
	}
	return out, nil
}

// expandBYOSources renders every bundled chart and generator the env declares
// and returns their objects, placed and owner-stamped, in declaration order
// (charts, then generators). hosted names the env's hosted workloads: a
// hosted env's bundle is admitted by the control plane as
// Workload/ManagedDatabase/StaticSite only, so neither source may be used.
func expandBYOSources(ctx context.Context, projectDir string, out map[string]any, hosted map[string]bool) ([]any, error) {
	charts, err := decodeBYO[byoChart](out["bundled_charts"], "bundled_charts")
	if err != nil {
		return nil, err
	}
	gens, err := decodeBYO[byoGenerated](out["generated"], "generated")
	if err != nil {
		return nil, err
	}
	if len(charts) == 0 && len(gens) == 0 {
		return nil, nil
	}
	if len(hosted) > 0 {
		var names []string
		for _, c := range charts {
			names = append(names, "HelmChart "+c.Name+" (delivery = \"bundle\")")
		}
		for _, g := range gens {
			names = append(names, "Generated "+g.Name)
		}
		return nil, fmt.Errorf("this env has hosted workloads (forge.OnHosted), and a hosted env's bundle carries only what the control plane admits (Workload, ManagedDatabase, StaticSite) — remove %s, or move them to an env that deploys to a cluster",
			strings.Join(names, ", "))
	}
	primary := decodeBYOPrimary(out["cluster_target"])

	var items []any
	for _, c := range charts {
		docs, err := renderBundledChart(ctx, c)
		if err != nil {
			return nil, err
		}
		// helm already placed namespaced objects in the chart's namespace;
		// only the cluster is defaulted.
		items = append(items, placeBYO(docs, c.Name, firstNonEmpty(c.Cluster, primary.cluster), "")...)
	}
	for _, g := range gens {
		docs, err := runGenerated(ctx, projectDir, g)
		if err != nil {
			return nil, err
		}
		items = append(items, placeBYO(docs, g.Name, firstNonEmpty(g.Cluster, primary.cluster), firstNonEmpty(g.Namespace, primary.namespace))...)
	}
	return items, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// renderBundledChart is `helm template --skip-crds` through the SAME
// renderer a bootstrap chart uses, so values, version, namespace and
// post-hook handling cannot diverge between the two deliveries.
func renderBundledChart(ctx context.Context, c byoChart) ([]any, error) {
	spec := HelmChartSpec{
		Name: c.Name, Chart: c.Chart, Repo: c.Repo, OCI: c.OCI,
		Version: c.Version, Namespace: c.Namespace, Values: c.Values,
	}
	rendered, err := helmTemplate(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("helm chart %q (delivery = \"bundle\"): %w", c.Name, err)
	}
	docs, err := decodeObjectStream(dropPostHelmHooks(rendered))
	if err != nil {
		return nil, fmt.Errorf("helm chart %q (delivery = \"bundle\"): %w", c.Name, err)
	}
	return docs, nil
}

// runGenerated runs one forge.Generated command and parses its stdout.
func runGenerated(ctx context.Context, projectDir string, g byoGenerated) ([]any, error) {
	if len(g.Command) == 0 {
		return nil, fmt.Errorf("generator %q: empty command", g.Name)
	}
	root := projectDir
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("generator %q: %w", g.Name, err)
		}
		root = wd
	}
	dir := root
	if g.Dir != "" {
		dir = filepath.Join(root, g.Dir)
		if rel, err := filepath.Rel(root, dir); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("generator %q: dir %q escapes the project root", g.Name, g.Dir)
		}
	}
	cmd := exec.CommandContext(ctx, g.Command[0], g.Command[1:]...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("generator %q (%s) failed: %w\n%s", g.Name, strings.Join(g.Command, " "), err, strings.TrimSpace(stderr.String()))
	}
	docs, err := decodeObjectStream(stdout.String())
	if err != nil {
		return nil, fmt.Errorf("generator %q (%s): %w", g.Name, strings.Join(g.Command, " "), err)
	}
	return docs, nil
}

// decodeObjectStream parses a multi-document YAML stream of Kubernetes
// objects. An empty document is skipped; a non-object, or one without
// apiVersion and kind, is an error — a stream that parses but is not
// manifests would otherwise ship as an object nothing can apply.
func decodeObjectStream(stream string) ([]any, error) {
	dec := yaml.NewDecoder(strings.NewReader(stream))
	var items []any
	for i := 1; ; i++ {
		var doc any
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				return items, nil
			}
			return nil, fmt.Errorf("output is not a YAML stream (document %d): %w", i, err)
		}
		if doc == nil {
			continue
		}
		m, ok := doc.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("document %d is not a Kubernetes object (got %T)", i, doc)
		}
		if s, _ := m["kind"].(string); s == "" {
			return nil, fmt.Errorf("document %d has no kind", i)
		}
		if s, _ := m["apiVersion"].(string); s == "" {
			return nil, fmt.Errorf("document %d (kind %v) has no apiVersion", i, m["kind"])
		}
		items = append(items, m)
	}
}

// placeBYO stamps each object exactly as a named forge.Manifests group does:
// `forge.dev/workload` is forced to the source's name (its --target), the
// app.kubernetes.io name/managed-by labels are defaulted only where absent,
// the cluster routing label is stamped, and — for the sources that ask — a
// namespaced object that names no namespace gets one.
func placeBYO(docs []any, name, cluster, namespace string) []any {
	for _, d := range docs {
		m := d.(map[string]any)
		meta, _ := m["metadata"].(map[string]any)
		if meta == nil {
			meta = map[string]any{}
		}
		labels, _ := meta["labels"].(map[string]any)
		if labels == nil {
			labels = map[string]any{}
		}
		labels[WorkloadLabel] = name
		if _, ok := labels[AppNameLabel]; !ok {
			labels[AppNameLabel] = name
		}
		if _, ok := labels["app.kubernetes.io/managed-by"]; !ok {
			labels["app.kubernetes.io/managed-by"] = "forge"
		}
		if cluster != "" {
			labels[ClusterRoutingLabel] = cluster
		}
		meta["labels"] = labels
		if kind, _ := m["kind"].(string); namespace != "" && meta["namespace"] == nil && !clusterScopedKinds[kind] {
			meta["namespace"] = namespace
		}
		m["metadata"] = meta
	}
	return docs
}
