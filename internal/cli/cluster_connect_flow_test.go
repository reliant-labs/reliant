package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateConnectGolden = flag.Bool("update-connect", false, "rewrite cluster connect --dry-run goldens")

// stubConnect replaces every cluster- and control-plane-contacting seam. The
// returned recorder holds what connect applied and which kubectl calls ran.
type connectStub struct {
	applied []string
	kubectl [][]string
	live    []liveObject
	caller  *fakeConnectCaller
}

func stubConnect(t *testing.T, kctx, stream string) *connectStub {
	t.Helper()
	s := &connectStub{caller: &fakeConnectCaller{}}
	rk, rc, ra, rt, rs, rl, rkc := kubeconfigClusterOf, clusterConnectClient, connectApply, connectReadToken, connectEnvStream, connectReadLive, connectKubectl
	t.Cleanup(func() {
		kubeconfigClusterOf, clusterConnectClient, connectApply, connectReadToken, connectEnvStream, connectReadLive, connectKubectl = rk, rc, ra, rt, rs, rl, rkc
	})
	kubeconfigClusterOf = func(string) (string, string, error) { return "https://34.1.2.3", "ca-bundle", nil }
	clusterConnectClient = func(context.Context, string, string) (cloudCaller, string, error) { return s.caller, "acme", nil }
	connectApply = func(_ context.Context, _ string, m string) error { s.applied = append(s.applied, m); return nil }
	connectReadToken = func(context.Context, string, connectRBAC) (string, error) { return fakeMintedToken, nil }
	connectEnvStream = func(context.Context, io.Writer, string) (string, error) { return stream, nil }
	connectReadLive = func(context.Context, string, connectRBAC) ([]liveObject, error) { return s.live, nil }
	connectKubectl = func(_ context.Context, _ string, args ...string) (string, error) {
		s.kubectl = append(s.kubectl, args)
		return "", nil
	}
	return s
}

