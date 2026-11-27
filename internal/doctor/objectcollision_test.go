package doctor

import (
	"context"
	"strings"
	"testing"
)

// Two environments writing one object is invisible to every other check: each
// env renders correctly alone, every manifest is applyable, and `kubectl
// apply` overwrites a same-named object without a word. So this check is the
// only thing standing between "last writer wins" and a ten-hour outage, and
// the cases below are its whole contract at both scopes.
//
// The asymmetry between the scopes is deliberate and load-bearing, so it is
// pinned in both directions: a namespaced address collides on identity alone,
// a cluster-scoped one only when the CONTENT differs (every env legitimately
// renders the same CRD / GatewayClass, and warning about that is how a check
// teaches people to ignore it).

// renderIn builds one env's render: the objects it applies plus the slice of
// the `output` contract that says which cluster they land on.
func renderIn(t *testing.T, env, cluster string, objs ...string) envRender {
	t.Helper()
	return renderFromJSON(t, env, `{"output":{"cluster_target":{"cluster":"`+cluster+
		`","namespace":"ignored"},"manifests":[`+strings.Join(objs, ",")+`]}}`)
}

// deployIn renders a Deployment carrying the app label forge stamps, so the
// attribution path under test is the real one.
func deployIn(name, ns, image string) string {
	return `{"apiVersion":"apps/v1","kind":"Deployment",` +
		`"metadata":{"name":"` + name + `","namespace":"` + ns + `",` +
		`"labels":{"app.kubernetes.io/name":"` + name + `"}},` +
		`"spec":{"template":{"spec":{"containers":[{"name":"` + name + `","image":"` + image + `"}]}}}}`
}

// clusterRoleBinding mirrors forge's own render_cluster_rbac: cluster-scoped,
// ONE name for every env, and a subject namespace that is the ENV's. That is
// the singleton whichever env deploys last steals.
func clusterRoleBinding(ns string) string {
	return `{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"ClusterRoleBinding",` +
		`"metadata":{"name":"workspace-controller-clusterrolebinding",` +
		`"labels":{"app.kubernetes.io/name":"workspace-controller"}},` +
		`"subjects":[{"kind":"ServiceAccount","name":"workspace-controller","namespace":"` + ns + `"}],` +
		`"roleRef":{"apiGroup":"rbac.authorization.k8s.io","kind":"ClusterRole","name":"workspace-controller-clusterrole"}}`
}

// clusterRole is the benign half of the same render: cluster-scoped, one
// name, and byte-identical in every env.
const clusterRole = `{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"ClusterRole",` +
	`"metadata":{"name":"workspace-controller-clusterrole",` +
	`"labels":{"app.kubernetes.io/name":"workspace-controller"}},` +
	`"rules":[{"apiGroups":[""],"resources":["pods"],"verbs":["get","list","watch"]}]}`

// The regression, verbatim: `dev-k8s` defaulted to the namespace `dev`
// deploys into, and its deploy replaced dev's daemon-gateway in place.
func TestObjectCollisionFlagsTwoEnvsOnOneNamespacedObject(t *testing.T) {
	env := envWithRender([]envRender{
		renderIn(t, "dev", "k3d-control-plane", deployIn("daemon-gateway", "control-plane-dev", "gw:dev")),
		renderIn(t, "dev-k8s", "k3d-control-plane", deployIn("daemon-gateway", "control-plane-dev", "gw:k8s")),
	})

	got := CheckObjectCollision(context.Background(), env)
	if got.Status != StatusWarn {
		t.Fatalf("status = %q, want %q — both envs write Deployment/daemon-gateway into "+
			"control-plane-dev on one cluster\nmessage: %s\nevidence: %s",
			got.Status, StatusWarn, got.Message, got.Evidence)
	}
	// The evidence has to name the address AND both claimants: "something
	// collides" is not actionable, and which env is in the wrong place is the
	// whole question.
	for _, want := range []string{"control-plane-dev", "daemon-gateway", "dev", "dev-k8s", "k3d-control-plane"} {
		if !strings.Contains(got.Evidence, want) {
			t.Errorf("evidence does not name %q:\n%s", want, got.Evidence)
		}
	}
}

