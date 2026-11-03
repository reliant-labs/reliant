package doctor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// A cluster-scoped object that no env renders is invisible to every other
// check and to every forge command. CheckObjectCollision compares envs to
// each OTHER, so it goes quiet the moment a name stops colliding — which is
// exactly what the rbac.k namespace-suffix fix achieved, and exactly what
// left the old names behind. CheckClusterWorkloads reads the namespace's
// pods, and these objects have no namespace. `forge env deploy` prunes
// namespaced Deployments only. So this check is the only thing between a
// rename and an object nothing will ever reclaim.
//
// Both halves of its contract are pinned below, because either one alone is
// worthless: a check that reports nothing is indistinguishable from a clean
// cluster, and one that reports everything is indistinguishable from a
// broken one. The second half is the harder one — forge stamps
// `managed-by=forge` on the Envoy Gateway chart it installs, so a
// managed-by-only rule would have reported the platform forever.

// clusterScopedJSON renders one cluster-scoped object: no namespace, forge's
// managed-by label, and the per-env ownership stamp when env is non-empty.
// Mirrors what kcl/lib/rbac.k + kcl/lib/labels.k actually emit.
func clusterScopedJSON(kind, name, env string) string {
	stamp := ""
	if env != "" {
		stamp = `,"forge.dev/env":"` + env + `"`
	}
	return `{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"` + kind + `",` +
		`"metadata":{"name":"` + name + `",` +
		`"labels":{"app.kubernetes.io/managed-by":"forge",` +
		`"app.kubernetes.io/name":"workspace-controller"` + stamp + `}},` +
		`"rules":[{"apiGroups":[""],"resources":["pods"],"verbs":["get"]}]}`
}

// liveObj is one object as the cluster reports it.
func liveObj(kind, name, env string) liveClusterObject {
	labels := map[string]string{
		"app.kubernetes.io/managed-by": "forge",
		"app.kubernetes.io/name":       "workspace-controller",
	}
	if env != "" {
		labels[forgeEnvLabel] = env
	}
	return liveClusterObject{Kind: kind, Name: name, Labels: labels}
}

// fakeProbe serves a fixed per-context object set. Every declared context
// is "known" unless unknownContexts says otherwise, and listErr fabricates
// the unreachable-cluster case no real cluster can be asked to produce.
func fakeProbe(objs map[string][]liveClusterObject) orphanProbe {
	return orphanProbe{
		contexts: func(context.Context) (map[string]bool, error) {
			known := map[string]bool{}
			for c := range objs {
				known[c] = true
			}
			return known, nil
		},
		list: func(_ context.Context, kctx, kind string) ([]liveClusterObject, error) {
			var out []liveClusterObject
			for _, o := range objs[kctx] {
				if strings.EqualFold(o.Kind, kind) {
					out = append(out, o)
				}
			}
			return out, nil
		},
	}
}

