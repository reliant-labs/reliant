package cli

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/fluxdelivery"
)

const deliveryNS = "forge-delivery-acme-prod"

func deliveryEntity(scope string) FluxDeliveryEntity {
	return FluxDeliveryEntity{
		Chart: fluxdelivery.ChartName, Env: "prod", Cluster: testCtx,
		Namespace: deliveryNS, ResourceSet: "delivery.prod",
		ApplyIdentities: true, DeliveryScope: scope,
	}
}

// deliveryStream is a release across all three phases on the env's one
// cluster: pre (a Namespace, a CRD and its instance, RBAC, config, a migrate
// Job), rollout (a Deployment), post (a post-rollout Job). The Secret and the
// object routed to another cluster must widen nothing.
func deliveryStream(extra ...string) string {
	return scopeStream(append([]string{
		onCtx(docCRD), onCtx(docWidget), onCtx(docClusterRole),
		onCtx("apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRoleBinding\nmetadata:\n  name: acme-reader\n" +
			"roleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: ClusterRole\n  name: acme-reader\nsubjects: []\n"),
		onCtx("apiVersion: rbac.authorization.k8s.io/v1\nkind: Role\nmetadata:\n  name: api\n  namespace: acme-prod\nrules: []\n"),
		onCtx("apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: migrate-abc\n  namespace: acme-prod\n"),
		onCtx("apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: seed-def\n  namespace: acme-prod\n  annotations:\n" +
			"    forge.dev/deploy-phase: post-rollout\n"),
	}, extra...)...)
}

func mustGrants(t *testing.T, stream, scope string) (fluxDeliveryGrant, fluxDeliveryGrant, []string) {
	t.Helper()
	pre, apply, ns, err := fluxDeliveryGrants(stream, deliveryEntity(scope))
	if err != nil {
		t.Fatalf("fluxDeliveryGrants: %v", err)
	}
	return pre, apply, ns
}

// rule finds the rule for one resource (and resourceNames), or nil.
func rule(rules []ruleSpec, group, resource string, names ...string) *ruleSpec {
	for i, r := range rules {
		if r.Ref == (resourceRef{group, resource}) && reflect.DeepEqual(r.Names, nilIfEmpty(names)) {
			return &rules[i]
		}
	}
	return nil
}

func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

