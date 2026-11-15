package cluster

// What a deploy would do to a chart's objects, observed BEFORE it does it.
//
// A chart is rendered and applied on its own path (applyRenderedCharts) and
// rides no bundle, so the deploy plan's bundle diff cannot see it. This file
// observes the same two effects that path has:
//
//   - apply: every document the chart renders is compared with the live object.
//     A missing one is an add, one whose rendered fields differ is a change.
//   - prune: PruneChartObjects' own decision (chartPruneCandidates) names the
//     objects the chart stopped rendering, which the apply then deletes.
//
// The comparison is SUBSET-wise: only fields the chart renders count, because
// the apiserver adds defaults, status and bookkeeping the chart never wrote and
// a field-for-field diff would report all of it as a change on every deploy.
// It is read-only (kubectl get only), and a Secret is never read.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/reliant-labs/forge/pkg/release"
)

// PlanCharts observes what applying specs to their clusters would change.
// envContext is the env's primary kubectl context, which a chart that declares
// no cluster of its own applies to.
//
// A chart it cannot observe is reported as unplanned rather than left out: the
// plan must never read "this chart does not change" for one it could not read.
func PlanCharts(ctx context.Context, envContext string, specs []HelmChartSpec) release.ChartPlan {
	var out release.ChartPlan
	for _, spec := range specs {
		kctx := strings.TrimSpace(chartContext(envContext, spec))
		if kctx == "" {
			out.Unplanned = append(out.Unplanned, release.ChartUnplanned{Chart: spec.Name, Reason: "no kubectl context to read it from"})
			continue
		}
		charts, err := renderSelectedCharts(ctx, []HelmChartSpec{spec})
		if err != nil {
			out.Unplanned = append(out.Unplanned, release.ChartUnplanned{Chart: spec.Name, Reason: err.Error()})
			continue
		}
		for _, rc := range charts {
			changes, err := planChart(ctx, kctx, rc, kubectlChartReader{})
			if err != nil {
				out.Unplanned = append(out.Unplanned, release.ChartUnplanned{Chart: spec.Name, Reason: err.Error()})
				continue
			}
			out.Changes = append(out.Changes, changes...)
		}
	}
	return out
}

// chartLiveReader is the one cluster read the apply diff makes.
type chartLiveReader interface {
	// Get returns the live objects of one resource type named in one
	// namespace ("" for none). A name that does not exist is simply absent;
	// a resource type the cluster does not serve returns none and no error,
	// since a CRD the chart is about to install is exactly that.
	Get(ctx context.Context, kctx, resource, namespace string, names []string) ([]map[string]any, error)

	// PruneCandidates is PruneChartObjects' decision without its effect.
	PruneCandidates(ctx context.Context, kctx string, rc renderedChart) ([]liveChartObject, bool, error)
}

type kubectlChartReader struct{}

func (kubectlChartReader) PruneCandidates(ctx context.Context, kctx string, rc renderedChart) ([]liveChartObject, bool, error) {
	if !rc.spec.Prune || rc.spec.Owner == "" {
		return nil, false, nil
	}
	del, _, readable, err := chartPruneCandidates(ctx, kctx, rc)
	return del, readable, err
}

func (kubectlChartReader) Get(ctx context.Context, kctx, resource, namespace string, names []string) ([]map[string]any, error) {
	args := append([]string{"get", resource}, names...)
	args = append(args, "-o", "json", "--ignore-not-found")
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	out, err := kubectlCmd(ctx, kctx, args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && strings.Contains(string(ee.Stderr), "doesn't have a resource type") {
			return nil, nil
		}
		msg := err.Error()
		if errors.As(err, &ee) {
			msg += ": " + strings.TrimSpace(string(ee.Stderr))
		}
		return nil, fmt.Errorf("kubectl get %s: %s", resource, msg)
	}
	return parseLiveGet(out)
}

// parseLiveGet reads `kubectl get -o json`: a List for several names, the bare
// object for one, nothing for none found.
func parseLiveGet(out []byte) ([]map[string]any, error) {
	if strings.TrimSpace(string(out)) == "" {
		return nil, nil
	}
	var top map[string]any
	if err := json.Unmarshal(out, &top); err != nil {
		return nil, fmt.Errorf("parse kubectl get output: %w", err)
	}
	if items, ok := top["items"].([]any); ok {
		var objs []map[string]any
		for _, it := range items {
			if m, ok := it.(map[string]any); ok {
				objs = append(objs, m)
			}
		}
		return objs, nil
	}
	return []map[string]any{top}, nil
}