// THE REGRESSION, VERBATIM. The rbac.k fix suffixed an operator's
// ClusterRole/ClusterRoleBinding with the namespace, so both envs now
// render `workspace-controller-<ns>-cluster*` — and the cluster still holds
// the pre-fix `workspace-controller-clusterrole` and
// `-clusterrolebinding`, which no env renders and no deploy will ever touch
// again. Measured on k3d-control-plane immediately after the fix.
func TestOrphanedClusterFindsObjectsNoEnvRenders(t *testing.T) {
	env := envWithRender([]envRender{
		renderIn(t, "dev", "k3d-control-plane",
			clusterScopedJSON("ClusterRole", "workspace-controller-control-plane-dev-clusterrole", "dev"),
			clusterScopedJSON("ClusterRoleBinding", "workspace-controller-control-plane-dev-clusterrolebinding", "dev")),
		renderIn(t, "e2e", "k3d-control-plane",
			clusterScopedJSON("ClusterRole", "workspace-controller-control-plane-e2e-clusterrole", "e2e"),
			clusterScopedJSON("ClusterRoleBinding", "workspace-controller-control-plane-e2e-clusterrolebinding", "e2e")),
	})
	// The cluster holds all four rendered objects PLUS the two pre-fix ones.
	probe := fakeProbe(map[string][]liveClusterObject{
		"k3d-control-plane": {
			liveObj("ClusterRole", "workspace-controller-control-plane-dev-clusterrole", "dev"),
			liveObj("ClusterRoleBinding", "workspace-controller-control-plane-dev-clusterrolebinding", "dev"),
			liveObj("ClusterRole", "workspace-controller-control-plane-e2e-clusterrole", "e2e"),
			liveObj("ClusterRoleBinding", "workspace-controller-control-plane-e2e-clusterrolebinding", "e2e"),
			liveObj("ClusterRole", "workspace-controller-clusterrole", "dev"),
			liveObj("ClusterRoleBinding", "workspace-controller-clusterrolebinding", "dev"),
		},
	})

	got := checkOrphanedClusterObjects(context.Background(), env, probe)
	if got.Status != StatusWarn {
		t.Fatalf("status = %q, want %q — two pre-rename objects exist that no env renders\nmessage: %s\nevidence: %s",
			got.Status, StatusWarn, got.Message, got.Evidence)
	}
	// The evidence must name the orphans AND the env to redeploy: "something
	// is orphaned" is not actionable, and which env reclaims it is the
	// question a reader has next.
	for _, want := range []string{
		"workspace-controller-clusterrole",
		"workspace-controller-clusterrolebinding",
		"k3d-control-plane",
		"redeploy dev",
	} {
		if !strings.Contains(got.Evidence, want) {
			t.Errorf("evidence does not name %q:\n%s", want, got.Evidence)
		}
	}
	// The objects the envs DO render must not be listed — a report that
	// includes them is a report nobody can act on.
	for _, live := range []string{
		"workspace-controller-control-plane-dev-clusterrole",
		"workspace-controller-control-plane-e2e-clusterrolebinding",
	} {
		if strings.Contains(got.Evidence, live) {
			t.Errorf("evidence lists %q, which dev/e2e still render:\n%s", live, got.Evidence)
		}
	}
}

// THE OTHER HALF, and the one that makes the check worth reading: when
// every cluster-scoped object IS rendered by some env, the check is silent.
// Without this, "no orphans" and "the check is broken" produce identical
// output.
func TestOrphanedClusterSilentWhenEveryObjectIsRendered(t *testing.T) {
	env := envWithRender([]envRender{
		renderIn(t, "dev", "k3d-control-plane",
			clusterScopedJSON("ClusterRole", "workspace-controller-control-plane-dev-clusterrole", "dev")),
		renderIn(t, "e2e", "k3d-control-plane",
			clusterScopedJSON("ClusterRole", "workspace-controller-control-plane-e2e-clusterrole", "e2e")),
	})
	probe := fakeProbe(map[string][]liveClusterObject{
		"k3d-control-plane": {
			liveObj("ClusterRole", "workspace-controller-control-plane-dev-clusterrole", "dev"),
			liveObj("ClusterRole", "workspace-controller-control-plane-e2e-clusterrole", "e2e"),
		},
	})

	got := checkOrphanedClusterObjects(context.Background(), env, probe)
	if got.Status != StatusPass {
		t.Fatalf("status = %q, want %q — every live cluster-scoped object is rendered by a declared env\nmessage: %s\nevidence: %s",
			got.Status, StatusPass, got.Message, got.Evidence)
	}
}