func TestConnect_GCPBindsTheBindingsOwnGSAAfterRegistering(t *testing.T) {
	s := stubConnect(t, testCtx, scopeStream())
	var out strings.Builder
	if err := runClusterConnect(context.Background(), clusterConnectOptions{
		Name: "prod-us", KubeContext: testCtx, Auth: "auto", Env: "prod", Out: &out,
	}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if len(s.applied) != 1 {
		t.Fatalf("applied %d stages, want 1", len(s.applied))
	}
	grant := s.applied[0]
	if !strings.Contains(grant, "name: fc-prod-ab12@acme.iam.gserviceaccount.com") {
		t.Errorf("the grant does not bind the binding's own GSA:\n%s", grant)
	}
	for _, banned := range []string{"flux-source@", "impersonate", "control-plane@"} {
		if strings.Contains(grant, banned) {
			t.Errorf("the grant contains %q:\n%s", banned, grant)
		}
	}
	req := s.caller.requests[procConnectCluster]
	if req["environment"] != "prod" {
		t.Errorf("the connect request does not name the env the binding is for: %v", req)
	}
	for _, want := range []string{"--member=serviceAccount:fc-prod-ab12@acme.iam.gserviceaccount.com", "roles/container.clusterViewer", "binding namespace:acme-prod"} {
		if want != "binding namespace:acme-prod" && !strings.Contains(out.String(), want) {
			t.Errorf("output omits %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "cluster-admin") {
		t.Errorf("a namespace-scope connect printed the cluster-admin warning:\n%s", out.String())
	}
}

// With no per-binding identity from the control plane, bind NOTHING: falling
// back to a shared identity is exactly what this design removes.
func TestConnect_GCPWithNoBindingIdentityBindsNothing(t *testing.T) {
	s := stubConnect(t, testCtx, scopeStream())
	s.caller.noBindingIdentity = true
	var out strings.Builder
	if err := runClusterConnect(context.Background(), clusterConnectOptions{
		Name: "prod-us", KubeContext: testCtx, Auth: "gcp", Env: "prod", Out: &out,
	}); err != nil {
		t.Fatal(err)
	}
	if len(s.applied) != 0 {
		t.Errorf("applied %d stages with no principal to bind", len(s.applied))
	}
	if strings.Contains(out.String(), "gcloud") || !strings.Contains(out.String(), "no per-binding GCP identity") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestConnect_TokenAppliesBeforeRegisteringAndNeverPrintsTheToken(t *testing.T) {
	s := stubConnect(t, "vke-prod", strings.ReplaceAll(scopeStream(), testCtx, "vke-prod"))
	var out strings.Builder
	if err := runClusterConnect(context.Background(), clusterConnectOptions{
		Name: "vke", KubeContext: "vke-prod", Auth: "auto", Env: "prod", Out: &out,
	}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if s.caller.requests[procConnectCluster]["token"] != fakeMintedToken {
		t.Error("the minted token was not sent")
	}
	if strings.Contains(out.String(), fakeMintedToken) {
		t.Error("the token was printed")
	}
	if !strings.Contains(s.applied[0], "name: forge-connect-vke-prod") || !strings.Contains(s.applied[0], "kind: Secret") {
		t.Errorf("the token identity was not minted per binding:\n%s", s.applied[0])
	}
}

func TestConnect_ClusterScopePrintsTheClusterAdminWarning(t *testing.T) {
	stream := scopeStream(onCtx(docClusterRole))
	s := stubConnect(t, testCtx, stream)
	var out strings.Builder
	if err := runClusterConnect(context.Background(), clusterConnectOptions{
		Name: "prod-us", KubeContext: testCtx, Auth: "gcp", Env: "prod", Scope: applyScope{Cluster: true}, Out: &out,
	}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if n := strings.Count(out.String(), "equivalent to cluster-admin"); n < 1 {
		t.Errorf("cluster scope did not say what it is:\n%s", out.String())
	}
	if !strings.Contains(s.applied[0], "kind: ClusterRoleBinding") || strings.Contains(s.applied[0], "kind: Role\n") {
		t.Errorf("cluster scope applied:\n%s", s.applied[0])
	}
}

func TestConnect_RefusesBeforeWritingAnythingWhenTheScopeDoesNotFit(t *testing.T) {
	s := stubConnect(t, testCtx, scopeStream(onCtx(docClusterRole)))
	err := runClusterConnect(context.Background(), clusterConnectOptions{
		Name: "prod-us", KubeContext: testCtx, Auth: "gcp", Env: "prod", Out: io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "namespace scope") || !strings.Contains(err.Error(), "--apply-scope cluster") {
		t.Fatalf("err = %v, want a namespace-scope refusal naming the cluster scope", err)
	}
	if len(s.applied) != 0 || len(s.caller.requests) != 0 {
		t.Errorf("a refused connect wrote: applied=%d requests=%v", len(s.applied), s.caller.requests)
	}
}

func TestConnect_RequiresAnEnv(t *testing.T) {
	stubConnect(t, testCtx, scopeStream())
	err := runClusterConnect(context.Background(), clusterConnectOptions{Name: "c", KubeContext: testCtx, Auth: "gcp", Out: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "--env is required") {
		t.Fatalf("err = %v", err)
	}
}

// A re-run with a narrower scope narrows the cluster: stale objects are
// pruned, by exact name, and nothing outside this binding's labels.
func TestConnect_NarrowerRerunPrunesStaleGrantObjects(t *testing.T) {
	s := stubConnect(t, testCtx, scopeStream())
	s.live = []liveObject{
		{Kind: "ClusterRole", Name: "forge-connect-prod-us-prod"},
		{Kind: "ClusterRoleBinding", Name: "forge-connect-prod-us-prod"},
		{Kind: "Role", Namespace: "acme-prod", Name: "forge-connect-prod-us-prod"},
		{Kind: "Role", Namespace: "acme-prod", Name: "delivery-keeper", Delivery: true},
	}
	var out strings.Builder
	if err := runClusterConnect(context.Background(), clusterConnectOptions{
		Name: "prod-us", KubeContext: testCtx, Auth: "gcp", Env: "prod", Out: &out,
	}); err != nil {
		t.Fatal(err)
	}
	var deletes []string
	for _, a := range s.kubectl {
		if len(a) > 0 && a[0] == "delete" && len(a) > 2 && !strings.Contains(strings.Join(a, " "), "-l") {
			deletes = append(deletes, strings.Join(a, " "))
		}
	}
	// The namespace plan still carries a ClusterRole (the pinned Namespace), so
	// only the unnamed-by-the-plan objects go; the delivery Role is never pruned.
	for _, d := range deletes {
		if strings.Contains(d, "delivery-keeper") {
			t.Errorf("connect pruned a delivery grant: %s", d)
		}
	}
}

func TestConnect_PrunesTheLegacySharedGrant(t *testing.T) {
	s := stubConnect(t, testCtx, scopeStream())
	connectKubectl = func(_ context.Context, _ string, args ...string) (string, error) {
		s.kubectl = append(s.kubectl, args)
		if len(args) > 0 && args[0] == "get" && strings.Contains(strings.Join(args, " "), "!"+connectBindingLabelKey) {
			return "clusterrole.rbac.authorization.k8s.io/forge-connect-prod-us\n", nil
		}
		return "", nil
	}
	var out strings.Builder
	if err := runClusterConnect(context.Background(), clusterConnectOptions{
		Name: "prod-us", KubeContext: testCtx, Auth: "gcp", Env: "prod", Out: &out,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "removed the legacy grant for prod-us") {
		t.Errorf("the legacy impersonating grant was left behind:\n%s", out.String())
	}
}

func TestConnect_DryRunOnlyFlagsAreRefusedOnARealRun(t *testing.T) {
	stubConnect(t, testCtx, scopeStream())
	err := runClusterConnect(context.Background(), clusterConnectOptions{
		Name: "c", KubeContext: testCtx, Auth: "gcp", Env: "prod", LiveFrom: "x.yaml", Out: io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "--dry-run only") {
		t.Fatalf("err = %v", err)
	}
}

func TestConnectCmd_FlagsAreRegisteredAndValidated(t *testing.T) {
	cmd := newClusterConnectCmd()
	for _, f := range []string{"apply-scope", "dry-run", "live-from", "binding-gsa", "delivery-secret", "delivery-subject", "env", "context", "auth"} {
		if cmd.Flags().Lookup(f) == nil {
			t.Errorf("--%s is not registered", f)
		}
	}
	for _, args := range [][]string{
		{"c", "--context", "x", "--env", "e", "--apply-scope", "everything"},
		{"c", "--context", "x", "--env", "e", "--delivery-secret", "bad name"},
	} {
		cmd := newClusterConnectCmd()
		cmd.SetArgs(args)
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		if err := cmd.Execute(); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
}

func TestConnect_DeliveryAppliesPolicyBeforeRolesAndStopsOnFailure(t *testing.T) {
	s := stubConnect(t, "vke-prod", strings.ReplaceAll(scopeStream(), testCtx, "vke-prod"))
	connectApply = func(_ context.Context, _ string, m string) error {
		s.applied = append(s.applied, m)
		if strings.Contains(m, "ValidatingAdmissionPolicy") {
			return fmt.Errorf("no matches for kind ValidatingAdmissionPolicy")
		}
		return nil
	}
	secrets, _ := parseDeliverySecrets([]string{"ns/a"})
	err := runClusterConnect(context.Background(), clusterConnectOptions{
		Name: "vke", KubeContext: "vke-prod", Auth: "token", Env: "prod", DeliverySecrets: secrets, Out: io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "ValidatingAdmissionPolicy") {
		t.Fatalf("err = %v, want the policy failure surfaced", err)
	}
	if len(s.applied) != 2 {
		t.Fatalf("applied %d stages, want grant then the failed policy and nothing after", len(s.applied))
	}
	if strings.Contains(s.applied[1], "kind: Role\n") {
		t.Error("the create grant was applied although its policy failed")
	}
}

// ── disconnect ──────────────────────────────────────────────────────────────

func TestDisconnect_RevokesFirstThenDeletesEverythingByLabel(t *testing.T) {
	s := stubConnect(t, testCtx, scopeStream())
	s.caller.clusters = []wireConnectedCluster{{ID: "cl_1", Name: "prod-us"}}
	var out strings.Builder
	if err := runClusterDisconnect(context.Background(), "prod-us", testCtx, "prod", "", &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if _, ok := s.caller.requests[procRemoveCluster]; !ok {
		t.Fatal("RemoveCluster was never called")
	}
	sel := forgeManagedLabelKey + "=" + forgeManagedLabelValue + "," + connectClusterLabelKey + "=prod-us"
	var joined []string
	for _, a := range s.kubectl {
		joined = append(joined, strings.Join(a, " "))
	}
	all := strings.Join(joined, "\n")
	for _, want := range []string{
		"delete validatingadmissionpolicybinding,validatingadmissionpolicy -l " + sel,
		"delete rolebinding,role --all-namespaces -l " + sel,
		"delete clusterrolebinding,clusterrole -l " + sel,
		"delete secret,serviceaccount --all-namespaces -l " + sel,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("disconnect omits %q:\n%s", want, all)
		}
	}
	// The policy goes first (it only narrows); nothing deletes a namespace.
	if !strings.HasPrefix(joined[0], "delete validatingadmissionpolicybinding") {
		t.Errorf("first delete = %q", joined[0])
	}
	if strings.Contains(all, "namespace ") && strings.Contains(all, "delete namespace") {
		t.Errorf("disconnect deletes a namespace:\n%s", all)
	}
	if !strings.Contains(out.String(), "namespaces were left in place") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestDisconnect_WithoutContextPrintsTheCommands(t *testing.T) {
	s := stubConnect(t, testCtx, scopeStream())
	s.caller.clusters = []wireConnectedCluster{{ID: "cl_1", Name: "prod-us"}}
	var out strings.Builder
	if err := runClusterDisconnect(context.Background(), "prod-us", "", "prod", "", &out); err != nil {
		t.Fatal(err)
	}
	if len(s.kubectl) != 0 {
		t.Errorf("kubectl ran with no --context: %v", s.kubectl)
	}
	if !strings.Contains(out.String(), "kubectl --context <ctx> delete clusterrolebinding,clusterrole -l ") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestDisconnect_ToleratesAClusterWithoutAdmissionPolicies(t *testing.T) {
	s := stubConnect(t, testCtx, scopeStream())
	s.caller.clusters = []wireConnectedCluster{{ID: "cl_1", Name: "prod-us"}}
	connectKubectl = func(_ context.Context, _ string, args ...string) (string, error) {
		s.kubectl = append(s.kubectl, args)
		if strings.Contains(args[1], "validatingadmissionpolicy") {
			return "", fmt.Errorf("the server doesn't have a resource type \"validatingadmissionpolicies\"")
		}
		return "", nil
	}
	if err := runClusterDisconnect(context.Background(), "prod-us", testCtx, "prod", "", io.Discard); err != nil {
		t.Fatalf("a cluster too old for admission policies failed disconnect: %v", err)
	}
	if len(s.kubectl) != 4 {
		t.Errorf("ran %d deletes, want all 4 (the rest must still run)", len(s.kubectl))
	}
}

func TestDisconnect_RefusesAnUnknownClusterAndDeletesNothing(t *testing.T) {
	s := stubConnect(t, testCtx, scopeStream())
	s.caller.clusters = []wireConnectedCluster{{ID: "cl_1", Name: "prod-us-2"}}
	if err := runClusterDisconnect(context.Background(), "prod-us", testCtx, "prod", "", io.Discard); err == nil {
		t.Fatal("a partial name disconnected a cluster")
	}
	if len(s.kubectl) != 0 {
		t.Errorf("kubectl ran: %v", s.kubectl)
	}
}

// ── dry-run diff ────────────────────────────────────────────────────────────

func dryRunOutput(t *testing.T, kctx, auth string, scope applyScope, stream, livePath string) string {
	t.Helper()
	s := stubConnect(t, kctx, stream)
	var out strings.Builder
	opts := clusterConnectOptions{
		Name: "prod-us", KubeContext: kctx, Auth: auth, Env: "prod", DryRun: true, Scope: scope, Out: &out,
		LiveFrom: livePath, BindingGSA: "fc-prod-ab12@acme.iam.gserviceaccount.com",
	}
	if auth == "token" {
		opts.BindingGSA = ""
	}
	if err := runClusterConnect(context.Background(), opts); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if len(s.applied) != 0 || len(s.caller.requests) != 0 || len(s.kubectl) != 0 {
		t.Fatalf("--dry-run wrote or contacted something: applied=%d requests=%v kubectl=%v", len(s.applied), s.caller.requests, s.kubectl)
	}
	return out.String()
}

func assertConnectGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "cluster_connect", name+".golden")
	if *updateConnectGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v\nrun: go test ./internal/cli -run %s -update-connect", err, t.Name())
	}
	if got != string(want) {
		t.Errorf("%s differs from the golden (go test ./internal/cli -run %s -update-connect):\n--- got ---\n%s\n--- want ---\n%s", name, t.Name(), got, want)
	}
}

func writeLive(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "live.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDryRun_FreshClusterGolden_NamespaceScope(t *testing.T) {
	live := writeLive(t, "apiVersion: v1\nkind: List\nitems: []\n")
	got := dryRunOutput(t, testCtx, "gcp", applyScope{Namespaces: []string{"acme-prod"}}, scopeStream(), live)
	assertConnectGolden(t, "dryrun_fresh_namespace", strings.ReplaceAll(got, live, "<live>"))
}

func TestDryRun_FreshClusterGolden_ClusterScope(t *testing.T) {
	live := writeLive(t, "apiVersion: v1\nkind: List\nitems: []\n")
	got := dryRunOutput(t, testCtx, "gcp", applyScope{Cluster: true}, scopeStream(onCtx(docClusterRole), onCtx(docRoleBinding)), live)
	assertConnectGolden(t, "dryrun_fresh_cluster", strings.ReplaceAll(got, live, "<live>"))
}

// The review the owner does before widening the prod grant: a namespace grant
// is live, a new release adds a CRD kind, and the owner re-runs with cluster
// scope. The diff names exactly what changes.
func TestDryRun_WideningGolden(t *testing.T) {
	// Capture the namespace-scope grant, as the cluster holds it.
	narrow := testRBAC(t, authGCP, applyScope{Namespaces: []string{"acme-prod"}})
	narrow.IssuerJWKS = true
	var live strings.Builder
	live.WriteString("apiVersion: v1\nkind: List\nitems:\n")
	for _, o := range narrow.grantObjects() {
		live.WriteString("- " + strings.ReplaceAll(strings.TrimRight(o.yaml(), "\n"), "\n", "\n  ") + "\n")
	}
	path := writeLive(t, live.String())

	// Unchanged scope and render: every object is "=" and nothing is newly granted.
	same := dryRunOutput(t, testCtx, "gcp", applyScope{Namespaces: []string{"acme-prod"}}, scopeStream(), path)
	if !strings.Contains(same, "none: this run grants nothing the cluster does not already grant") || strings.Contains(same, "\n+ ") || strings.Contains(same, "\n~ ") {
		t.Errorf("an unchanged re-run reports a change:\n%s", same)
	}
	assertConnectGolden(t, "dryrun_unchanged", strings.ReplaceAll(same, path, "<live>"))

	// A release that adds a kind: the namespace Role gains exactly that rule.
	cron := onCtx("apiVersion: batch/v1\nkind: CronJob\nmetadata:\n  name: sweep\n  namespace: acme-prod\n")
	grown := dryRunOutput(t, testCtx, "gcp", applyScope{Namespaces: []string{"acme-prod"}}, scopeStream(cron), path)
	if !strings.Contains(grown, "namespace acme-prod: create batch/cronjobs") || strings.Contains(grown, "namespace acme-prod: create apps/deployments") {
		t.Errorf("the new kind, and only it, should be named as newly granted:\n%s", grown)
	}
	assertConnectGolden(t, "dryrun_new_kind", strings.ReplaceAll(grown, path, "<live>"))

	// The owner widens to cluster scope: the namespace Role goes, a ClusterRole
	// arrives, and everything the cluster did not grant before is listed.
	wide := dryRunOutput(t, testCtx, "gcp", applyScope{Cluster: true}, scopeStream(cron), path)
	if !strings.Contains(wide, "- Role acme-prod/forge-connect-prod-us-prod  removed (connect prunes it)") ||
		!strings.Contains(wide, "~ ClusterRole forge-connect-prod-us-prod  changed") ||
		!strings.Contains(wide, "cluster: create apps/deployments") ||
		!strings.Contains(wide, "- rule     core/namespaces: get,patch,update  only acme-prod") {
		t.Errorf("widening to cluster scope is not reviewable from the diff:\n%s", wide)
	}
	assertConnectGolden(t, "dryrun_widen_to_cluster", strings.ReplaceAll(wide, path, "<live>"))
}

func TestDryRun_ReportsStaleObjectsItWouldPrune(t *testing.T) {
	live := writeLive(t, "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRoleBinding\nmetadata:\n  name: old-binding\n"+
		"roleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: ClusterRole\n  name: old\nsubjects: []\n")
	got := dryRunOutput(t, testCtx, "gcp", applyScope{Namespaces: []string{"acme-prod"}}, scopeStream(), live)
	if !strings.Contains(got, "- ClusterRoleBinding old-binding  removed (connect prunes it)") {
		t.Errorf("the stale object is not reported:\n%s", got)
	}
}

func TestDryRun_ReadsTheClusterThroughTheContextWhenNoCaptureIsGiven(t *testing.T) {
	s := stubConnect(t, testCtx, scopeStream())
	s.live = nil
	var out strings.Builder
	if err := runClusterConnect(context.Background(), clusterConnectOptions{
		Name: "prod-us", KubeContext: testCtx, Auth: "gcp", Env: "prod", DryRun: true, Out: &out,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "live       context "+testCtx+" (read-only)") {
		t.Errorf("output:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "selector     app.kubernetes.io/managed-by=forge,forge.dev/connected-cluster=prod-us,forge.dev/binding-env=prod") {
		t.Errorf("the capture selector is not printed:\n%s", out.String())
	}
}

func TestDryRun_BindingGSAIsGCPOnly(t *testing.T) {
	stubConnect(t, "vke-prod", strings.ReplaceAll(scopeStream(), testCtx, "vke-prod"))
	err := runClusterConnect(context.Background(), clusterConnectOptions{
		Name: "c", KubeContext: "vke-prod", Auth: "token", Env: "prod", DryRun: true, BindingGSA: "x@y", Out: io.Discard,
		LiveFrom: writeLive(t, "kind: List\nitems: []\n"),
	})
	if err == nil || !strings.Contains(err.Error(), "--binding-gsa is for --auth gcp") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseLive_AcceptsListsStreamsAndIgnoresOtherKinds(t *testing.T) {
	got, err := parseLive([]byte("apiVersion: v1\nkind: List\nitems:\n- kind: ConfigMap\n  metadata: {name: x}\n- kind: Role\n  metadata: {name: r, namespace: a}\n  rules: []\n---\nkind: ClusterRole\nmetadata: {name: c}\nrules: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Kind != "ClusterRole" || got[1].Kind != "Role" {
		t.Errorf("got %+v", got)
	}
}

func TestLineDiff(t *testing.T) {
	got := strings.Join(lineDiff([]string{"a", "b", "c"}, []string{"a", "x", "c", "d"}), "|")
	if got != "  a|- b|+ x|  c|+ d" {
		t.Errorf("lineDiff = %q", got)
	}
}
