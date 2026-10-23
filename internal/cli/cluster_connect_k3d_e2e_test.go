//go:build e2e

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestE2EClusterConnect_TokenAgainstARealCluster drives `connect --auth token`
// and `disconnect` against a REAL throwaway k3d cluster and a fake control
// plane, for BOTH apply scopes and TWO bindings of one cluster: the binding's
// ServiceAccount, its token Secret and the render-derived RBAC are created for
// real, the minted token is read back and sent write-only, and the grant is
// then EXERCISED AS THAT IDENTITY — what the render applies is allowed, a kind
// it did not contain is Forbidden (the tripwire), and namespace scope stops at
// its namespaces. Disconnect removes exactly what forge created, by label.
//
// RUN IT ON A DEVELOPER MACHINE (k3d, kubectl and a docker daemon; it is NOT
// run in the kata sandbox, where nested clusters are impossible):
//
//	go test -tags e2e -count=1 -timeout 15m -run TestE2EClusterConnect_TokenAgainstARealCluster ./internal/cli/
//
// WHAT IS STUBBED, AND WHY ONLY THAT. A k3d API server is loopback, which the
// command refuses on purpose (the hub dials from a pod, where loopback is that
// pod). So the seams replaced are the kubeconfig ADDRESS read and the env's
// render (a fixed stream, since there is no project here); everything the test
// exists to prove — the apply, the asynchronous token population, the
// write-only send, the grant as the identity experiences it, the by-label
// cleanup — runs for real.
//
// THE e2e TIER, because it needs what only that tier provisions: a live
// docker daemon with the capacity to boot a k3d cluster (and pull its image).
// A missing tool or daemon is a named skip on a laptop and a hard failure in
// CI, where e2e-suite.yml installs k3d and checks the daemon.
//
// The cluster is named uniquely and deleted by that exact name; no other
// cluster is read or touched, and the developer's kubeconfig is never
// modified.
func TestE2EClusterConnect_TokenAgainstARealCluster(t *testing.T) {
	if testing.Short() {
		t.Skip("creates a real k3d cluster")
	}
	requireTool(t, "k3d", "kubectl")
	requireDockerDaemon(t)

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	name := fmt.Sprintf("fcc-%d", time.Now().UnixNano()%1_000_000_000)
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	kctx := "k3d-" + name

	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		// By exact name. Best-effort, but a leak is reported. Registered BEFORE
		// the create, so a create that hangs or half-succeeds is still cleaned up.
		// Under forge's kubeconfig lock: the delete rewrites the default
		// kubeconfig (kubeconfig_lock.go).
		_ = withKubeconfigLock(func() error {
			if out, err := exec.CommandContext(cleanup, "k3d", "cluster", "delete", name).CombinedOutput(); err != nil {
				t.Logf("cleanup: k3d cluster delete %s: %v\n%s", name, err, out)
			}
			return nil
		})
	})

	// A sandbox without nested networking (kata, gVisor) boots the k3d
	// containers but resets every connection to their API server, which makes
	// `--wait` hang for its whole timeout. Bound the create and PROBE the API,
	// so that environment is a named skip here and a hard failure in CI.
	createCtx, cancelCreate := context.WithTimeout(ctx, 150*time.Second)
	defer cancelCreate()
	createErr := exec.CommandContext(createCtx, "k3d", "cluster", "create", name,
		"--servers", "1", "--agents", "0", "--no-lb",
		"--wait", "--timeout", "120s",
		"--kubeconfig-update-default=false", "--kubeconfig-switch-context=false").Run()
	if createErr == nil {
		createErr = exec.CommandContext(createCtx, "k3d", "kubeconfig", "write", name,
			"--output", kubeconfig, "--overwrite", "--kubeconfig-switch-context=false").Run()
	}
	if createErr == nil {
		createErr = exec.CommandContext(createCtx, "kubectl", "--kubeconfig", kubeconfig, "--context", kctx,
			"--request-timeout=20s", "get", "--raw", "/readyz").Run()
	}
	if createErr != nil {
		if e2eToolsRequired() {
			t.Fatalf("a k3d cluster did not come up with a reachable API server (%v)", createErr)
		}
		t.Skipf("skipped: a k3d cluster did not come up with a reachable API server (%v). This sandbox has no nested "+
			"cluster networking. It is a HARD FAILURE in CI. Run it on a developer machine with k3d, kubectl and docker:\n"+
			"    go test -tags e2e -count=1 -timeout 15m -run TestE2EClusterConnect_TokenAgainstARealCluster ./internal/cli/", createErr)
	}

	// A private kubeconfig for every kubectl forge shells out to.
	t.Setenv("KUBECONFIG", kubeconfig)

	restoreKube, restoreClient, restoreStream := kubeconfigClusterOf, clusterConnectClient, connectEnvStream
	t.Cleanup(func() {
		kubeconfigClusterOf, clusterConnectClient, connectEnvStream = restoreKube, restoreClient, restoreStream
	})
	kubeconfigClusterOf = func(string) (string, string, error) {
		return "https://203.0.113.10:6443", "ca-bundle", nil
	}
	caller := &fakeConnectCaller{}
	clusterConnectClient = func(context.Context, string, string) (cloudCaller, string, error) {
		return caller, "acme", nil
	}

	// The owner's namespaces exist before connect: RBAC cannot pin create.
	const appNS, otherNS, deliveryNS = "fcc-app", "fcc-other", "fcc-delivery"
	for _, ns := range []string{appNS, otherNS, deliveryNS} {
		runCluster(ctx, t, nil, "kubectl", "--context", kctx, "create", "namespace", ns)
	}
	render := func(extra string) string {
		return "# cluster: " + kctx + "\napiVersion: v1\nkind: Namespace\nmetadata:\n  name: " + appNS + "\n---\n" +
			"# cluster: " + kctx + "\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n  namespace: " + appNS + "\n" + extra
	}
	connectEnvStream = func(context.Context, io.Writer, string) (string, error) { return render(""), nil }

	deliverySecrets, err := parseDeliverySecrets([]string{deliveryNS + "/reliant-otel-auth"})
	if err != nil {
		t.Fatal(err)
	}
	connect := func(env string, scope applyScope, secrets []deliverySecret) string {
		t.Helper()
		var out strings.Builder
		if err := runClusterConnect(ctx, clusterConnectOptions{
			Name: "e2e", KubeContext: kctx, Auth: "token", Env: env, Scope: scope, Out: &out, DeliverySecrets: secrets,
		}); err != nil {
			t.Fatalf("connect %s: %v\n%s", env, err, out.String())
		}
		return out.String()
	}
	asBinding := func(env string) string {
		return "--as=system:serviceaccount:" + connectTokenNamespace + ":" + connectBindingName("e2e", env)
	}
	canI := func(env string, verb, res, ns string) bool {
		args := []string{"--context", kctx, asBinding(env), "auth", "can-i", verb, res}
		if ns != "" {
			args = append(args, "-n", ns)
		} else {
			args = append(args, "--all-namespaces")
		}
		return tryCluster(ctx, "kubectl", args...) == nil
	}

	// ── namespace scope: the default ──
	out := connect("prod", applyScope{Namespaces: []string{appNS}}, deliverySecrets)
	for _, obj := range [][]string{
		{"get", "namespace", connectTokenNamespace},
		{"get", "serviceaccount", connectBindingName("e2e", "prod"), "-n", connectTokenNamespace},
		{"get", "secret", connectBindingName("e2e", "prod") + "-token", "-n", connectTokenNamespace},
		{"get", "role", connectBindingName("e2e", "prod"), "-n", appNS},
		{"get", "rolebinding", connectBindingName("e2e", "prod"), "-n", appNS},
	} {
		runCluster(ctx, t, nil, "kubectl", append([]string{"--context", kctx}, obj...)...)
	}
	if strings.Contains(out, "cluster-admin") {
		t.Errorf("a namespace-scope connect printed the cluster-admin warning:\n%s", out)
	}
	for _, tc := range []struct {
		verb, res, ns string
		want          bool
	}{
		{"create", "configmaps", appNS, true},
		{"patch", "configmaps", appNS, true},
		{"create", "configmaps", otherNS, false},     // namespace scope stops at its namespaces
		{"create", "deployments.apps", appNS, false}, // the TRIPWIRE: a kind the render did not contain
		{"get", "secrets", appNS, false},             // Secrets never ride a bundle
		{"create", "clusterroles.rbac.authorization.k8s.io", "", false},
		// The rendered Namespace is pinned by resourceNames, so the question
		// must NAME it: RBAC never matches a pinned rule to a nameless request,
		// and `can-i patch namespaces` is "no" by construction.
		{"patch", "namespaces/" + appNS, "", true},
		{"patch", "namespaces/" + otherNS, "", false}, // the pin stops at the rendered name
		{"delete", "namespaces/" + appNS, "", false},  // a prune can never remove one
		{"impersonate", "serviceaccounts", "", false}, // no impersonation, ever
	} {
		if got := canI("prod", tc.verb, tc.res, tc.ns); got != tc.want {
			t.Errorf("namespace scope: can-i %s %s %q = %v, want %v", tc.verb, tc.res, tc.ns, got, tc.want)
		}
	}
	verifyDeliveryGrantAs(ctx, t, kctx, deliveryNS, asBinding("prod"))

	// The token the control plane received is the one the cluster minted.
	sent, _ := caller.requests[procConnectCluster]["token"].(string)
	if !strings.HasPrefix(sent, "eyJ") {
		t.Fatalf("the token sent to the control plane is not a service-account JWT: %q", sent)
	}
	if strings.Contains(out, sent) {
		t.Fatal("the minted token appears in the command's output")
	}

	// ── a second binding of the SAME cluster is a different identity ──
	connect("staging", applyScope{Namespaces: []string{appNS}}, nil)
	if tryCluster(ctx, "kubectl", "--context", kctx, "get", "serviceaccount", connectBindingName("e2e", "staging"), "-n", connectTokenNamespace) != nil {
		t.Error("the second binding has no ServiceAccount of its own")
	}

	// ── re-run is an idempotent upsert, and a widened release is Forbidden until re-connected ──
	connect("prod", applyScope{Namespaces: []string{appNS}}, deliverySecrets)
	connectEnvStream = func(context.Context, io.Writer, string) (string, error) {
		return render("---\n# cluster: " + kctx + "\napiVersion: v1\nkind: Service\nmetadata:\n  name: svc\n  namespace: " + appNS + "\n"), nil
	}
	if canI("prod", "create", "services", appNS) {
		t.Error("a new kind was allowed before the owner re-ran connect")
	}
	connect("prod", applyScope{Namespaces: []string{appNS}}, deliverySecrets)
	if !canI("prod", "create", "services", appNS) {
		t.Error("the new kind is still Forbidden after re-running connect")
	}

	// ── cluster scope: a ClusterRole derived from the render, bind/escalate on RBAC ──
	connectEnvStream = func(context.Context, io.Writer, string) (string, error) {
		return render("---\n# cluster: " + kctx + "\napiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: fcc-reader\nrules: []\n"), nil
	}
	out = connect("prod", applyScope{Cluster: true}, nil)
	if !strings.Contains(out, "equivalent to cluster-admin") {
		t.Errorf("cluster scope did not say what it is:\n%s", out)
	}
	for _, tc := range []struct {
		verb, res, ns string
		want          bool
	}{
		{"create", "configmaps", otherNS, true},
		{"create", "clusterroles.rbac.authorization.k8s.io", "", true},
		{"escalate", "clusterroles.rbac.authorization.k8s.io", "", true},
		{"create", "deployments.apps", appNS, false}, // still derived from the render
		{"impersonate", "serviceaccounts", "", false},
	} {
		if got := canI("prod", tc.verb, tc.res, tc.ns); got != tc.want {
			t.Errorf("cluster scope: can-i %s %s %q = %v, want %v", tc.verb, tc.res, tc.ns, got, tc.want)
		}
	}
	// Widening pruned the namespace Role: the cluster narrowed where the plan did.
	if tryCluster(ctx, "kubectl", "--context", kctx, "get", "role", connectBindingName("e2e", "prod"), "-n", appNS) == nil {
		t.Error("the namespace Role survived a re-run at cluster scope")
	}

	// ── disconnect: revoke, then delete by label ──
	caller.clusters = []wireConnectedCluster{{ID: "cl_new", Name: "e2e"}}
	out = ""
	var dout strings.Builder
	if err := runClusterDisconnect(ctx, "e2e", kctx, "prod", "", &dout); err != nil {
		t.Fatalf("disconnect: %v\n%s", err, dout.String())
	}
	if _, ok := caller.requests[procRemoveCluster]; !ok {
		t.Error("disconnect never called RemoveCluster")
	}
	for _, env := range []string{"prod", "staging"} {
		for _, obj := range [][]string{
			{"get", "serviceaccount", connectBindingName("e2e", env), "-n", connectTokenNamespace},
			{"get", "secret", connectBindingName("e2e", env) + "-token", "-n", connectTokenNamespace},
			{"get", "clusterrole", connectBindingName("e2e", env)},
			{"get", "clusterrolebinding", connectBindingName("e2e", env)},
			{"get", "role", connectBindingName("e2e", env), "-n", appNS},
			{"get", "rolebinding", connectBindingName("e2e", env), "-n", appNS},
			{"get", "validatingadmissionpolicy", deliveryObjectName("e2e", env)},
			{"get", "role", deliveryObjectName("e2e", env), "-n", deliveryNS},
		} {
			if err := tryCluster(ctx, "kubectl", append([]string{"--context", kctx}, obj...)...); err == nil {
				t.Errorf("disconnect left %s behind", strings.Join(obj[1:], " "))
			}
		}
	}
	// The shared namespaces are deliberately left in place.
	runCluster(ctx, t, nil, "kubectl", "--context", kctx, "get", "namespace", connectTokenNamespace)
	runCluster(ctx, t, nil, "kubectl", "--context", kctx, "get", "namespace", appNS)

	// A second disconnect converges rather than failing on missing objects.
	dout.Reset()
	if err := runClusterDisconnect(ctx, "e2e", kctx, "prod", "", &dout); err != nil {
		t.Fatalf("a repeated disconnect failed: %v\n%s", err, dout.String())
	}

	if _, err := os.Stat(kubeconfig); err != nil {
		t.Errorf("private kubeconfig vanished: %v", err)
	}
}