// The false positive that would have made this check unreadable, measured on
// the real cluster: forge stamps `managed-by=forge` on the Envoy Gateway
// chart it installs via `forge cluster up` (internal/cluster.stampDocAppLabel)
// and on the pinned Gateway API CRDs. Those are PLATFORM objects — no env's
// manifest render contains them and none ever will — so a managed-by-only
// rule reports them forever, and the yellow is permanent and unfixable.
//
// `forge.dev/env` is the discriminator: it marks exactly the objects that
// came from an env render. These carry none.
func TestOrphanedClusterIgnoresPlatformObjectsWithNoEnvStamp(t *testing.T) {
	env := envWithRender([]envRender{
		renderIn(t, "dev", "k3d-control-plane",
			clusterScopedJSON("ClusterRole", "workspace-controller-control-plane-dev-clusterrole", "dev")),
	})
	probe := fakeProbe(map[string][]liveClusterObject{
		"k3d-control-plane": {
			liveObj("ClusterRole", "workspace-controller-control-plane-dev-clusterrole", "dev"),
			// Exactly what k3d-control-plane holds, verbatim: forge-managed,
			// cluster-scoped, rendered by no env, and correct.
			liveObj("ClusterRole", "envoy-gateway-gateway-helm-envoy-gateway-role", ""),
			liveObj("ClusterRoleBinding", "envoy-gateway-gateway-helm-envoy-gateway-rolebinding", ""),
			liveObj("GatewayClass", "eg", ""),
			liveObj("CustomResourceDefinition", "gateways.gateway.networking.k8s.io", ""),
		},
	})

	got := checkOrphanedClusterObjects(context.Background(), env, probe)
	if got.Status != StatusPass {
		t.Fatalf("status = %q, want %q — the platform objects forge installs are not env-rendered and never will be\n"+
			"message: %s\nevidence: %s", got.Status, StatusPass, got.Message, got.Evidence)
	}
	for _, platform := range []string{"envoy-gateway", "eg", "gateways.gateway.networking.k8s.io"} {
		if strings.Contains(got.Evidence, platform) {
			t.Errorf("evidence lists platform object %q as an orphan:\n%s", platform, got.Evidence)
		}
	}
}

// The caveat is a CORRECTNESS requirement, not tone. An env whose last
// deploy predates a rename is still bound to the old name: its pods are up
// and its cluster-scoped grants come from the object listed here. A reader
// who takes this list as a disposal list strips those grants while the pods
// keep running — leases and CR watches start returning `is forbidden` and
// nothing looks broken until someone reads the logs. That is the same outage
// the rename was made to end, arrived at from the other direction.
//
// So the wording is asserted, not assumed: the result must say an object may
// still be serving, and must state the redeploy-first order.
func TestOrphanedClusterSaysAListedObjectMayStillBeServing(t *testing.T) {
	env := envWithRender([]envRender{
		renderIn(t, "dev", "k3d-control-plane",
			clusterScopedJSON("ClusterRole", "workspace-controller-control-plane-dev-clusterrole", "dev")),
	})
	probe := fakeProbe(map[string][]liveClusterObject{
		"k3d-control-plane": {
			liveObj("ClusterRole", "workspace-controller-control-plane-dev-clusterrole", "dev"),
			liveObj("ClusterRoleBinding", "workspace-controller-clusterrolebinding", "dev"),
		},
	})

	got := checkOrphanedClusterObjects(context.Background(), env, probe)
	if got.Status != StatusWarn {
		t.Fatalf("status = %q, want %q", got.Status, StatusWarn)
	}
	// The one-line MESSAGE carries it too, not only the evidence: evidence
	// prints under `-v`, and the person about to run `kubectl delete` is not
	// passing `-v` — they have already decided what the line meant.
	if !strings.Contains(strings.ToLower(got.Message), "may still be serving") {
		t.Errorf("the message a reader sees without -v does not warn that an object may still be in use: %s", got.Message)
	}
	for _, want := range []string{"MAY STILL BE SERVING", "not a disposal list", "redeploy EVERY declared env"} {
		if !strings.Contains(got.Evidence, want) {
			t.Errorf("evidence does not state %q — a reader could take this as safe-to-delete:\n%s", want, got.Evidence)
		}
	}
}