// chartDoc is one document a chart renders.
type chartDoc struct {
	kind, name, namespace, apiVersion string
	body                              map[string]any
}

func chartDocsOf(rc renderedChart) ([]chartDoc, error) {
	var docs []chartDoc
	index := map[chartObjectRef]int{}
	for _, stream := range []string{rc.crds, rc.manifests, rc.extra} {
		for _, raw := range splitDocs(stream) {
			var body map[string]any
			if err := yaml.Unmarshal([]byte(raw), &body); err != nil {
				return nil, fmt.Errorf("a rendered document does not parse: %w", err)
			}
			if body == nil {
				continue
			}
			kind, _ := body["kind"].(string)
			md, _ := body["metadata"].(map[string]any)
			name, _ := md["name"].(string)
			if kind == "" || name == "" {
				continue
			}
			ns, _ := md["namespace"].(string)
			api, _ := body["apiVersion"].(string)
			if clusterScopedKinds[kind] {
				ns = ""
			} else if ns == "" {
				ns = rc.spec.Namespace
			}
			d := chartDoc{kind: kind, name: name, namespace: ns, apiVersion: api, body: body}
			ref := chartObjectRef{kind, ns, name}
			if i, dup := index[ref]; dup {
				docs[i] = d
				continue
			}
			index[ref] = len(docs)
			docs = append(docs, d)
		}
	}
	return docs, nil
}

// resourceOf is the argument kubectl takes for a document's type: kind.group,
// or the bare kind in the core group.
func resourceOf(apiVersion, kind string) string {
	if g, _, found := strings.Cut(apiVersion, "/"); found {
		return strings.ToLower(kind) + "." + g
	}
	return strings.ToLower(kind)
}

func planChart(ctx context.Context, kctx string, rc renderedChart, live chartLiveReader) ([]release.ChartChange, error) {
	docs, err := chartDocsOf(rc)
	if err != nil {
		return nil, err
	}
	type group struct {
		resource, namespace string
		names               []string
	}
	groups := map[string]*group{}
	var order []string
	for _, d := range docs {
		// A Secret's value is never read into this process (the same rule as
		// the prune), so a chart's Secrets are not part of the comparison.
		if d.kind == "Secret" {
			continue
		}
		res := resourceOf(d.apiVersion, d.kind)
		key := res + "|" + d.namespace
		g := groups[key]
		if g == nil {
			g = &group{resource: res, namespace: d.namespace}
			groups[key] = g
			order = append(order, key)
		}
		g.names = append(g.names, d.name)
	}
	found := map[chartObjectRef]map[string]any{}
	for _, key := range order {
		g := groups[key]
		objs, err := live.Get(ctx, kctx, g.resource, g.namespace, g.names)
		if err != nil {
			return nil, err
		}
		for _, o := range objs {
			md, _ := o["metadata"].(map[string]any)
			kind, _ := o["kind"].(string)
			name, _ := md["name"].(string)
			found[chartObjectRef{kind, g.namespace, name}] = o
		}
	}

	var changes []release.ChartChange
	for _, d := range docs {
		if d.kind == "Secret" {
			continue
		}
		fp, err := release.HashDocument(d.body)
		if err != nil {
			return nil, fmt.Errorf("hash %s %s: %w", d.kind, d.name, err)
		}
		change := release.ChartChange{
			Chart: rc.spec.Name, Fingerprint: fp,
			Key: release.ObjectKey{Cluster: kctx, Kind: d.kind, Namespace: d.namespace, Name: d.name},
		}
		obj, exists := found[chartObjectRef{d.kind, d.namespace, d.name}]
		if !exists {
			change.Op = release.ChartAdded
			changes = append(changes, change)
			continue
		}
		if fields := chartChangedFields(d.body, obj); len(fields) > 0 {
			change.Op, change.Fields = release.ChartChanged, fields
			changes = append(changes, change)
		}
	}

	pruned, readable, err := live.PruneCandidates(ctx, kctx, rc)
	if err != nil {
		return nil, fmt.Errorf("the objects it would prune could not be listed: %w", err)
	}
	if readable {
		for _, o := range pruned {
			fp, herr := release.HashDocument(map[string]any{"prune": rc.spec.Name, "object": o.ref().String()})
			if herr != nil {
				return nil, herr
			}
			spec, _ := o.raw["spec"].(map[string]any)
			svcType, _ := spec["type"].(string)
			changes = append(changes, release.ChartChange{
				Chart: rc.spec.Name, Op: release.ChartRemoved, Fingerprint: fp,
				Key:          release.ObjectKey{Cluster: kctx, Kind: o.Kind, Namespace: o.Metadata.Namespace, Name: o.Metadata.Name},
				LoadBalancer: o.Kind == "Service" && svcType == "LoadBalancer",
			})
		}
	}
	sort.SliceStable(changes, func(i, j int) bool { return changes[i].Key.String() < changes[j].Key.String() })
	return changes, nil
}