// The false positive that would have made this check unreadable: the SAME
// namespace name on two DIFFERENT clusters is not a collision. Nothing
// overwrites anything — they are two namespaces that happen to share a
// spelling, which is the normal shape of per-cluster environments.
func TestObjectCollisionIgnoresSameNamespaceOnDifferentClusters(t *testing.T) {
	env := envWithRender([]envRender{
		renderIn(t, "dev-k8s", "k3d-control-plane", deployIn("api", "app", "api:dev")),
		renderIn(t, "prod", "gke-prod", deployIn("api", "app", "api:prod")),
	})

	got := CheckObjectCollision(context.Background(), env)
	if got.Status != StatusPass {
		t.Fatalf("status = %q, want %q — `app` on k3d-control-plane and `app` on gke-prod are "+
			"two different namespaces\nmessage: %s\nevidence: %s",
			got.Status, StatusPass, got.Message, got.Evidence)
	}
}

// One env can legitimately span two clusters using the same namespace name in
// each — control-plane's `dev` puts workspace-proxy in control-plane-dev on
// k3d-cp-daemon while its other workloads sit in control-plane-dev on
// k3d-control-plane. An env cannot collide with ITSELF, and a check that said
// otherwise would be permanently yellow on a correct project.
func TestObjectCollisionNeverSelfCollidesOnOneEnvSpanningClusters(t *testing.T) {
	// The workload's own OnCluster runtime is what pins workspace-proxy to
	// the daemon cluster; everything else rides the env-wide cluster_target.
	body := `{"output":{"cluster_target":{"cluster":"k3d-control-plane","namespace":"control-plane-dev"},` +
		`"workloads":[{"name":"workspace-proxy","runtime":{"type":"cluster","cluster":"k3d-cp-daemon"}}],` +
		`"manifests":[` +
		deployIn("workspace-proxy", "control-plane-dev", "proxy:dev") + `,` +
		deployIn("daemon-gateway", "control-plane-dev", "gw:dev") + `]}}`
	env := envWithRender([]envRender{renderFromJSON(t, "dev", body)})

	got := CheckObjectCollision(context.Background(), env)
	if got.Status != StatusPass {
		t.Fatalf("status = %q, want %q — one env spanning two clusters is not a collision\n"+
			"message: %s\nevidence: %s", got.Status, StatusPass, got.Message, got.Evidence)
	}
}

// A namespaced address is a collision on IDENTITY, not on content: two envs
// owning one object in one namespace are fighting over it whatever the bytes
// say today, because being different environments is what makes their content
// diverge tomorrow. Byte-identical here must still warn — the opposite of the
// cluster-scoped rule below, and the asymmetry is the point.
func TestObjectCollisionWarnsOnNamespacedEvenWhenContentMatches(t *testing.T) {
	same := deployIn("api", "shared-ns", "api:1.0")
	env := envWithRender([]envRender{
		renderIn(t, "dev", "k3d-control-plane", same),
		renderIn(t, "e2e", "k3d-control-plane", same),
	})

	got := CheckObjectCollision(context.Background(), env)
	if got.Status != StatusWarn {
		t.Fatalf("status = %q, want %q — identical bytes today does not make two envs owning "+
			"one namespaced object safe\nmessage: %s", got.Status, StatusWarn, got.Message)
	}
}

// The cluster-scoped defect, verbatim: one ClusterRoleBinding name across
// every env, whose subject namespace differs — so the last env to deploy
// silently repoints it at ITS namespace and every other env's controller
// loses its grant.
func TestObjectCollisionFlagsClusterScopedSingletonWithDifferingContent(t *testing.T) {
	env := envWithRender([]envRender{
		renderIn(t, "dev", "k3d-control-plane", clusterRoleBinding("control-plane-dev")),
		renderIn(t, "e2e", "k3d-control-plane", clusterRoleBinding("control-plane-e2e")),
	})

	got := CheckObjectCollision(context.Background(), env)
	if got.Status != StatusWarn {
		t.Fatalf("status = %q, want %q — one cluster-scoped name, two different subjects\n"+
			"message: %s\nevidence: %s", got.Status, StatusWarn, got.Message, got.Evidence)
	}
	// Naming the field is what turns this from "these differ somehow" into a
	// one-line explanation of the outage.
	if !strings.Contains(got.Evidence, "subjects[0].namespace") {
		t.Errorf("evidence should name the differing field (subjects[0].namespace):\n%s", got.Evidence)
	}
	for _, want := range []string{"ClusterRoleBinding", "workspace-controller-clusterrolebinding", "dev", "e2e"} {
		if !strings.Contains(got.Evidence, want) {
			t.Errorf("evidence does not name %q:\n%s", want, got.Evidence)
		}
	}
}