// It must never FAIL. A deliberate orphan is legitimate (an env being
// retired, a rename mid-migration), forge cannot tell intent from accident,
// and a red `forge doctor` over an object that may be load-bearing pushes
// people toward exactly the deletion that causes the outage.
func TestOrphanedClusterNeverFails(t *testing.T) {
	env := envWithRender([]envRender{
		renderIn(t, "dev", "k3d-control-plane",
			clusterScopedJSON("ClusterRole", "keep-clusterrole", "dev")),
	})
	probe := fakeProbe(map[string][]liveClusterObject{
		"k3d-control-plane": {
			liveObj("ClusterRole", "keep-clusterrole", "dev"),
			liveObj("ClusterRole", "orphan-a", "dev"),
			liveObj("ClusterRoleBinding", "orphan-b", "dev"),
			liveObj("Namespace", "orphan-ns", "dev"),
		},
	})

	got := checkOrphanedClusterObjects(context.Background(), env, probe)
	if got.Status == StatusFail {
		t.Fatalf("status = fail — three orphans must warn, never fail: a red doctor over an object that may "+
			"still be serving an env pushes the reader toward the deletion that causes the outage\nmessage: %s",
			got.Message)
	}
	if got.Status != StatusWarn {
		t.Fatalf("status = %q, want %q\nmessage: %s", got.Status, StatusWarn, got.Message)
	}
}

// A cluster this machine's kubeconfig has never heard of is SKIP, not
// UNDETERMINED. A CI runner with no kubeconfig is not the machine that
// deploys prod and has nothing to clean up there; yellowing its report over
// a cluster it is not responsible for is how a check gets filtered out of CI.
func TestOrphanedClusterSkipsAClusterThisMachineDoesNotKnow(t *testing.T) {
	env := envWithRender([]envRender{
		renderIn(t, "prod", "gke-prod", clusterScopedJSON("ClusterRole", "api-clusterrole", "prod")),
	})
	probe := orphanProbe{
		contexts: func(context.Context) (map[string]bool, error) {
			return map[string]bool{"k3d-local": true}, nil // no gke-prod
		},
		list: func(_ context.Context, kctx, _ string) ([]liveClusterObject, error) {
			return nil, fmt.Errorf("list must not be called for unknown context %q", kctx)
		},
	}

	got := checkOrphanedClusterObjects(context.Background(), env, probe)
	if got.Status != StatusSkip {
		t.Fatalf("status = %q, want %q — gke-prod is not in this machine's kubeconfig\nmessage: %s",
			got.Status, StatusSkip, got.Message)
	}
}

// The other side of that line, and the distinction the whole status model
// rests on: a context the kubeconfig DOES know but that did not answer is
// UNDETERMINED. This machine deploys there, forge could not look, and
// reporting a pass would be a false all-clear on the exact defect the check
// exists for.
func TestOrphanedClusterReportsUnreachableClusterAsUndetermined(t *testing.T) {
	env := envWithRender([]envRender{
		renderIn(t, "dev", "k3d-control-plane", clusterScopedJSON("ClusterRole", "api-clusterrole", "dev")),
	})
	probe := orphanProbe{
		contexts: func(context.Context) (map[string]bool, error) {
			return map[string]bool{"k3d-control-plane": true}, nil
		},
		list: func(context.Context, string, string) ([]liveClusterObject, error) {
			return nil, errors.New("dial tcp 127.0.0.1:6443: connect: connection refused")
		},
	}

	got := checkOrphanedClusterObjects(context.Background(), env, probe)
	if got.Status == StatusPass || got.Status == StatusSkip {
		t.Fatalf("status = %q — the cluster is declared and known, so a clean answer here is a false "+
			"all-clear on the objects nobody looked at\nmessage: %s", got.Status, got.Message)
	}
	if got.Status != StatusUnknown {
		t.Fatalf("status = %q, want %q\nmessage: %s", got.Status, StatusUnknown, got.Message)
	}
	if !strings.Contains(got.Evidence, "connection refused") {
		t.Errorf("evidence does not carry WHY the cluster went unread:\n%s", got.Evidence)
	}
}