func refsOf(rules []ruleSpec) []string {
	var out []string
	for _, r := range rules {
		s := r.Ref.String()
		if len(r.Names) > 0 {
			s += "[" + strings.Join(r.Names, ",") + "]"
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// TestFluxDeliveryGrantsSplitByPhase is the #441 fix: the pre identity holds
// the cluster-scoped kinds the release carries (so its Namespaces and CRDs
// apply), the apply identity holds the workloads, and neither holds a kind
// the render does not contain.
func TestFluxDeliveryGrantsSplitByPhase(t *testing.T) {
	t.Parallel()
	pre, apply, namespaces := mustGrants(t, deliveryStream(), "namespace")

	if pre.ServiceAccount != fluxdelivery.PreServiceAccount || apply.ServiceAccount != fluxdelivery.ApplyServiceAccount {
		t.Fatalf("identities %q / %q", pre.ServiceAccount, apply.ServiceAccount)
	}
	if got, want := refsOf(pre.ClusterRules), []string{
		"apiextensions.k8s.io/customresourcedefinitions",
		"core/namespaces",
		"rbac.authorization.k8s.io/clusterrolebindings",
		"rbac.authorization.k8s.io/clusterroles",
		"rbac.authorization.k8s.io/clusterroles[acme-reader]",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("pre ClusterRole resources = %v, want %v", got, want)
	}
	if r := rule(pre.ClusterRules, "", "namespaces"); r == nil || hasString(r.Verbs, "delete") || !hasString(r.Verbs, "create") {
		t.Errorf("pre namespaces rule = %+v: create, never delete (a pruned Namespace takes every workload in it)", r)
	}
	if r := rule(pre.ClusterRules, "rbac.authorization.k8s.io", "clusterroles", "acme-reader"); r == nil || !reflect.DeepEqual(r.Verbs, []string{"bind", "escalate"}) {
		t.Errorf("clusterrole pins = %+v, want bind+escalate pinned to acme-reader", r)
	}
	if got, want := refsOf(pre.NamespaceRules["acme-prod"]), []string{
		"acme.dev/widgets", "batch/jobs", "core/configmaps", "core/services",
		"rbac.authorization.k8s.io/roles", "rbac.authorization.k8s.io/roles[api]",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("pre Role in acme-prod = %v, want %v", got, want)
	}
	if len(apply.ClusterRules) != 0 {
		t.Errorf("the apply identity holds cluster rules %v; rollout and post carry no cluster-scoped kind", refsOf(apply.ClusterRules))
	}
	if got, want := refsOf(apply.NamespaceRules["acme-prod"]), []string{"apps/deployments", "apps/replicasets", "batch/jobs", "core/pods"}; !reflect.DeepEqual(got, want) {
		t.Errorf("apply Role in acme-prod = %v, want %v (the workloads, the post Job, and the health check's reads)", got, want)
	}
	if r := rule(apply.NamespaceRules["acme-prod"], "", "pods"); r == nil || !reflect.DeepEqual(r.Verbs, readVerbs) {
		t.Errorf("pods = %+v, want read only", r)
	}
	for _, g := range []fluxDeliveryGrant{pre, apply} {
		if _, ok := g.NamespaceRules["not-ours"]; ok {
			t.Errorf("%s holds a grant on another cluster's namespace", g.ServiceAccount)
		}
		for _, rules := range g.NamespaceRules {
			if rule(rules, "", "secrets") != nil {
				t.Errorf("%s holds a Secret rule; Secrets never ride a bundle", g.ServiceAccount)
			}
		}
	}
	if !reflect.DeepEqual(namespaces, []string{"acme-prod"}) {
		t.Errorf("pre-created namespaces = %v, want the one the render declares", namespaces)
	}
}

// TestFluxDeliveryGrantsClusterScope: one ClusterRole per identity, no Role
// and so no Namespace to pre-create.
func TestFluxDeliveryGrantsClusterScope(t *testing.T) {
	t.Parallel()
	pre, apply, namespaces := mustGrants(t, deliveryStream(), "cluster")
	if len(pre.NamespaceRules)+len(apply.NamespaceRules) != 0 || len(namespaces) != 0 {
		t.Fatalf("cluster scope rendered Roles %v / %v and namespaces %v", pre.NamespaceRules, apply.NamespaceRules, namespaces)
	}
	if rule(pre.ClusterRules, "", "configmaps") == nil || rule(pre.ClusterRules, "apiextensions.k8s.io", "customresourcedefinitions") == nil {
		t.Errorf("pre ClusterRole = %v, want the cluster-scoped and namespaced pre kinds", refsOf(pre.ClusterRules))
	}
	if rule(pre.ClusterRules, "rbac.authorization.k8s.io", "roles", "api") == nil {
		t.Errorf("pre ClusterRole has no escalate pin on the Role it creates: %v", refsOf(pre.ClusterRules))
	}
	if rule(apply.ClusterRules, "apps", "deployments") == nil || rule(apply.ClusterRules, "", "pods") == nil {
		t.Errorf("apply ClusterRole = %v", refsOf(apply.ClusterRules))
	}
	if rule(apply.ClusterRules, "apiextensions.k8s.io", "customresourcedefinitions") != nil {
		t.Errorf("apply ClusterRole holds a pre kind: %v", refsOf(apply.ClusterRules))
	}
}

// TestFluxDeliveryIdentityObjects: the grants as the objects that ride the
// bootstrap chart.
func TestFluxDeliveryIdentityObjects(t *testing.T) {
	t.Parallel()
	objs, err := fluxDeliveryIdentityObjects(deliveryStream(), deliveryEntity("namespace"))
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	for _, o := range objs {
		refs = append(refs, o.ref())
	}
	want := []string{
		"ClusterRole " + deliveryNS + "-pre",
		"ClusterRoleBinding " + deliveryNS + "-pre",
		"ConfigMap " + deliveryNS + "/" + fluxdelivery.GrantsRecord,
		"Namespace acme-prod",
		"Role acme-prod/" + deliveryNS + "-apply",
		"Role acme-prod/" + deliveryNS + "-pre",
		"RoleBinding acme-prod/" + deliveryNS + "-apply",
		"RoleBinding acme-prod/" + deliveryNS + "-pre",
	}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("objects = %v, want %v", refs, want)
	}
	for _, o := range objs {
		md := o.Body["metadata"].(map[string]any)
		if md["labels"].(map[string]any)[fluxdelivery.EnvLabel] != "prod" {
			t.Errorf("%s lacks the delivery env label", o.ref())
		}
		switch o.Kind {
		case "Namespace":
			if md["annotations"].(map[string]any)[cluster.PruneAnnotation] != cluster.PruneDisabled {
				t.Errorf("the pre-created Namespace is not prune-disabled: the chart's prune could delete it")
			}
		case "RoleBinding", "ClusterRoleBinding":
			subject := o.Body["subjects"].([]any)[0].(map[string]any)
			wantSA := fluxdelivery.ApplyServiceAccount
			if strings.HasSuffix(o.Name, "-pre") {
				wantSA = fluxdelivery.PreServiceAccount
			}
			if subject["name"] != wantSA || subject["namespace"] != deliveryNS {
				t.Errorf("%s binds %v, want %s/%s", o.ref(), subject, deliveryNS, wantSA)
			}
		}
	}

	stream, err := fluxDeliveryIdentityManifests(deliveryStream(), deliveryEntity("namespace"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(stream, "\n---\n") + 1; n != len(want) {
		t.Errorf("manifest stream holds %d documents, want %d", n, len(want))
	}
	for _, doc := range strings.Split(stream, "\n---\n") {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(doc), &m); err != nil || m["kind"] == nil {
			t.Errorf("document does not parse as an object (%v):\n%s", err, doc)
		}
	}
}

// TestFluxDeliveryGrantsRefuse: a render the grant cannot be derived from.
func TestFluxDeliveryGrantsRefuse(t *testing.T) {
	t.Parallel()
	bad := deliveryStream(onCtx("apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: typo\n  namespace: acme-prod\n  annotations:\n" +
		"    forge.dev/deploy-phase: pre-rolout\n"))
	if _, _, _, err := fluxDeliveryGrants(bad, deliveryEntity("namespace")); err == nil || !strings.Contains(err.Error(), "pre-rolout") {
		t.Errorf("a typo'd deploy phase: err = %v, want it named", err)
	}
	fd := deliveryEntity("namespace")
	fd.Cluster = "nowhere"
	if _, _, _, err := fluxDeliveryGrants(deliveryStream(), fd); err == nil || !strings.Contains(err.Error(), "puts nothing on cluster") {
		t.Errorf("an unbound cluster: err = %v", err)
	}
}

// TestDeriveGrantNeedsSharedByConnect: connect's derivation still picks by
// hub unit through the shared helper.
func TestDeriveGrantNeedsSharedByConnect(t *testing.T) {
	t.Parallel()
	unit := onCtx("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: other\n  namespace: acme-prod\n  labels:\n    forge.dev/delivery-unit: other\n")
	n, err := deriveGrantNeeds(scopeStream(unit), testCtx, []string{"other"})
	if err != nil {
		t.Fatal(err)
	}
	if n.Objects != 1 || !n.Namespaced["acme-prod"][resourceRef{"", "configmaps"}] {
		t.Errorf("hub unit `other` needs = %+v, want its one ConfigMap", n)
	}
}