// chartChangedFields names the fields the chart renders that the live object
// does not carry with the same value. metadata is compared for its labels and
// annotations only; status and apiVersion never.
func chartChangedFields(desired, live map[string]any) []string {
	var out []string
	for k, dv := range desired {
		switch k {
		case "apiVersion", "kind", "status":
			continue
		case "metadata":
			dm, _ := dv.(map[string]any)
			lm, _ := live["metadata"].(map[string]any)
			for _, sub := range []string{"labels", "annotations"} {
				subsetDiff("metadata."+sub, dm[sub], lm[sub], &out)
			}
			continue
		}
		subsetDiff(k, dv, live[k], &out)
	}
	sort.Strings(out)
	return out
}

// subsetDiff appends the path of every place desired has a value live lacks or
// holds differently. A desired value that is empty (null, "", 0, false, {},
// []) matches an absent live one: the apiserver drops those.
func subsetDiff(at string, desired, live any, out *[]string) {
	switch d := desired.(type) {
	case map[string]any:
		l, _ := live.(map[string]any)
		if live != nil && l == nil {
			*out = append(*out, at)
			return
		}
		for k, dv := range d {
			subsetDiff(at+"."+k, dv, l[k], out)
		}
	case []any:
		l, isList := live.([]any)
		if live == nil && len(d) == 0 {
			return
		}
		if !isList {
			*out = append(*out, at)
			return
		}
		dn, dNamed := namedItems(d)
		ln, lNamed := namedItems(l)
		if dNamed && lNamed {
			for _, item := range d {
				name := item.(map[string]any)["name"].(string)
				lv, ok := ln[name]
				if !ok {
					*out = append(*out, at+"["+name+"]")
					continue
				}
				subsetDiff(at+"["+name+"]", dn[name], lv, out)
			}
			return
		}
		if len(d) != len(l) {
			*out = append(*out, at)
			return
		}
		for i := range d {
			subsetDiff(at+"["+strconv.Itoa(i)+"]", d[i], l[i], out)
		}
	default:
		if desired == nil || isZeroScalar(desired) && live == nil {
			return
		}
		if scalarText(desired) != scalarText(live) && !sameQuantity(at, desired, live) {
			*out = append(*out, at)
		}
	}
}

// sameQuantity reports whether two strings at a resource-quantity path name the
// same amount: the apiserver canonicalises `1024Mi` to `1Gi`, which a text
// comparison would report as a change on every deploy. Only paths that hold
// quantities are compared this way, so an env value "1k" is never read as 1000.
func sameQuantity(at string, desired, live any) bool {
	if !strings.Contains(at, ".resources.") && !strings.Contains(at, ".capacity.") &&
		!strings.Contains(at, ".hard.") && !strings.HasSuffix(at, ".sizeLimit") {
		return false
	}
	d, dok := desired.(string)
	l, lok := live.(string)
	if !dok || !lok {
		return false
	}
	dq, err := resource.ParseQuantity(d)
	if err != nil {
		return false
	}
	lq, err := resource.ParseQuantity(l)
	if err != nil {
		return false
	}
	return dq.Cmp(lq) == 0
}

func namedItems(list []any) (map[string]any, bool) {
	if len(list) == 0 {
		return map[string]any{}, true
	}
	out := make(map[string]any, len(list))
	for _, it := range list {
		m, ok := it.(map[string]any)
		if !ok {
			return nil, false
		}
		name, ok := m["name"].(string)
		if !ok || name == "" {
			return nil, false
		}
		if _, dup := out[name]; dup {
			return nil, false
		}
		out[name] = m
	}
	return out, true
}

func isZeroScalar(v any) bool {
	switch t := v.(type) {
	case string:
		return t == ""
	case bool:
		return !t
	case int:
		return t == 0
	case int64:
		return t == 0
	case float64:
		return t == 0
	}
	return false
}

// scalarText compares scalars across the decoders: a rendered YAML integer is
// an int, the same value from the apiserver's JSON a float64.
func scalarText(v any) string {
	switch t := v.(type) {
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}