// A PARTIAL render must never produce an orphan list, and this rule is
// stricter here than renderScope.fold's.
//
// fold keeps a Warn and prefixes it with the scope, which is right when the
// finding is independent of the missing env. Here the finding IS the absence
// of an object from every render, so an unread env does not narrow the
// answer — it FABRICATES it. Every object only that env renders becomes
// indistinguishable from an orphan, and those are precisely the entries
// still load-bearing. A scope prefix is no protection: the reader acts on
// the list, not the preamble.
func TestOrphanedClusterListsNothingWhenAnEnvDidNotRender(t *testing.T) {
	env := envWithRender([]envRender{
		renderIn(t, "dev", "k3d-control-plane",
			clusterScopedJSON("ClusterRole", "workspace-controller-control-plane-dev-clusterrole", "dev")),
		unreadable("e2e"),
	})
	// e2e's OWN object is live. It is not an orphan — e2e renders it — but
	// e2e did not render, so nothing here can know that.
	probe := fakeProbe(map[string][]liveClusterObject{
		"k3d-control-plane": {
			liveObj("ClusterRole", "workspace-controller-control-plane-dev-clusterrole", "dev"),
			liveObj("ClusterRole", "workspace-controller-control-plane-e2e-clusterrole", "e2e"),
		},
	})

	got := checkOrphanedClusterObjects(context.Background(), env, probe)
	if got.Status != StatusUnknown {
		t.Fatalf("status = %q, want %q — e2e went unread, so its objects cannot be told from orphans\nmessage: %s",
			got.Status, StatusUnknown, got.Message)
	}
	if !strings.Contains(got.Message, "e2e") {
		t.Errorf("message does not name the unread env: %s", got.Message)
	}
	// The decisive assertion: e2e's live object must NOT appear. Listing it
	// under an "undetermined" heading is what gets it deleted.
	if strings.Contains(got.Evidence, "workspace-controller-control-plane-e2e-clusterrole") {
		t.Errorf("evidence lists an object e2e renders as though it were orphaned — a reader could act on it:\n%s",
			got.Evidence)
	}
}

// An object stamped by an env this project does not DECLARE is reported
// separately and never as an orphan. Another checkout, another branch, or a
// deleted env may own it; forge cannot render what it cannot see, so it
// cannot call the object unwanted. Measured live: k3d-control-plane holds
// `workspace-controller-operator-control-plane-dev-my-new-feature-50f77334`,
// put there by another agent's namespace.
func TestOrphanedClusterDoesNotClaimAnotherProjectsEnvStamp(t *testing.T) {
	env := envWithRender([]envRender{
		renderIn(t, "dev", "k3d-control-plane", clusterScopedJSON("ClusterRole", "keep-clusterrole", "dev")),
	})
	probe := fakeProbe(map[string][]liveClusterObject{
		"k3d-control-plane": {
			liveObj("ClusterRole", "keep-clusterrole", "dev"),
			liveObj("ClusterRoleBinding", "workspace-controller-operator-my-new-feature", "my-new-feature"),
		},
	})

	got := checkOrphanedClusterObjects(context.Background(), env, probe)
	if got.Status != StatusPass {
		t.Fatalf("status = %q, want %q — an env this project does not declare is not this project's orphan\n"+
			"message: %s\nevidence: %s", got.Status, StatusPass, got.Message, got.Evidence)
	}
	// Reported as context, not as a finding: worth seeing, not worth acting on.
	if !strings.Contains(got.Evidence, "my-new-feature") {
		t.Errorf("evidence should still SHOW the foreign-stamped object, as context:\n%s", got.Evidence)
	}
	if !strings.Contains(got.Evidence, "does not declare") {
		t.Errorf("evidence does not explain why it was not judged:\n%s", got.Evidence)
	}
}

// The same NAME on a DIFFERENT cluster is a different object. An env that
// renders `api-clusterrole` onto gke-prod does not excuse one left behind on
// k3d-control-plane — only one of the two is wanted, and cluster-scoped
// objects have no namespace to tell them apart.
func TestOrphanedClusterKeepsClustersApart(t *testing.T) {
	env := envWithRender([]envRender{
		renderIn(t, "prod", "gke-prod", clusterScopedJSON("ClusterRole", "api-clusterrole", "prod")),
		renderIn(t, "dev", "k3d-control-plane", clusterScopedJSON("ClusterRole", "dev-clusterrole", "dev")),
	})
	probe := fakeProbe(map[string][]liveClusterObject{
		"gke-prod":          {liveObj("ClusterRole", "api-clusterrole", "prod")},
		"k3d-control-plane": {liveObj("ClusterRole", "dev-clusterrole", "dev"), liveObj("ClusterRole", "api-clusterrole", "prod")},
	})

	got := checkOrphanedClusterObjects(context.Background(), env, probe)
	if got.Status != StatusWarn {
		t.Fatalf("status = %q, want %q — api-clusterrole on k3d-control-plane is rendered by nothing\nmessage: %s",
			got.Status, StatusWarn, got.Message)
	}
	if !strings.Contains(got.Evidence, "k3d-control-plane  ClusterRole/api-clusterrole") {
		t.Errorf("evidence does not name the cluster the orphan is actually on:\n%s", got.Evidence)
	}
	if strings.Contains(got.Evidence, "gke-prod") {
		t.Errorf("evidence implicates gke-prod, where prod renders that exact name:\n%s", got.Evidence)
	}
}

