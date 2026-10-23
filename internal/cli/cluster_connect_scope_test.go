package cli

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

const testCtx = "gke_acme_us-central1_prod"

// scopeStream is a render of one env onto one cluster: a namespaced app, a
// CRD and its custom resource, a Secret (which never rides a bundle), a
// ClusterRole and a Namespace (cluster-scoped), and one object routed to a
// DIFFERENT cluster, which must not widen this one's grant.
func scopeStream(extra ...string) string {
	docs := []string{
		"# cluster: " + testCtx + "\napiVersion: v1\nkind: Namespace\nmetadata:\n  name: acme-prod\n",
		"# cluster: " + testCtx + "\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api\n  namespace: acme-prod\n",
		"# cluster: " + testCtx + "\napiVersion: v1\nkind: Service\nmetadata:\n  name: api\n  namespace: acme-prod\n",
		"# cluster: " + testCtx + "\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n  namespace: acme-prod\n",
		"# cluster: " + testCtx + "\napiVersion: v1\nkind: Secret\nmetadata:\n  name: db\n  namespace: acme-prod\ndata:\n  url: \"forge.dev/redacted: sha256:abc\"\n",
		"# cluster: other-ctx\napiVersion: batch/v1\nkind: CronJob\nmetadata:\n  name: elsewhere\n  namespace: not-ours\n",
	}
	docs = append(docs, extra...)
	return strings.Join(docs, "---\n")
}

func onCtx(doc string) string { return "# cluster: " + testCtx + "\n" + doc }

const (
	docClusterRole = "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: acme-reader\nrules: []\n"
	docRoleBinding = "apiVersion: rbac.authorization.k8s.io/v1\nkind: RoleBinding\nmetadata:\n  name: acme-reader\n  namespace: acme-prod\n" +
		"roleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: ClusterRole\n  name: acme-reader\nsubjects: []\n"
	docCRD = "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: widgets.acme.dev\n" +
		"spec:\n  group: acme.dev\n  scope: Namespaced\n  names:\n    kind: Widget\n    plural: widgets\n"
	docWidget = "apiVersion: acme.dev/v1\nkind: Widget\nmetadata:\n  name: w\n  namespace: acme-prod\n"
)

func mustNeeds(t *testing.T, stream string) grantNeeds {
	t.Helper()
	n, err := deriveGrantNeeds(stream, testCtx)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestParseApplyScope(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want applyScope
		err  string
	}{
		{"", applyScope{}, ""},
		{"cluster", applyScope{Cluster: true}, ""},
		{"namespace:b,a, b", applyScope{Namespaces: []string{"a", "b"}}, ""},
		{"namespace:", applyScope{}, "not a namespace name"},
		{"namespace:Bad_Name", applyScope{}, "not a namespace name"},
		{"clusterwide", applyScope{}, "is not namespace:"},
		{"namespace", applyScope{}, "is not namespace:"},
	} {
		got, err := parseApplyScope(tc.in)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("parseApplyScope(%q) err = %v, want %q", tc.in, err, tc.err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseApplyScope(%q) = %+v, %v; want %+v", tc.in, got, err, tc.want)
		}
	}
}

func TestDeriveGrantNeeds_ReadsOnlyThisClustersNonSecretObjects(t *testing.T) {
	n := mustNeeds(t, scopeStream(onCtx(docCRD), onCtx(docWidget), onCtx(docClusterRole)))
	ns := n.Namespaced["acme-prod"]
	for _, want := range []resourceRef{
		{"apps", "deployments"}, {"", "services"}, {"", "configmaps"}, {"acme.dev", "widgets"},
	} {
		if !ns[want] {
			t.Errorf("acme-prod needs %s; got %v", want, ns)
		}
	}
	if ns[resourceRef{"", "secrets"}] {
		t.Error("a Secret never rides a bundle and must not be granted")
	}
	if _, ok := n.Namespaced["not-ours"]; ok {
		t.Error("an object routed to another cluster widened this cluster's grant")
	}
	if !n.NamespaceObjects["acme-prod"] {
		t.Error("the Namespace object is not tracked")
	}
	for _, want := range []resourceRef{{"rbac.authorization.k8s.io", "clusterroles"}, {"apiextensions.k8s.io", "customresourcedefinitions"}} {
		if _, ok := n.Cluster[want]; !ok {
			t.Errorf("cluster-scoped %s not found in %v", want, n.Cluster)
		}
	}
}

