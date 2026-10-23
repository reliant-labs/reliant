package cli

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
)

// TestMintServiceAccountKubeconfig_Live exercises the whole mint against a
// REAL cluster: converge the ServiceAccount + RBAC + token Secret, wait for
// the token controller, assemble the kubeconfig, and apply it.
//
// Opt-in via FORGE_LIVE_KUBECONFIG_CONTEXT — it needs a cluster, so it is
// not part of any default lane. Everything it creates lives in a namespace
// it creates and removes; it touches nothing pre-existing, because the
// clusters this runs against are shared.
//
// What only a live run can prove: that the token Secret is actually
// populated by the token controller (the pure tests supply a token), and
// that the minted kubeconfig AUTHENTICATES — exercised by using it to make
// a real API call that the declared rules permit.
func TestMintServiceAccountKubeconfig_Live(t *testing.T) {
	kctx := os.Getenv("FORGE_LIVE_KUBECONFIG_CONTEXT")
	if kctx == "" {
		t.Skip("set FORGE_LIVE_KUBECONFIG_CONTEXT to a kubectl context to run the live mint")
	}
	ns := os.Getenv("FORGE_LIVE_KUBECONFIG_NAMESPACE")
	if ns == "" {
		t.Skip("set FORGE_LIVE_KUBECONFIG_NAMESPACE to a namespace this test may create and delete")
	}

	k := KubeconfigSecretEntity{
		Name:          "live-minted-kubeconfig",
		InCluster:     kctx,
		TargetCluster: "live",
		TargetContext: kctx,
		ContextName:   "live",
		Namespace:     ns,
		Reachability:  "endpoint",
		ServiceAccount: &KubeconfigServiceAccountEntity{
			Name:       "live-applier",
			Namespace:  ns,
			Namespaces: []string{ns},
			Rules: []KubeconfigPolicyRuleEntity{
				{Resources: []string{"configmaps"}, Verbs: []string{"get", "list"}},
			},
		},
	}

	if err := mintServiceAccountKubeconfig(t.Context(), k, ns); err != nil {
		t.Fatalf("live mint: %v", err)
	}

	// Read the minted kubeconfig back out of the Secret it landed in.
	raw := kubectlSecretField(t.Context(), kctx, ns, k.Name, "kubeconfig")
	if raw == "" {
		t.Fatal("the minted Secret carries no kubeconfig")
	}
	decoded := decodeB64(t, raw)

	cfg, err := clientcmd.Load(decoded)
	if err != nil {
		t.Fatalf("load minted kubeconfig: %v", err)
	}
	if cfg.AuthInfos["live"].Token == "" {
		t.Fatal("the token controller never populated a token — the minted kubeconfig has no credential")
	}
	if strings.Contains(string(decoded), "exec:") {
		t.Fatal("the minted kubeconfig carries an exec plugin")
	}

	// The real proof: authenticate with it. `kubectl --kubeconfig <minted>`
	// uses ONLY what is in the file, so a success here means a pod holding
	// this Secret can do the same.
	f, err := os.CreateTemp(t.TempDir(), "minted-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(decoded); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	cmd := exec.CommandContext(t.Context(), "kubectl", "--kubeconfig", f.Name(),
		"get", "configmaps", "-n", ns)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("the minted kubeconfig cannot authenticate + list configmaps: %v: %s", err, stderr.String())
	}

	// And that the rules BOUND it: a verb nobody granted must be denied.
	cmd = exec.CommandContext(t.Context(), "kubectl", "--kubeconfig", f.Name(),
		"get", "secrets", "-n", ns)
	if err := cmd.Run(); err == nil {
		t.Error("the minted credential can read Secrets, which no declared rule grants")
	}
}

func decodeB64(t *testing.T, s string) []byte {
	t.Helper()
	cmd := exec.Command("base64", "-d")
	cmd.Stdin = strings.NewReader(s)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("base64 -d: %v", err)
	}
	return out
}