// A NAMESPACED object is not this check's subject. It has an owner that can
// collect it — `forge env deploy` prunes forge-managed Deployments the
// render no longer contains, and deleting the namespace takes the rest — so
// reporting one here would duplicate a finding that already has a home and
// dilute the list that does not.
func TestOrphanedClusterIgnoresNamespacedObjects(t *testing.T) {
	env := envWithRender([]envRender{
		renderIn(t, "dev", "k3d-control-plane", deployIn("api", "control-plane-dev", "api:1")),
	})
	// The probe only ever lists cluster-scoped kinds, so a namespaced
	// Deployment cannot reach the judgement at all.
	probe := fakeProbe(map[string][]liveClusterObject{"k3d-control-plane": nil})

	got := checkOrphanedClusterObjects(context.Background(), env, probe)
	if got.Status != StatusPass {
		t.Fatalf("status = %q, want %q — a namespaced Deployment is not orphaned cluster state\nmessage: %s",
			got.Status, StatusPass, got.Message)
	}
}

// A partial LISTING is a hole, never a clean answer. The kinds that failed
// are exactly where an unlisted orphan hides, so reporting the kinds that
// did answer as the whole picture is the false all-clear this check exists
// to prevent.
func TestOrphanedClusterPartialKindListingIsNotAPass(t *testing.T) {
	env := envWithRender([]envRender{
		renderIn(t, "dev", "k3d-control-plane", clusterScopedJSON("ClusterRole", "keep-clusterrole", "dev")),
	})
	probe := orphanProbe{
		contexts: func(context.Context) (map[string]bool, error) {
			return map[string]bool{"k3d-control-plane": true}, nil
		},
		list: func(_ context.Context, _, kind string) ([]liveClusterObject, error) {
			if kind == "clusterrolebinding" {
				return nil, errors.New("clusterrolebindings.rbac.authorization.k8s.io is forbidden")
			}
			if kind == "clusterrole" {
				return []liveClusterObject{liveObj("ClusterRole", "keep-clusterrole", "dev")}, nil
			}
			return nil, nil
		},
	}

	got := checkOrphanedClusterObjects(context.Background(), env, probe)
	if got.Status == StatusPass {
		t.Fatalf("status = pass — ClusterRoleBindings were never listed, and that is precisely the kind the "+
			"measured orphans were\nmessage: %s", got.Message)
	}
	if got.Status != StatusUnknown {
		t.Fatalf("status = %q, want %q\nmessage: %s", got.Status, StatusUnknown, got.Message)
	}
	if !strings.Contains(got.Evidence, "forbidden") {
		t.Errorf("evidence does not carry why the kind went unlisted:\n%s", got.Evidence)
	}
}