func TestDeriveGrantNeeds_ClusterScopedCRDKindIsClusterScoped(t *testing.T) {
	crd := strings.Replace(docCRD, "scope: Namespaced", "scope: Cluster", 1)
	widget := "apiVersion: acme.dev/v1\nkind: Widget\nmetadata:\n  name: w\n"
	n := mustNeeds(t, scopeStream(onCtx(crd), onCtx(widget)))
	if _, ok := n.Cluster[resourceRef{"acme.dev", "widgets"}]; !ok {
		t.Errorf("a Cluster-scoped CRD's kind was treated as namespaced: %+v", n)
	}
}

func TestPlanGrant_NamespaceDefaultCoversTheRenderedNamespaces(t *testing.T) {
	n := mustNeeds(t, scopeStream(onCtx(docRoleBinding)))
	plan, err := planGrant(n, applyScope{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Namespaces, []string{"acme-prod"}) || plan.Cluster {
		t.Fatalf("plan = %+v", plan)
	}
	if !reflect.DeepEqual(plan.PinnedNamespaces, []string{"acme-prod"}) {
		t.Errorf("the rendered Namespace object is not pinned: %+v", plan.PinnedNamespaces)
	}
	if !reflect.DeepEqual(plan.BindClusterRoles, []string{"acme-reader"}) {
		t.Errorf("a RoleBinding to a ClusterRole needs bind on exactly that ClusterRole: %+v", plan.BindClusterRoles)
	}
}

func TestPlanGrant_NamespaceScopeRefusals(t *testing.T) {
	withCRole := mustNeeds(t, scopeStream(onCtx(docClusterRole)))
	for _, tc := range []struct {
		name  string
		needs grantNeeds
		scope applyScope
		want  string
	}{
		{"cluster-scoped kinds in a namespace grant", withCRole, applyScope{}, "cluster-scoped kinds no namespace grant can cover: rbac.authorization.k8s.io/clusterroles"},
		{"objects outside the named namespaces", mustNeeds(t, scopeStream()), applyScope{Namespaces: []string{"other"}}, "objects outside --apply-scope"},
		{"a named namespace the env renders nothing in", mustNeeds(t, scopeStream()), applyScope{Namespaces: []string{"acme-prod", "typo"}}, `namespace "typo" is in scope but the env renders nothing there`},
		{"an env that renders nothing here", grantNeeds{}, applyScope{}, "applies nothing to this cluster"},
	} {
		if _, err := planGrant(tc.needs, tc.scope); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}

// ── the grant shapes ────────────────────────────────────────────────────────

func testRBAC(t *testing.T, auth string, scope applyScope, extra ...string) connectRBAC {
	t.Helper()
	plan, err := planGrant(mustNeeds(t, scopeStream(extra...)), scope)
	if err != nil {
		t.Fatal(err)
	}
	r := connectRBAC{
		ClusterName: "prod-us", Env: "prod", KubeContext: testCtx, Auth: auth,
		TokenNamespace: connectTokenNamespace, Scope: scope, Plan: plan,
	}
	if auth == authGCP {
		r.BindingGSA = "fc-prod-ab12@acme.iam.gserviceaccount.com"
	}
	return r
}

func decodeGrant(t *testing.T, r connectRBAC) (roles map[string]rbacv1.Role, cr *rbacv1.ClusterRole, bindings []rbacv1.RoleBinding, crb *rbacv1.ClusterRoleBinding) {
	t.Helper()
	roles = map[string]rbacv1.Role{}
	for _, o := range r.grantObjects() {
		doc := o.yaml()
		switch o.Kind {
		case "Role":
			var v rbacv1.Role
			if err := yaml.UnmarshalStrict([]byte(doc), &v); err != nil {
				t.Fatalf("%v\n%s", err, doc)
			}
			roles[v.Namespace] = v
		case "ClusterRole":
			cr = &rbacv1.ClusterRole{}
			if err := yaml.UnmarshalStrict([]byte(doc), cr); err != nil {
				t.Fatalf("%v\n%s", err, doc)
			}
		case "RoleBinding":
			var v rbacv1.RoleBinding
			if err := yaml.UnmarshalStrict([]byte(doc), &v); err != nil {
				t.Fatalf("%v\n%s", err, doc)
			}
			bindings = append(bindings, v)
		case "ClusterRoleBinding":
			crb = &rbacv1.ClusterRoleBinding{}
			if err := yaml.UnmarshalStrict([]byte(doc), crb); err != nil {
				t.Fatalf("%v\n%s", err, doc)
			}
		}
	}
	return
}

func ruleStrings(rules []rbacv1.PolicyRule) []string {
	var out []string
	for _, r := range rules {
		s := strings.Join(r.APIGroups, ",") + "|" + strings.Join(r.Resources, ",") + "|" + strings.Join(r.Verbs, ",")
		if len(r.ResourceNames) > 0 {
			s += "|" + strings.Join(r.ResourceNames, ",")
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func TestGrant_NamespaceScopeIsARolePerNamespaceFromTheRender(t *testing.T) {
	cron := onCtx("apiVersion: batch/v1\nkind: CronJob\nmetadata:\n  name: sweep\n  namespace: acme-prod\n")
	r := testRBAC(t, authGCP, applyScope{Namespaces: []string{"acme-prod"}}, cron)
	roles, cr, bindings, crb := decodeGrant(t, r)

	role, ok := roles["acme-prod"]
	if !ok || len(roles) != 1 {
		t.Fatalf("roles = %v, want exactly one, in acme-prod", roles)
	}
	got := ruleStrings(role.Rules)
	for _, want := range []string{
		"apps|deployments|create,delete,get,list,patch,update,watch",
		"|services|create,delete,get,list,patch,update,watch",
		"|configmaps|create,delete,get,list,patch,update,watch",
		"batch|cronjobs|create,delete,get,list,patch,update,watch",
		// Flux's health check follows a Deployment to its ReplicaSets and Pods.
		"apps|replicasets|get,list,watch",
		"|pods|get,list,watch",
	} {
		if !hasString(got, want) {
			t.Errorf("namespace Role omits %q:\n%s", want, strings.Join(got, "\n"))
		}
	}
	// A kind the render does not contain is not granted: the tripwire.
	for _, r := range got {
		if strings.Contains(r, "secrets") || strings.Contains(r, "*") || strings.Contains(r, "bind") || strings.Contains(r, "impersonate") {
			t.Errorf("namespace Role grants %q, which the render does not need", r)
		}
	}
	if len(bindings) != 1 || bindings[0].RoleRef.Kind != "Role" || bindings[0].Namespace != "acme-prod" {
		t.Errorf("bindings = %+v, want one RoleBinding to the Role", bindings)
	}
	wantSubject := []rbacv1.Subject{{Kind: "User", Name: "fc-prod-ab12@acme.iam.gserviceaccount.com", APIGroup: "rbac.authorization.k8s.io"}}
	if !reflect.DeepEqual(bindings[0].Subjects, wantSubject) {
		t.Errorf("subjects = %+v, want the binding's own GSA %+v", bindings[0].Subjects, wantSubject)
	}

	// The render declares the Namespace object: pinned by name, no create,
	// no delete, so a prune can never remove a namespace.
	if cr == nil || crb == nil {
		t.Fatalf("a rendered Namespace needs a name-pinned ClusterRole and binding: %v %v", cr, crb)
	}
	if got := ruleStrings(cr.Rules); !reflect.DeepEqual(got, []string{"|namespaces|get,patch,update|acme-prod"}) {
		t.Errorf("namespace pin = %v", got)
	}
	if crb.RoleRef.Kind != "ClusterRole" || !reflect.DeepEqual(crb.Subjects, wantSubject) {
		t.Errorf("ClusterRoleBinding = %+v", crb)
	}
}

func TestGrant_NamespaceScopeBindsClusterRolesByName(t *testing.T) {
	r := testRBAC(t, authToken, applyScope{}, onCtx(docRoleBinding))
	roles, _, _, _ := decodeGrant(t, r)
	got := ruleStrings(roles["acme-prod"].Rules)
	for _, want := range []string{
		"rbac.authorization.k8s.io|rolebindings|create,delete,get,list,patch,update,watch",
		"rbac.authorization.k8s.io|clusterroles|bind|acme-reader",
		"rbac.authorization.k8s.io|roles|bind,escalate",
	} {
		if !hasString(got, want) {
			t.Errorf("Role omits %q:\n%s", want, strings.Join(got, "\n"))
		}
	}
}

func TestGrant_ClusterScopeIsARenderDerivedClusterRoleWithBindEscalate(t *testing.T) {
	r := testRBAC(t, authToken, applyScope{Cluster: true}, onCtx(docClusterRole), onCtx(docRoleBinding))
	roles, cr, bindings, crb := decodeGrant(t, r)
	if len(roles) != 0 || len(bindings) != 0 {
		t.Fatalf("cluster scope renders namespaced objects: %v %v", roles, bindings)
	}
	if cr == nil || crb == nil || cr.Name != "forge-connect-prod-us-prod" {
		t.Fatalf("ClusterRole = %v, ClusterRoleBinding = %v", cr, crb)
	}
	got := ruleStrings(cr.Rules)
	for _, want := range []string{
		"apps|deployments|create,delete,get,list,patch,update,watch",
		// the render's own ClusterRole kind, merged with bind and escalate
		"rbac.authorization.k8s.io|clusterroles|bind,create,delete,escalate,get,list,patch,update,watch",
		"rbac.authorization.k8s.io|roles|bind,escalate",
		"|namespaces|create,delete,get,list,patch,update,watch",
	} {
		if !hasString(got, want) {
			t.Errorf("ClusterRole omits %q:\n%s", want, strings.Join(got, "\n"))
		}
	}
	for _, r := range got {
		if strings.Contains(r, "*") || strings.Contains(r, "secrets") || strings.Contains(r, "impersonate") {
			t.Errorf("ClusterRole grants %q", r)
		}
	}
	wantSubject := []rbacv1.Subject{{Kind: "ServiceAccount", Name: "forge-connect-prod-us-prod", Namespace: "forge-system"}}
	if !reflect.DeepEqual(crb.Subjects, wantSubject) {
		t.Errorf("subjects = %+v, want %+v", crb.Subjects, wantSubject)
	}
}

// Without an RBAC kind in the render, even cluster scope holds no bind or
// escalate: a render that creates no roles needs neither.
func TestGrant_ClusterScopeWithoutRBACKindsHoldsNoBindOrEscalate(t *testing.T) {
	r := testRBAC(t, authToken, applyScope{Cluster: true})
	_, cr, _, _ := decodeGrant(t, r)
	for _, rule := range ruleStrings(cr.Rules) {
		if strings.Contains(rule, "bind") || strings.Contains(rule, "escalate") {
			t.Errorf("cluster rule %q without an RBAC kind in the render", rule)
		}
	}
}

func TestGrant_NeverImpersonatesAndNeverWritesBootstrapObjects(t *testing.T) {
	for _, auth := range []string{authGCP, authToken} {
		for _, scope := range []applyScope{{Cluster: true}, {}} {
			r := testRBAC(t, auth, scope, onCtx(docRoleBinding))
			all := r.bootstrapManifests()
			for _, banned := range []string{"impersonate", "flux-source@", "system:serviceaccount:flux-", "escalate\n- ", "- serviceaccounts"} {
				if strings.Contains(all, banned) && banned != "escalate\n- " {
					t.Errorf("auth=%s scope=%s: the grant contains %q:\n%s", auth, scope, banned, all)
				}
			}
			// The ONLY kinds connect writes: the grant, and (token) the identity.
			for _, doc := range splitDocs(all) {
				var meta struct{ Kind string }
				if err := yaml.Unmarshal([]byte(doc), &meta); err != nil {
					t.Fatal(err)
				}
				switch meta.Kind {
				case "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding":
				case "Namespace", "ServiceAccount", "Secret":
					if auth != authToken {
						t.Errorf("auth=%s writes a %s, which only the token identity needs", auth, meta.Kind)
					}
				default:
					t.Errorf("auth=%s writes unexpected kind %s", auth, meta.Kind)
				}
			}
		}
	}
}

func TestGrant_EachBindingOwnsItsOwnObjectsAndIdentity(t *testing.T) {
	prod := testRBAC(t, authToken, applyScope{})
	staging := prod
	staging.Env = "staging"
	if prod.bindingName() == staging.bindingName() {
		t.Fatal("two envs on one cluster share a grant name; one connect would rewrite the other's subject")
	}
	if prod.tokenServiceAccount() == staging.tokenServiceAccount() || prod.tokenSecretName() == staging.tokenSecretName() {
		t.Error("two bindings share a token identity")
	}
	other := prod
	other.ClusterName = "prod-eu"
	if prod.bindingName() == other.bindingName() {
		t.Error("two clusters share a grant name")
	}
	if prod.bindingSelector() == staging.bindingSelector() {
		t.Error("two bindings share a selector, so a diff or prune of one would see the other")
	}
}

func TestGrant_EveryObjectCarriesTheBindingLabels(t *testing.T) {
	r := testRBAC(t, authToken, applyScope{})
	for _, doc := range splitDocs(r.baseManifests()) {
		var obj struct {
			Kind     string
			Metadata struct{ Labels map[string]string }
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatal(err)
		}
		l := obj.Metadata.Labels
		if l[forgeManagedLabelKey] != forgeManagedLabelValue || l[connectClusterLabelKey] != "prod-us" || l[connectBindingLabelKey] != "prod" {
			t.Errorf("%s labels = %v", obj.Kind, l)
		}
	}
}

func TestGrant_TokenIdentityIsMintedPerBinding(t *testing.T) {
	r := testRBAC(t, authToken, applyScope{})
	docs := splitDocs(r.baseManifests())
	joined := strings.Join(docs, "\n")
	for _, want := range []string{
		"kind: Namespace", "name: forge-system",
		"kind: ServiceAccount", "name: forge-connect-prod-us-prod",
		"type: kubernetes.io/service-account-token",
		"kubernetes.io/service-account.name: forge-connect-prod-us-prod",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("token identity omits %q", want)
		}
	}
	if g := testRBAC(t, authGCP, applyScope{}).identityManifests(); len(g) != 0 {
		t.Errorf("gcp mints %d identity objects; its identity is the GSA", len(g))
	}
}

func TestGrant_RenderIsDeterministic(t *testing.T) {
	a := testRBAC(t, authGCP, applyScope{Cluster: true}, onCtx(docCRD), onCtx(docWidget)).bootstrapManifests()
	for i := 0; i < 20; i++ {
		if b := testRBAC(t, authGCP, applyScope{Cluster: true}, onCtx(docCRD), onCtx(docWidget)).bootstrapManifests(); a != b {
			t.Fatalf("render %d differs:\n%s\n---\n%s", i, a, b)
		}
	}
}

// A namespaced custom resource needs its CRD, which is cluster-scoped, so the
// derived grant for such a render is cluster scope — and namespace scope says so.
func TestGrant_CRDsAreClusterScopedAndCustomResourcesAreGrantedByTheirCRDPlural(t *testing.T) {
	stream := scopeStream(onCtx(docCRD), onCtx(docWidget))
	if _, err := planGrant(mustNeeds(t, stream), applyScope{}); err == nil ||
		!strings.Contains(err.Error(), "customresourcedefinitions") {
		t.Fatalf("namespace scope accepted a render with a CRD: %v", err)
	}
	r := testRBAC(t, authToken, applyScope{Cluster: true}, onCtx(docCRD), onCtx(docWidget))
	_, cr, _, _ := decodeGrant(t, r)
	got := ruleStrings(cr.Rules)
	for _, want := range []string{
		"acme.dev|widgets|create,delete,get,list,patch,update,watch",
		"apiextensions.k8s.io|customresourcedefinitions|create,delete,get,list,patch,update,watch",
	} {
		if !hasString(got, want) {
			t.Errorf("ClusterRole omits %q:\n%s", want, strings.Join(got, "\n"))
		}
	}
}
