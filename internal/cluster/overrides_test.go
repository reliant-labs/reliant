package cluster

import (
	"strings"
	"testing"
)

// deploymentItem is the expanded Deployment an override targets: two
// containers, so a patch naming one by `name` has something to merge INTO
// and something it must not delete.
func deploymentItem(namespace, name, cluster string) map[string]any {
	md := map[string]any{"name": name, "namespace": namespace}
	if cluster != "" {
		md["labels"] = map[string]any{ClusterRoutingLabel: cluster}
	}
	return map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   md,
		"spec": map[string]any{
			"replicas": int64(1),
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{
						map[string]any{"name": "api", "image": "ghcr.io/acme/api:v1"},
						map[string]any{"name": "otel", "image": "otel/collector:1"},
					},
				},
			},
		},
	}
}

func namespaceItem(name string) map[string]any {
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata": map[string]any{
			"name": name,
			"labels": map[string]any{
				"pod-security.kubernetes.io/enforce": "restricted",
			},
		},
	}
}

func applyOne(t *testing.T, items []any, key string, patch map[string]any) ([]any, []AppliedOverride) {
	t.Helper()
	got, applied, err := applyOverrides(items, map[string]map[string]any{key: patch}, nil)
	if err != nil {
		t.Fatalf("applyOverrides(%q): %v", key, err)
	}
	return got, applied
}

// wantRefusal asserts the override is REFUSED and that the message names
// both the key and the fix. "It failed" is not evidence the right rule
// fired — every one of these failures is reachable by a different bug.
func wantRefusal(t *testing.T, items []any, overrides map[string]map[string]any, hosted map[string]bool, substrings ...string) {
	t.Helper()
	_, _, err := applyOverrides(items, overrides, hosted)
	if err == nil {
		t.Fatalf("applyOverrides succeeded; want a refusal naming %v", substrings)
	}
	for _, want := range substrings {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q:\n%v", want, err)
		}
	}
}

// The owner's headline case: one deep field, by key.
func TestApplyOverrides_SetsField(t *testing.T) {
	items := []any{deploymentItem("acme-dev", "api", "")}
	got, applied := applyOne(t, items, "Deployment/api", map[string]any{
		"spec": map[string]any{"replicas": int64(10)},
	})

	dep := got[0].(map[string]any)
	spec := dep["spec"].(map[string]any)
	if spec["replicas"] != int64(10) {
		t.Errorf("replicas = %#v (%T), want int64(10)", spec["replicas"], spec["replicas"])
	}
	if len(applied) != 1 || applied[0].Key != "Deployment/api" || applied[0].Name != "api" {
		t.Errorf("applied = %#v", applied)
	}
	if got, want := applied[0].Object(), "Deployment/acme-dev/api"; got != want {
		t.Errorf("Object() = %q, want %q", got, want)
	}
}

// THE reason built-in kinds take a strategic merge patch: a patch naming one
// container merges into THAT container. Under a plain JSON merge patch this
// same patch replaces the whole list, silently deleting the otel sidecar and
// the api container's image — which is the bug the strategic path exists to
// prevent, and it would pass a test that only checked the patched field.
func TestApplyOverrides_StrategicMergeKeepsSiblingContainers(t *testing.T) {
	items := []any{deploymentItem("acme-dev", "api", "")}
	got, _ := applyOne(t, items, "Deployment/api", map[string]any{
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []any{map[string]any{
				"name":      "api",
				"resources": map[string]any{"limits": map[string]any{"cpu": "500m"}},
			}},
		}}},
	})

	containers := got[0].(map[string]any)["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
	if len(containers) != 2 {
		t.Fatalf("got %d container(s), want 2 (the sidecar must survive a patch that names only `api`): %#v", len(containers), containers)
	}
	byName := map[string]map[string]any{}
	for _, c := range containers {
		m := c.(map[string]any)
		byName[m["name"].(string)] = m
	}
	api, ok := byName["api"]
	if !ok {
		t.Fatalf("no `api` container: %#v", byName)
	}
	if api["image"] != "ghcr.io/acme/api:v1" {
		t.Errorf("api.image = %#v; a merge must not drop the field the patch did not restate", api["image"])
	}
	limits := api["resources"].(map[string]any)["limits"].(map[string]any)
	if limits["cpu"] != "500m" {
		t.Errorf("api.resources.limits.cpu = %#v, want 500m", limits["cpu"])
	}
	if otel, ok := byName["otel"]; !ok || otel["image"] != "otel/collector:1" {
		t.Errorf("otel sidecar lost or altered: %#v", byName["otel"])
	}
}