// An unreachable SECOND cluster must not bury a real finding on the first.
// The precedence here is Warn > Unknown, inverting CheckClusterWorkloads' —
// there the hole was the defect being closed; here the orphan list IS the
// deliverable, and demoting it to "undetermined" would restore the exact
// invisibility this check removes. The hole still rides on the one line the
// reader sees.
func TestOrphanedClusterReportsOrphansEvenWhenAnotherClusterIsUnreachable(t *testing.T) {
	env := envWithRender([]envRender{
		renderIn(t, "dev", "k3d-control-plane", clusterScopedJSON("ClusterRole", "keep-clusterrole", "dev")),
		renderIn(t, "prod", "gke-prod", clusterScopedJSON("ClusterRole", "api-clusterrole", "prod")),
	})
	probe := orphanProbe{
		contexts: func(context.Context) (map[string]bool, error) {
			return map[string]bool{"k3d-control-plane": true, "gke-prod": true}, nil
		},
		list: func(_ context.Context, kctx, kind string) ([]liveClusterObject, error) {
			if kctx == "gke-prod" {
				return nil, errors.New("Unable to connect to the server: dial tcp: i/o timeout")
			}
			if kind == "clusterrole" {
				return []liveClusterObject{
					liveObj("ClusterRole", "keep-clusterrole", "dev"),
					liveObj("ClusterRole", "workspace-controller-clusterrole", "dev"),
				}, nil
			}
			return nil, nil
		},
	}

	got := checkOrphanedClusterObjects(context.Background(), env, probe)
	if got.Status != StatusWarn {
		t.Fatalf("status = %q, want %q — k3d-control-plane holds a real orphan\nmessage: %s",
			got.Status, StatusWarn, got.Message)
	}
	if !strings.Contains(got.Evidence, "workspace-controller-clusterrole") {
		t.Errorf("evidence lost the orphan it did find:\n%s", got.Evidence)
	}
	// The hole must reach the one-line message, not only the evidence: "1
	// orphaned" while silently dropping "and prod never answered" is a
	// smaller copy of the invisibility being closed.
	if !strings.Contains(got.Message, "NOT INSPECTED") {
		t.Errorf("the one-line message hides that a cluster went uninspected: %s", got.Message)
	}
	if !strings.Contains(got.Evidence, "i/o timeout") {
		t.Errorf("evidence does not say why prod went uninspected:\n%s", got.Evidence)
	}
}

// A project declaring no environments has no cluster-scoped state to own —
// `--kind cli` / library projects must stay quiet.
func TestOrphanedClusterSkipsProjectWithNoClusters(t *testing.T) {
	env := envWithRender([]envRender{
		renderFromJSON(t, "dev", `{"output":{"manifests":[`+clusterScopedJSON("ClusterRole", "x", "dev")+`]}}`),
	})
	probe := fakeProbe(nil)

	got := checkOrphanedClusterObjects(context.Background(), env, probe)
	if got.Status != StatusSkip {
		t.Fatalf("status = %q, want %q — the render declares no cluster, so there is nowhere to be orphaned\nmessage: %s",
			got.Status, StatusSkip, got.Message)
	}
}

// kubectl prints "the server doesn't have a resource type" for an absent
// optional CRD and still EXITS 0 (verified against k3d-control-plane), so
// the success path must test stderr too. Without this, the empty body parses
// as "no objects" and a whole kind goes silently unlisted — a hole that
// reads as a pass.
func TestMissingResourceKindRecognisesBothKubectlSpellings(t *testing.T) {
	for _, stderr := range []string{
		`error: the server doesn't have a resource type "clusterissuer"`,
		`error: no matches for kind "ClusterIssuer" in version "cert-manager.io/v1"`,
	} {
		if !missingResourceKind(stderr) {
			t.Errorf("missingResourceKind(%q) = false — the kind would be reported as a hole instead of absent", stderr)
		}
	}
	if missingResourceKind("Error from server (Forbidden): clusterroles is forbidden") {
		t.Error("a Forbidden refusal was mistaken for an absent kind — it is a real hole and must not be silenced")
	}
}

// A check that is never registered never runs. This one belongs to the
// project set rather than deployabilityChecks(), which is contracted to
// answer on a bare checkout with nothing running.
func TestOrphanedClusterIsRegistered(t *testing.T) {
	for _, c := range projectChecks() {
		if c.name == orphanedClusterCheckName {
			for _, d := range deployabilityChecks() {
				if d.name == orphanedClusterCheckName {
					t.Fatalf("%q is in deployabilityChecks(), which must answer with nothing running — "+
						"this check reads a live cluster", orphanedClusterCheckName)
				}
			}
			return
		}
	}
	t.Fatalf("%q is not in projectChecks() — it would never run under `forge doctor`", orphanedClusterCheckName)
}