// The benign case that must stay silent. Every env expects the same CRD,
// GatewayClass and ClusterRole to exist, identically — that is shared
// infrastructure being applied idempotently, not a fight. Warning here is
// how a check earns a permanent yellow and stops being read.
func TestObjectCollisionIgnoresIdenticalClusterScopedSharedInfra(t *testing.T) {
	env := envWithRender([]envRender{
		renderIn(t, "dev", "k3d-control-plane", clusterRole),
		renderIn(t, "e2e", "k3d-control-plane", clusterRole),
		renderIn(t, "prod", "k3d-control-plane", clusterRole),
	})

	got := CheckObjectCollision(context.Background(), env)
	if got.Status != StatusPass {
		t.Fatalf("status = %q, want %q — three envs rendering one IDENTICAL ClusterRole is shared "+
			"infrastructure\nmessage: %s\nevidence: %s", got.Status, StatusPass, got.Message, got.Evidence)
	}
}

// Distinct cluster-scoped names never collide, whatever their content.
func TestObjectCollisionIgnoresDistinctClusterScopedNames(t *testing.T) {
	env := envWithRender([]envRender{
		renderIn(t, "dev", "k3d-control-plane", strings.Replace(clusterRole,
			"workspace-controller-clusterrole", "dev-clusterrole", 1)),
		renderIn(t, "e2e", "k3d-control-plane", strings.Replace(clusterRole,
			"workspace-controller-clusterrole", "e2e-clusterrole", 1)),
	})

	got := CheckObjectCollision(context.Background(), env)
	if got.Status != StatusPass {
		t.Fatalf("status = %q, want %q — different names are different objects\nmessage: %s",
			got.Status, StatusPass, got.Message)
	}
}

// When the render says nothing about which cluster an env deploys to, the
// honest answer is "unknown", not "they share one". Assuming a shared cluster
// invents the collision; assuming distinct ones hides it.
func TestObjectCollisionReportsUndeterminedWhenClusterIsUnknown(t *testing.T) {
	body := func(ns string) string {
		return `{"output":{"manifests":[` + deployIn("api", ns, "api:1") + `]}}`
	}
	env := envWithRender([]envRender{
		renderFromJSON(t, "dev", body("shared-ns")),
		renderFromJSON(t, "e2e", body("shared-ns")),
	})

	got := CheckObjectCollision(context.Background(), env)
	if got.Status != StatusUnknown {
		t.Fatalf("status = %q, want %q — no cluster_target and no per-service cluster means the "+
			"landing site is unknown\nmessage: %s\nevidence: %s",
			got.Status, StatusUnknown, got.Message, got.Evidence)
	}
	if !strings.Contains(got.Evidence, "could not be determined") {
		t.Errorf("evidence should say WHY it is undetermined:\n%s", got.Evidence)
	}
}

// The first-class `forge.dev/cluster` label is how forge's gateway/route
// builders pin one manifest to one cluster, and the deploy-time scoper honours
// it ahead of every other rule. Reading it is what keeps this check agreeing
// with where the object actually lands.
func TestObjectCollisionHonoursTheFirstClassClusterLabel(t *testing.T) {
	pinned := func(cluster string) string {
		return `{"apiVersion":"gateway.networking.k8s.io/v1","kind":"Gateway",` +
			`"metadata":{"name":"public","namespace":"shared-ns",` +
			`"labels":{"forge.dev/cluster":"` + cluster + `"}},"spec":{}}`
	}
	// Both envs default to the SAME cluster; only the label separates the two
	// Gateways. Without it this is a collision.
	env := envWithRender([]envRender{
		renderIn(t, "dev", "k3d-control-plane", pinned("k3d-control-plane")),
		renderIn(t, "e2e", "k3d-control-plane", pinned("k3d-cp-daemon")),
	})

	got := CheckObjectCollision(context.Background(), env)
	if got.Status != StatusPass {
		t.Fatalf("status = %q, want %q — the two Gateways are pinned to different clusters\n"+
			"message: %s\nevidence: %s", got.Status, StatusPass, got.Message, got.Evidence)
	}
}

// A check that is never registered is a check that never runs — and this one
// belongs to the deployability set precisely so CI answers it on a bare
// checkout, before either env has been applied.
func TestObjectCollisionIsRegistered(t *testing.T) {
	for _, c := range deployabilityChecks() {
		if c.name == "Object Collision" {
			return
		}
	}
	t.Fatal("Object Collision is not in deployabilityChecks() — it would never run under " +
		"`forge doctor` or `forge doctor --signal deploy`")
}