// The override cp replaces a duplicate Namespace object with.
func TestApplyOverrides_NamespacePSALabel(t *testing.T) {
	items := []any{namespaceItem("control-plane-dev")}
	got, _ := applyOne(t, items, "Namespace/control-plane-dev", map[string]any{
		"metadata": map[string]any{"labels": map[string]any{
			"pod-security.kubernetes.io/enforce": "baseline",
		}},
	})

	labels := got[0].(map[string]any)["metadata"].(map[string]any)["labels"].(map[string]any)
	if labels["pod-security.kubernetes.io/enforce"] != "baseline" {
		t.Errorf("enforce = %#v, want baseline", labels["pod-security.kubernetes.io/enforce"])
	}
}

// A CRD has no scheme entry, so it takes the RFC 7386 path — and `null`
// deletes.
func TestApplyOverrides_JSONMergePatchOnCRDAndNullDeletes(t *testing.T) {
	items := []any{map[string]any{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "ClusterIssuer",
		"metadata":   map[string]any{"name": "letsencrypt"},
		"spec":       map[string]any{"acme": map[string]any{"server": "https://acme-staging.example", "email": "a@example.com"}},
	}}
	got, _ := applyOne(t, items, "ClusterIssuer/letsencrypt", map[string]any{
		"spec": map[string]any{"acme": map[string]any{
			"server": "https://acme-v02.example",
			"email":  nil,
		}},
	})

	acme := got[0].(map[string]any)["spec"].(map[string]any)["acme"].(map[string]any)
	if acme["server"] != "https://acme-v02.example" {
		t.Errorf("server = %#v", acme["server"])
	}
	if _, present := acme["email"]; present {
		t.Errorf("email survived a null patch: %#v", acme)
	}
}

func TestApplyOverrides_RefusesMissingTargetAndNamesCandidates(t *testing.T) {
	items := []any{deploymentItem("acme-dev", "api", ""), deploymentItem("acme-dev", "worker", "")}
	wantRefusal(t, items, map[string]map[string]any{
		"Deployment/ap": {"spec": map[string]any{"replicas": int64(2)}},
	}, nil, "Deployment/ap", "matches no rendered object", "Deployment/acme-dev/api")
}

func TestApplyOverrides_RefusesAmbiguousKey(t *testing.T) {
	items := []any{deploymentItem("acme-dev", "api", ""), deploymentItem("acme-stage", "api", "")}
	wantRefusal(t, items, map[string]map[string]any{
		"Deployment/api": {"spec": map[string]any{"replicas": int64(2)}},
	}, nil, "matches 2 objects", "acme-dev", "acme-stage", "add the qualifier")
}

func TestApplyOverrides_RefusesIdentityChange(t *testing.T) {
	items := []any{deploymentItem("acme-dev", "api", "")}
	for field, patch := range map[string]map[string]any{
		"metadata.name":      {"metadata": map[string]any{"name": "other"}},
		"metadata.namespace": {"metadata": map[string]any{"namespace": "other"}},
		"kind":               {"kind": "StatefulSet"},
		"apiVersion":         {"apiVersion": "apps/v1beta1"},
	} {
		t.Run(field, func(t *testing.T) {
			wantRefusal(t, items, map[string]map[string]any{"Deployment/api": patch}, nil,
				"Deployment/api", field, "never its identity")
		})
	}
}

func TestApplyOverrides_RefusesTwoKeysOnOneObject(t *testing.T) {
	items := []any{deploymentItem("acme-dev", "api", "")}
	wantRefusal(t, items, map[string]map[string]any{
		"Deployment/api":          {"spec": map[string]any{"replicas": int64(2)}},
		"Deployment/acme-dev/api": {"spec": map[string]any{"replicas": int64(3)}},
	}, nil, "both resolve to", "merge the two patches")
}

// A hosted workload renders no object in this env — the control plane
// renders it — so the refusal must SAY that rather than report the generic
// "no such object" a reader would take as a forge bug.
func TestApplyOverrides_RefusesHostedTarget(t *testing.T) {
	items := []any{namespaceItem("acme-dev")}
	wantRefusal(t, items, map[string]map[string]any{
		"Deployment/api": {"spec": map[string]any{"replicas": int64(2)}},
	}, map[string]bool{"api": true}, "HOSTED runtime", "control plane renders")
}

func TestApplyOverrides_RefusesMalformedKey(t *testing.T) {
	items := []any{deploymentItem("acme-dev", "api", "")}
	wantRefusal(t, items, map[string]map[string]any{
		"api": {"spec": map[string]any{"replicas": int64(2)}},
	}, nil, "is not an override key", "Kind/name")
}

// A cluster-qualified key selects ONE of two same-named objects that a
// multi-cluster env renders, and leaves the other alone.
func TestApplyOverrides_ClusterQualifierSelectsOne(t *testing.T) {
	items := []any{
		deploymentItem("acme-dev", "api", "k3d-a"),
		deploymentItem("acme-dev", "api", "k3d-b"),
	}
	got, applied := applyOne(t, items, "Deployment/api@k3d-b", map[string]any{
		"spec": map[string]any{"replicas": int64(7)},
	})

	if r := got[0].(map[string]any)["spec"].(map[string]any)["replicas"]; r != int64(1) {
		t.Errorf("k3d-a replicas = %#v, want the unpatched 1", r)
	}
	if r := got[1].(map[string]any)["spec"].(map[string]any)["replicas"]; r != int64(7) {
		t.Errorf("k3d-b replicas = %#v, want 7", r)
	}
	if applied[0].Object() != "Deployment/acme-dev/api@k3d-b" {
		t.Errorf("Object() = %q", applied[0].Object())
	}
}

