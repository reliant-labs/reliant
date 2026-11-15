package cluster

// Typed overrides of forge's own rendered objects (Bundle.overrides).
//
// WHY THIS IS IN GO, NOT KCL. The objects most worth overriding — Deployment,
// Service, ServiceAccount — do not exist in the KCL render. `output.manifests`
// carries forge.dev/v1alpha1 Workload RECORDS, and expandTierDeclarations
// expands each into the object set pkg/deploy renders. So a patch applied in
// KCL could only reach the Namespace and the raw `forge.Manifests` groups,
// which is the half an author is least likely to need. The patch therefore
// applies HERE, immediately after expansion, where every object a deploy will
// apply is addressable and nothing downstream has consumed the stream yet.
//
// ONE HOOK COVERS FOUR COMMANDS. `forge env render`, `forge env deploy`,
// `forge env shape` and the bundle all reach the stream through
// ExtractManifests, so patching inside it is what makes the shape's object
// hashes and the bundle's objects post-override by construction, rather than
// by four call sites each remembering to ask.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/kubernetes/scheme"
)

// AppliedOverride is one override that landed, for the "overrides applied"
// summary `forge env render` prints and its --json carries. It records the
// KEY the author wrote next to the object it actually resolved to, because
// the whole class of mistake here is a key that matches something other
// than what its author pictured.
type AppliedOverride struct {
	Key       string `json:"key"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Cluster   string `json:"cluster,omitempty"`
}

// Object renders the resolved target the way the summary prints it.
func (a AppliedOverride) Object() string {
	s := a.Kind + "/"
	if a.Namespace != "" {
		s += a.Namespace + "/"
	}
	s += a.Name
	if a.Cluster != "" {
		s += "@" + a.Cluster
	}
	return s
}

// overrideKey is a parsed Bundle.overrides key. The namespace and the cluster
// are QUALIFIERS: absent, they match anything, which is what lets a
// single-namespace env write `Deployment/api` and a multi-cluster one add
// only the qualifier it needs.
type overrideKey struct {
	raw       string
	kind      string
	namespace string
	name      string
	cluster   string
}

// The same grammar kcl/schema.k's _OVERRIDE_KEY_RE checks at load. It is
// re-checked here because ExtractManifests is reached by callers that did not
// come through this project's KCL (internal/doctor replays a stored render
// contract), and a malformed key must fail the same way whatever path it
// arrived on.
var overrideKeyRE = regexp.MustCompile(`^([A-Z][A-Za-z0-9]*)/([a-z0-9]([-a-z0-9.]*[a-z0-9])?)(?:/([a-z0-9]([-a-z0-9.]*[a-z0-9])?))?(?:@([A-Za-z0-9][-A-Za-z0-9_.:]*))?$`)

func parseOverrideKey(raw string) (overrideKey, error) {
	m := overrideKeyRE.FindStringSubmatch(raw)
	if m == nil {
		return overrideKey{}, fmt.Errorf("override key %q is not an override key: the grammar is `Kind/name`, `Kind/namespace/name`, either with an optional `@<cluster>` — e.g. \"Deployment/api\", \"Deployment/acme-dev/api\", \"Deployment/api@k3d-cp\"", raw)
	}
	k := overrideKey{raw: raw, kind: m[1], name: m[2], cluster: m[6]}
	// Three segments means the MIDDLE one is the namespace: `Kind/ns/name`.
	// Two means the one name, unqualified.
	if m[4] != "" {
		k.namespace, k.name = m[2], m[4]
	}
	return k, nil
}

// decodeOverrides reads `output.overrides` off a render. Absent or null is no
// overrides — the shape every project that declares none renders.
func decodeOverrides(raw any) (map[string]map[string]any, error) {
	if raw == nil {
		return nil, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("output.overrides: %w", err)
	}
	var out map[string]map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("output.overrides: every value must be a patch map, e.g. \"Deployment/api\" = {spec.replicas = 10}: %w", err)
	}
	return out, nil
}

// decodeHostedNames reads the names of this env's HOSTED workloads off
// `output.workloads`. They render no Kubernetes object in this env — the
// control plane renders them — so an override naming one must say that,
// rather than reporting the generic "no such object" a reader would
// reasonably take as a forge bug.
func decodeHostedNames(raw any) map[string]bool {
	if raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var ws []struct {
		Name    string `json:"name"`
		Runtime struct {
			Type string `json:"type"`
		} `json:"runtime"`
	}
	if err := json.Unmarshal(b, &ws); err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, w := range ws {
		if w.Runtime.Type == "hosted" && w.Name != "" {
			out[w.Name] = true
		}
	}
	return out
}

// objectIdentity is how one rendered object is addressed by a key.
type objectIdentity struct {
	kind      string
	namespace string
	name      string
	cluster   string
}

func (o objectIdentity) String() string {
	s := o.kind + "/"
	if o.namespace != "" {
		s += o.namespace + "/"
	}
	s += o.name
	if o.cluster != "" {
		s += "@" + o.cluster
	}
	return s
}

func identityOfItem(m map[string]any) (objectIdentity, bool) {
	kind, _ := m["kind"].(string)
	md, _ := m["metadata"].(map[string]any)
	name, _ := md["name"].(string)
	if kind == "" || name == "" {
		return objectIdentity{}, false
	}
	ns, _ := md["namespace"].(string)
	var cluster string
	if labels, ok := md["labels"].(map[string]any); ok {
		cluster, _ = labels[ClusterRoutingLabel].(string)
	}
	return objectIdentity{kind: kind, namespace: ns, name: name, cluster: cluster}, true
}

func (k overrideKey) matches(id objectIdentity) bool {
	if k.kind != id.kind || k.name != id.name {
		return false
	}
	if k.namespace != "" && k.namespace != id.namespace {
		return false
	}
	if k.cluster != "" && k.cluster != id.cluster {
		return false
	}
	return true
}

// Fields an override may never change. A patch that renamed an object would
// not override forge's object — it would ADD a second one and leave the
// original in the stream, so the author's env silently grows an object. Same
// for the GVK: the patch would be merged against the wrong schema.
var overrideImmutable = []struct {
	path  []string
	label string
}{
	{[]string{"apiVersion"}, "apiVersion"},
	{[]string{"kind"}, "kind"},
	{[]string{"metadata", "name"}, "metadata.name"},
	{[]string{"metadata", "namespace"}, "metadata.namespace"},
}

func patchTouchesImmutable(patch map[string]any) string {
	for _, im := range overrideImmutable {
		cur := patch
		for i, seg := range im.path {
			if i == len(im.path)-1 {
				if _, ok := cur[seg]; ok {
					return im.label
				}
				break
			}
			next, ok := cur[seg].(map[string]any)
			if !ok {
				break
			}
			cur = next
		}
	}
	return ""
}

// applyOverrides resolves every override key against the EXPANDED stream and
// merges its patch, returning a NEW slice. Order is by key, sorted, so the
// same inputs give byte-identical output however the render's map iterated.
//
// The input slice is left alone deliberately. An in-place version passes the
// same tests — the caller reassigns the same backing array — which makes the
// return value look load-bearing when it is not, and a caller that kept the
// original around to compare would silently be holding the patched objects.
func applyOverrides(items []any, overrides map[string]map[string]any, hosted map[string]bool) ([]any, []AppliedOverride, error) {
	if len(overrides) == 0 {
		return items, nil, nil
	}
	items = append([]any(nil), items...)
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Every addressable object in the stream, by index.
	ids := map[int]objectIdentity{}
	for i, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if id, ok := identityOfItem(m); ok {
			ids[i] = id
		}
	}

	claimed := map[int]string{}
	var applied []AppliedOverride
	for _, raw := range keys {
		key, err := parseOverrideKey(raw)
		if err != nil {
			return nil, nil, err
		}
		patch := overrides[raw]
		if field := patchTouchesImmutable(patch); field != "" {
			return nil, nil, fmt.Errorf("override %q sets %s: an override changes FIELDS of the object forge rendered, never its identity — a renamed object would be a second object alongside forge's. Remove %s from the patch; to target a different object, change the override key", raw, field, field)
		}

		var hits []int
		for i, id := range ids {
			if key.matches(id) {
				hits = append(hits, i)
			}
		}
		sort.Ints(hits)

		switch {
		case len(hits) == 0:
			if hosted[key.name] {
				return nil, nil, fmt.Errorf("override %q targets %q, which runs on the HOSTED runtime: the control plane renders its objects, not this env, so there is nothing here to patch. Set the field on the workload declaration itself, or bind it to forge.OnCluster to render — and override — it locally", raw, key.name)
			}
			return nil, nil, fmt.Errorf("override %q matches no rendered object. %s", raw, candidateHint(ids, key))
		case len(hits) > 1:
			return nil, nil, fmt.Errorf("override %q matches %d objects (%s): add the qualifier that tells them apart — the namespace (`%s/<namespace>/%s`) or the cluster (`%s@<cluster>`)",
				raw, len(hits), describeIdentities(ids, hits), key.kind, key.name, raw)
		}
		i := hits[0]
		if other, dup := claimed[i]; dup {
			return nil, nil, fmt.Errorf("overrides %q and %q both resolve to %s: one object takes one patch, so merge the two patches into a single override", other, raw, ids[i])
		}
		claimed[i] = raw

		m, _ := items[i].(map[string]any)
		patched, err := mergeOverride(m, patch)
		if err != nil {
			return nil, nil, fmt.Errorf("override %q on %s: %w", raw, ids[i], err)
		}
		items[i] = patched
		id := ids[i]
		applied = append(applied, AppliedOverride{Key: raw, Kind: id.kind, Name: id.name, Namespace: id.namespace, Cluster: id.cluster})
	}
	sort.Slice(applied, func(a, b int) bool { return applied[a].Key < applied[b].Key })
	return items, applied, nil
}

// candidateHint names the objects an author most plausibly meant. Same kind
// first — a typo'd name is the common case and the candidate list is then
// short and exactly right; failing that, same name under a different kind.
func candidateHint(ids map[int]objectIdentity, key overrideKey) string {
	var sameKind, sameName []string
	for _, id := range ids {
		if id.kind == key.kind {
			sameKind = append(sameKind, id.String())
		} else if id.name == key.name {
			sameName = append(sameName, id.String())
		}
	}
	sort.Strings(sameKind)
	sort.Strings(sameName)
	switch {
	case len(sameKind) > 0:
		return fmt.Sprintf("This env renders these %s objects: %s", key.kind, strings.Join(cap5(sameKind), ", "))
	case len(sameName) > 0:
		return fmt.Sprintf("Nothing of kind %s is rendered; %q is: %s", key.kind, key.name, strings.Join(cap5(sameName), ", "))
	}
	return fmt.Sprintf("This env renders no %s at all. Run `forge env render <env> --list` to see what it does render", key.kind)
}

func describeIdentities(ids map[int]objectIdentity, hits []int) string {
	out := make([]string, 0, len(hits))
	for _, i := range hits {
		out = append(out, ids[i].String())
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

func cap5(in []string) []string {
	if len(in) <= 5 {
		return in
	}
	return append(in[:5:5], fmt.Sprintf("… and %d more", len(in)-5))
}

// mergeOverride merges one patch into one object.
//
// STRATEGIC merge patch for a built-in kind, so a patch naming one container
// by `name` lands ON that container: a plain JSON merge patch replaces the
// whole `containers` list, which would silently delete the sidecars and the
// env of every container the patch did not restate. RFC 7386 JSON merge patch
// for a CRD or an unknown kind, where there is no merge-key metadata to drive
// the strategic variant. `null` deletes a field under both.
func mergeOverride(obj map[string]any, patch map[string]any) (map[string]any, error) {
	objJSON, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("marshal object: %w", err)
	}
	patchJSON, err := json.Marshal(patch)
	if err != nil {
		return nil, fmt.Errorf("marshal patch: %w", err)
	}

	var mergedJSON []byte
	apiVersion, _ := obj["apiVersion"].(string)
	kind, _ := obj["kind"].(string)
	if typed, nerr := scheme.Scheme.New(schema.FromAPIVersionAndKind(apiVersion, kind)); nerr == nil {
		mergedJSON, err = strategicpatch.StrategicMergePatch(objJSON, patchJSON, typed)
		if err != nil {
			return nil, fmt.Errorf("strategic merge patch: %w", err)
		}
	} else if mergedJSON, err = jsonpatch.MergePatch(objJSON, patchJSON); err != nil {
		return nil, fmt.Errorf("json merge patch: %w", err)
	}

	var merged map[string]any
	if err := json.Unmarshal(mergedJSON, &merged); err != nil {
		return nil, fmt.Errorf("decode patched object: %w", err)
	}
	// encoding/json decodes every number as float64, which would render
	// `replicas: 10` as `10` but `cpu: 1` and any other integral field through
	// a float round-trip. Narrow the integral ones back so the stream stays
	// byte-identical to an unpatched render of the same value.
	return normalizeNumbers(merged).(map[string]any), nil
}

func normalizeNumbers(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, inner := range t {
			t[k] = normalizeNumbers(inner)
		}
		return t
	case []any:
		for i, inner := range t {
			t[i] = normalizeNumbers(inner)
		}
		return t
	case float64:
		if t == float64(int64(t)) {
			return int64(t)
		}
		return t
	}
	return v
}