// stamped splices forge's per-env ownership label into an existing fixture's
// label map, the way kcl/lib/labels.k stamps every rendered object.
func stamped(obj, env string) string {
	return strings.Replace(obj, `"labels":{`, `"labels":{"forge.dev/env":"`+env+`",`, 1)
}

// A partial render is worse for THIS check than for a per-object one. It
// reasons about what environments have in common, so an unread env does not
// merely go unexamined — it takes every collision it was half of with it. The
// env that fails to render is exactly the env that might have been colliding,
// which is why "2 of 3 rendered, no collisions found" is not a finding anyone
// may act on.
func TestObjectCollisionPartialRenderIsNeverAPass(t *testing.T) {
	// dev renders and is clean ON ITS OWN. dev-k8s — the env that collides
	// with it in the real defect — is the one that failed to evaluate.
	env := envWithRender([]envRender{
		renderIn(t, "dev", "k3d-control-plane", deployIn("daemon-gateway", "control-plane-dev", "gw:dev")),
		unreadable("dev-k8s"),
	})

	got := CheckObjectCollision(context.Background(), env)
	if got.Status == StatusPass || got.Status == StatusSkip {
		t.Fatalf("status = %q — the env that went unread is the one that would have collided; "+
			"a clean answer here is a false all-clear on the defect this check exists for\nmessage: %s",
			got.Status, got.Message)
	}
	if got.Status != StatusUnknown {
		t.Fatalf("status = %q, want %q — nothing is known to be broken, the facts are missing\nmessage: %s",
			got.Status, StatusUnknown, got.Message)
	}
	if !strings.Contains(got.Message, "dev-k8s") {
		t.Errorf("message does not name the unread environment: %s", got.Message)
	}
	if !strings.Contains(got.Message, "1 of 2 env(s) read") {
		t.Errorf("message does not say how much was examined: %s", got.Message)
	}
	if !strings.Contains(got.Evidence, "dev-k8s") || !strings.Contains(got.Evidence, "ingress_host") {
		t.Errorf("evidence does not carry the render error: %s", got.Evidence)
	}
}

// forge stamps `forge.dev/env: <env>` on every object it renders, so its
// value differs between any two envs BY CONSTRUCTION. If content comparison
// counted it, every legitimately-shared cluster-scoped singleton — the
// identical CRD / GatewayClass / ClusterRole every env expects to exist —
// would warn forever, from a label doing its job rather than from a defect.
func TestObjectCollisionIgnoresThePerEnvOwnershipStamp(t *testing.T) {
	env := envWithRender([]envRender{
		renderIn(t, "dev", "k3d-control-plane", stamped(clusterRole, "dev")),
		renderIn(t, "e2e", "k3d-control-plane", stamped(clusterRole, "e2e")),
		renderIn(t, "prod", "k3d-control-plane", stamped(clusterRole, "prod")),
	})

	got := CheckObjectCollision(context.Background(), env)
	if got.Status != StatusPass {
		t.Fatalf("status = %q, want %q — the ClusterRole is identical apart from the env stamp, "+
			"which can never match across envs\nmessage: %s\nevidence: %s",
			got.Status, StatusPass, got.Message, got.Evidence)
	}
}

// The other half of the same hazard: the stamp must not STEAL the
// attribution of a real collision. firstJSONDiff reports the first differing
// path in sorted key order and `metadata` sorts before `subjects`, so a
// counted stamp would report the ClusterRoleBinding outage as
// "metadata.labels.forge.dev/env differs" — pointing at the one field that
// was always going to differ, instead of the subject namespace that actually
// causes it.
func TestObjectCollisionNamesTheRealFieldDespiteTheEnvStamp(t *testing.T) {
	env := envWithRender([]envRender{
		renderIn(t, "dev", "k3d-control-plane", stamped(clusterRoleBinding("control-plane-dev"), "dev")),
		renderIn(t, "e2e", "k3d-control-plane", stamped(clusterRoleBinding("control-plane-e2e"), "e2e")),
	})

	got := CheckObjectCollision(context.Background(), env)
	if got.Status != StatusWarn {
		t.Fatalf("status = %q, want %q — the subjects still differ\nmessage: %s",
			got.Status, StatusWarn, got.Message)
	}
	if !strings.Contains(got.Evidence, "subjects[0].namespace") {
		t.Errorf("evidence should name the field that causes the outage:\n%s", got.Evidence)
	}
	if strings.Contains(got.Evidence, forgeEnvLabel) {
		t.Errorf("evidence blames the per-env ownership stamp, which differs by design:\n%s", got.Evidence)
	}
}