// Same inputs, byte-identical output: the summary is key-sorted, so it does
// not inherit the render map's iteration order.
func TestApplyOverrides_Deterministic(t *testing.T) {
	overrides := map[string]map[string]any{
		"Deployment/worker":  {"spec": map[string]any{"replicas": int64(3)}},
		"Deployment/api":     {"spec": map[string]any{"replicas": int64(2)}},
		"Namespace/acme-dev": {"metadata": map[string]any{"labels": map[string]any{"x": "y"}}},
	}
	var first []string
	for range 8 {
		items := []any{
			deploymentItem("acme-dev", "api", ""),
			deploymentItem("acme-dev", "worker", ""),
			namespaceItem("acme-dev"),
		}
		_, applied, err := applyOverrides(items, overrides, nil)
		if err != nil {
			t.Fatalf("applyOverrides: %v", err)
		}
		var keys []string
		for _, a := range applied {
			keys = append(keys, a.Key+"→"+a.Object())
		}
		if first == nil {
			first = keys
			continue
		}
		if strings.Join(keys, ",") != strings.Join(first, ",") {
			t.Fatalf("summary order varies between runs:\n%v\n%v", first, keys)
		}
	}
	if len(first) != 3 {
		t.Errorf("applied %d override(s), want 3: %v", len(first), first)
	}
}

// No overrides is the shape every project that declares none renders, and it
// must not disturb the stream.
func TestApplyOverrides_NoneIsIdentity(t *testing.T) {
	items := []any{deploymentItem("acme-dev", "api", "")}
	got, applied, err := applyOverrides(items, nil, nil)
	if err != nil {
		t.Fatalf("applyOverrides: %v", err)
	}
	if len(applied) != 0 {
		t.Errorf("applied = %#v, want none", applied)
	}
	if r := got[0].(map[string]any)["spec"].(map[string]any)["replicas"]; r != int64(1) {
		t.Errorf("replicas = %#v, want the unpatched 1", r)
	}
}

func TestParseOverrideKey(t *testing.T) {
	for _, tc := range []struct {
		raw                            string
		kind, namespace, name, cluster string
	}{
		{"Deployment/api", "Deployment", "", "api", ""},
		{"Deployment/acme-dev/api", "Deployment", "acme-dev", "api", ""},
		{"Deployment/api@k3d-cp", "Deployment", "", "api", "k3d-cp"},
		{"Deployment/acme-dev/api@k3d-cp", "Deployment", "acme-dev", "api", "k3d-cp"},
		{"ClusterIssuer/letsencrypt", "ClusterIssuer", "", "letsencrypt", ""},
		// A cloud kubectl context carries `_` and `.`.
		{"Service/api@gke_reliant-labs_us-central1_prod", "Service", "", "api", "gke_reliant-labs_us-central1_prod"},
	} {
		got, err := parseOverrideKey(tc.raw)
		if err != nil {
			t.Errorf("parseOverrideKey(%q): %v", tc.raw, err)
			continue
		}
		if got.kind != tc.kind || got.namespace != tc.namespace || got.name != tc.name || got.cluster != tc.cluster {
			t.Errorf("parseOverrideKey(%q) = %+v, want kind=%q ns=%q name=%q cluster=%q",
				tc.raw, got, tc.kind, tc.namespace, tc.name, tc.cluster)
		}
	}
	for _, bad := range []string{"api", "deployment/api", "Deployment", "Deployment/", "/api", "Deployment/a/b/c", "Deployment/API"} {
		if _, err := parseOverrideKey(bad); err == nil {
			t.Errorf("parseOverrideKey(%q) succeeded; want a refusal", bad)
		}
	}
}

// applyOverrides returns a NEW slice: the caller's stream is not patched
// behind its back. Pinned because the in-place version passed every other
// test here — the caller reassigns the same backing array — which made the
// return value look load-bearing when it was not.
func TestApplyOverrides_DoesNotMutateInput(t *testing.T) {
	items := []any{deploymentItem("acme-dev", "api", "")}
	if _, _, err := applyOverrides(items, map[string]map[string]any{
		"Deployment/api": {"spec": map[string]any{"replicas": int64(10)}},
	}, nil); err != nil {
		t.Fatalf("applyOverrides: %v", err)
	}
	if r := items[0].(map[string]any)["spec"].(map[string]any)["replicas"]; r != int64(1) {
		t.Errorf("input slice was patched in place (replicas = %#v, want the original 1)", r)
	}
}