func runCluster(ctx context.Context, t *testing.T, env []string, name string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

func tryCluster(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

// verifyDeliveryGrant exercises the grant as the connect identity would, by
// impersonating it: RBAC through `auth can-i`, and the admission policy by
// attempting real writes. The policy is enforced asynchronously after its
// binding appears, so the first denial is polled for.
func verifyDeliveryGrantAs(ctx context.Context, t *testing.T, kctx, ns, as string) {
	t.Helper()
	kube := func(args ...string) error {
		return tryCluster(ctx, "kubectl", append([]string{"--context", kctx, as}, args...)...)
	}
	for _, tc := range []struct {
		verb, res string
		want      bool
	}{
		{"get", "secret/reliant-otel-auth", true},
		{"patch", "secret/reliant-otel-auth", true},
		{"get", "secret/something-else", false},
		{"update", "secret/something-else", false},
		{"list", "secrets", false},
	} {
		err := kube("auth", "can-i", tc.verb, tc.res, "-n", ns)
		if (err == nil) != tc.want {
			t.Errorf("can-i %s %s in %s: allowed=%v, want %v", tc.verb, tc.res, ns, err == nil, tc.want)
		}
	}
	if err := kube("auth", "can-i", "get", "secret/reliant-otel-auth", "-n", "default"); err == nil {
		t.Error("the name-pinned grant leaked into another namespace")
	}

	// Poll until the policy is live: an undeclared create must be refused.
	deadline := time.Now().Add(60 * time.Second)
	for {
		if err := kube("create", "secret", "generic", "undeclared", "-n", ns); err != nil {
			break
		}
		_ = tryCluster(ctx, "kubectl", "--context", kctx, "delete", "secret", "undeclared", "-n", ns)
		if time.Now().After(deadline) {
			t.Fatal("the admission policy never refused a create of an undeclared Secret")
		}
		time.Sleep(2 * time.Second)
	}
	if err := kube("create", "secret", "generic", "reliant-otel-auth", "-n", ns, "--from-literal=token=x"); err != nil {
		t.Errorf("creating the declared Secret was refused: %v", err)
	}
	// Right name, wrong type: a service-account-token Secret would mint a credential.
	_ = tryCluster(ctx, "kubectl", "--context", kctx, "delete", "secret", "reliant-otel-auth", "-n", ns)
	manifest := "apiVersion: v1\nkind: Secret\ntype: kubernetes.io/service-account-token\nmetadata:\n" +
		"  name: reliant-otel-auth\n  namespace: " + ns + "\n  annotations:\n    kubernetes.io/service-account.name: default\n"
	cmd := exec.CommandContext(ctx, "kubectl", "--context", kctx, as, "create", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	if err := cmd.Run(); err == nil {
		t.Error("the declared name was admitted as a service-account-token Secret")
	}
}
