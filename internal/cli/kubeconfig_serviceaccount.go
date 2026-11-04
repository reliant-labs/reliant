// Package cli — minting a POD-USABLE kubeconfig for any cluster forge can
// reach (forge defect F1b).
//
// The sibling file kubeconfig_secret.go mints a kubeconfig by COPYING the
// operator's own credential for the target cluster. That works for k3d,
// whose kubeconfig holds a client certificate usable from anywhere, and it
// does not work at all for a managed cluster. A GKE kubeconfig authenticates
// through an `exec` plugin:
//
//	users:
//	- name: gke_p_us-central1_prod
//	  user:
//	    exec: {command: gke-gcloud-auth-plugin, ...}
//
// which is an instruction to run a binary on the operator's machine using
// the operator's application-default credentials. Copied into a Secret and
// mounted into a pod it is inert — no plugin binary, no credentials, no
// authentication. That is why a hub kubeconfig had to be hand-made
// out-of-band, and it is what this file removes.
//
// The fix is to stop copying and start MINTING. forge converges a
// ServiceAccount, the declared permissions, and a long-lived token Secret ON THE
// TARGET CLUSTER, then assembles a kubeconfig whose only credential is that
// token, inline. Nothing in the result refers to the operator: it is usable
// by any pod holding the Secret, which is the entire requirement.
package cli

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/reliant-labs/forge/internal/cluster"
)

// inClusterAPIServer is the address a pod uses to reach the API server of
// the cluster it is RUNNING IN. Every pod's cluster DNS resolves it and the
// cluster's serving certificate covers it, so a kubeconfig that names it
// needs no endpoint discovery and never goes stale.
const inClusterAPIServer = "https://kubernetes.default.svc"

// forgeManagedLabel marks every object this mint creates. It is also the
// ADOPTION GATE: forge refuses to modify a pre-existing ServiceAccount,
// Role or Secret that does not carry it. A target cluster is shared by
// construction (it is somebody's production cluster), so silently taking
// ownership of an object forge did not create — and then rebinding its
// permissions — is the failure mode that makes minting dangerous rather
// than convenient.
const (
	forgeManagedLabelKey   = "app.kubernetes.io/managed-by"
	forgeManagedLabelValue = "forge"
)

// mintServiceAccountKubeconfig converges the declared ServiceAccount
// credential on the TARGET cluster and projects it as a kubeconfig Secret on
// the CONSUMER cluster.
//
// The two clusters are deliberately separate parameters throughout: the
// Secret lands where the READER runs (k.InCluster), while the SA, its Roles
// and the token live where the credential is HONOURED (the target). They are
// the same cluster for the in-cluster case and different for every other
// one, and conflating them writes permissions into the wrong cluster.
func mintServiceAccountKubeconfig(ctx context.Context, k KubeconfigSecretEntity, consumerNamespace string) error {
	sa := k.ServiceAccount
	targetContext, err := targetKubectlContext(k)
	if err != nil {
		return err
	}
	ns := k.Namespace
	if ns == "" {
		ns = consumerNamespace
	}
	if ns == "" {
		return fmt.Errorf("no namespace: set KubeconfigSecret.namespace or ensure the env declares one")
	}
	key := k.Key
	if key == "" {
		key = "kubeconfig"
	}
	tokenSecret := sa.Name + "-forge-token"

	fmt.Printf("  minting ServiceAccount credential %s/%s on %s (target=%s)\n",
		sa.Namespace, sa.Name, targetContext, k.TargetCluster)

	// 1. The credential's own namespace must exist before the SA does.
	if err := cluster.EnsureNamespace(ctx, targetContext, sa.Namespace); err != nil {
		return fmt.Errorf("ensure namespace %q on target %q: %w", sa.Namespace, targetContext, err)
	}
	// 2. Refuse to adopt anything forge did not create. Checked BEFORE the
	//    apply, so a collision is reported instead of overwritten.
	for _, o := range adoptionTargets(sa, tokenSecret) {
		if err := refuseUnmanagedAdoption(ctx, targetContext, o.kind, o.name, o.namespace); err != nil {
			return err
		}
	}
	// 3. Converge SA + roles + token Secret. Server-side apply, so re-running
	//    a deploy with unchanged rules is a no-op and a changed rule set
	//    converges rather than accumulating.
	if err := cluster.KubectlApply(ctx, targetContext, serviceAccountRBACManifests(sa, tokenSecret)); err != nil {
		return fmt.Errorf("apply ServiceAccount + permissions to target %q: %w", targetContext, err)
	}
	// 4. The token is populated ASYNCHRONOUSLY by the token controller, so
	//    a read immediately after the apply usually finds an empty Secret.
	token, caData, err := readServiceAccountToken(ctx, targetContext, sa.Namespace, tokenSecret)
	if err != nil {
		return err
	}
	// 5. The server the READER dials, which depends on where the reader is.
	server, err := readerServer(ctx, k, targetContext)
	if err != nil {
		return err
	}
	kubeconfig, err := assembleServiceAccountKubeconfig(serviceAccountKubeconfig{
		ContextName: k.ContextName,
		Server:      server,
		CAData:      caData,
		Token:       token,
		Namespace:   sa.Namespace,
	})
	if err != nil {
		return err
	}
	// 6. Project it onto the CONSUMER cluster.
	if err := cluster.EnsureNamespace(ctx, k.InCluster, ns); err != nil {
		return fmt.Errorf("ensure namespace %q in %q: %w", ns, k.InCluster, err)
	}
	fmt.Printf("  applying kubeconfig Secret %s/%s into %s (server=%s, credential=ServiceAccount %s/%s)\n",
		ns, k.Name, k.InCluster, server, sa.Namespace, sa.Name)
	if err := cluster.KubectlApply(ctx, k.InCluster, kubeconfigSecretYAML(k.Name, ns, key, kubeconfig)); err != nil {
		return fmt.Errorf("apply kubeconfig Secret: %w", err)
	}
	return nil
}

// targetKubectlContext resolves the kubectl context addressing the target.
// An explicit target_context wins; otherwise the k3d derivation, which is
// the only cluster naming forge can infer.
func targetKubectlContext(k KubeconfigSecretEntity) (string, error) {
	if c := strings.TrimSpace(k.TargetContext); c != "" {
		return c, nil
	}
	if strings.TrimSpace(k.TargetCluster) == "" {
		return "", fmt.Errorf("KubeconfigSecret %q names no target", k.Name)
	}
	return "k3d-" + k.TargetCluster, nil
}

// readerServer is the API-server address written into the minted
// kubeconfig, chosen by WHERE THE READER SITS — which is what
// `reachability` declares.
//
// "in-cluster": the reader is a pod in the target cluster, so the answer is
// the in-cluster Service address. It is correct on every cluster, needs no
// discovery, and cannot go stale when a control-plane endpoint is rotated —
// which is precisely why a hub kubeconfig wants it.
//
// "endpoint": the reader is elsewhere, so the target's own advertised
// endpoint is the only address that reaches it. Read from the operator's
// kubeconfig for the target context — the address, note, NOT the
// credential; the credential is the minted token either way.
func readerServer(ctx context.Context, k KubeconfigSecretEntity, targetContext string) (string, error) {
	if k.Reachability == "in-cluster" {
		return inClusterAPIServer, nil
	}
	server, err := kubectlContextServer(ctx, targetContext)
	if err != nil {
		return "", err
	}
	return server, nil
}

// kubectlContextServer reads a context's API-server URL out of the
// operator's kubeconfig. Only the ADDRESS is taken: an exec-plugin user
// entry beside it is exactly what this mint exists to avoid copying.
func kubectlContextServer(ctx context.Context, kctx string) (string, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "config", "view", "--minify",
		"--context", kctx, "-o", "jsonpath={.clusters[0].cluster.server}")
	scrubSubprocessLogEnv(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("read API server address for context %q: %w "+
			"(the context must exist in your kubeconfig — forge addresses the target through it)", kctx, err)
	}
	server := strings.TrimSpace(string(out))
	if server == "" {
		return "", fmt.Errorf("context %q has no cluster.server in your kubeconfig", kctx)
	}
	return server, nil
}

// serviceAccountKubeconfig is the assembled kubeconfig's inputs — every
// value that ends up in the file, so the assembly below is pure and
// testable without a cluster.
type serviceAccountKubeconfig struct {
	ContextName string
	Server      string
	CAData      []byte
	Token       string
	Namespace   string
}

// assembleServiceAccountKubeconfig builds the minted kubeconfig.
//
// THE POINT OF THIS FUNCTION is what it does NOT emit: no `exec` block, no
// `tokenFile`, no client certificate, no `insecure-skip-tls-verify`. The
// single credential is an inline bearer token, and the CA is inline
// certificate-authority-data. A pod with this file and nothing else can
// authenticate to the target and verify it.
func assembleServiceAccountKubeconfig(in serviceAccountKubeconfig) ([]byte, error) {
	switch {
	case strings.TrimSpace(in.ContextName) == "":
		return nil, fmt.Errorf("kubeconfig assembly: no context name")
	case strings.TrimSpace(in.Server) == "":
		return nil, fmt.Errorf("kubeconfig assembly: no server address")
	case strings.TrimSpace(in.Token) == "":
		return nil, fmt.Errorf("kubeconfig assembly: no ServiceAccount token")
	case len(in.CAData) == 0:
		// Never degrade to skipping verification. A kubeconfig that cannot
		// verify its API server is worse than no kubeconfig: Flux's
		// kustomize-controller ignores insecure-skip-tls-verify by design,
		// so the failure would surface as an x509 error at apply time
		// rather than here, where it is actionable.
		return nil, fmt.Errorf("kubeconfig assembly: no cluster CA — the minted kubeconfig verifies " +
			"the API server and will not fall back to skipping verification")
	}
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters[in.ContextName] = &clientcmdapi.Cluster{
		Server:                   in.Server,
		CertificateAuthorityData: in.CAData,
	}
	cfg.AuthInfos[in.ContextName] = &clientcmdapi.AuthInfo{Token: in.Token}
	cfg.Contexts[in.ContextName] = &clientcmdapi.Context{
		Cluster:   in.ContextName,
		AuthInfo:  in.ContextName,
		Namespace: in.Namespace,
	}
	cfg.CurrentContext = in.ContextName
	out, err := clientcmd.Write(*cfg)
	if err != nil {
		return nil, fmt.Errorf("write minted kubeconfig: %w", err)
	}
	return out, nil
}

// serviceAccountRBACManifests renders everything forge converges on the
// TARGET cluster: the ServiceAccount, its permissions, and the token Secret.
//
// Pure — no cluster contact — so `--dry-run` prints exactly the objects a
// real deploy would apply, and a test can assert on them.
//
// The token is a `kubernetes.io/service-account-token` Secret, not a
// TokenRequest. See KubeconfigServiceAccount's rotation note in
// kcl/schema.k: a TokenRequest's expiry ties credential validity to deploy
// cadence, so an env nobody deploys for a month stops working. A long-lived
// token has no such deadline, and its blast radius is bounded by `rules`,
// which is the control that actually matters.
func serviceAccountRBACManifests(sa *KubeconfigServiceAccountEntity, tokenSecret string) string {
	var b strings.Builder
	writeDoc := func(s string) {
		if b.Len() > 0 {
			b.WriteString("---\n")
		}
		b.WriteString(s)
	}
	labels := "  labels:\n    " + forgeManagedLabelKey + ": " + forgeManagedLabelValue + "\n"

	writeDoc("apiVersion: v1\nkind: ServiceAccount\nmetadata:\n" +
		"  name: " + sa.Name + "\n  namespace: " + sa.Namespace + "\n" + labels)

	rules := renderPolicyRules(sa.Rules)
	subjects := "subjects:\n- kind: ServiceAccount\n  name: " + sa.Name +
		"\n  namespace: " + sa.Namespace + "\n"

	if len(sa.Namespaces) == 0 {
		// Cluster-wide: one ClusterRole + ClusterRoleBinding.
		name := clusterScopedRBACName(sa)
		writeDoc("apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n" +
			"  name: " + name + "\n" + labels + rules)
		writeDoc("apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRoleBinding\nmetadata:\n" +
			"  name: " + name + "\n" + labels +
			"roleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: ClusterRole\n  name: " + name + "\n" +
			subjects)
	} else {
		// Scoped: the same rules as a Role in each named namespace. The
		// binding's subject is still the SA in ITS namespace — a Role in
		// namespace N may be granted to a ServiceAccount from anywhere.
		for _, target := range sa.Namespaces {
			name := sa.Name
			writeDoc("apiVersion: rbac.authorization.k8s.io/v1\nkind: Role\nmetadata:\n" +
				"  name: " + name + "\n  namespace: " + target + "\n" + labels + rules)
			writeDoc("apiVersion: rbac.authorization.k8s.io/v1\nkind: RoleBinding\nmetadata:\n" +
				"  name: " + name + "\n  namespace: " + target + "\n" + labels +
				"roleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: Role\n  name: " + name + "\n" +
				subjects)
		}
	}

	// The token Secret. `kubernetes.io/service-account.name` is what makes
	// the token controller populate `.data.token` and `.data.ca.crt`.
	writeDoc("apiVersion: v1\nkind: Secret\nmetadata:\n" +
		"  name: " + tokenSecret + "\n  namespace: " + sa.Namespace + "\n" +
		"  annotations:\n    kubernetes.io/service-account.name: " + sa.Name + "\n" +
		labels + "type: kubernetes.io/service-account-token\n")
	return b.String()
}

// clusterScopedRBACName namespaces the ClusterRole name by the SA's
// namespace. ClusterRoles are cluster-scoped, so two envs minting a
// credential called "flux-hub-applier" in different namespaces would
// otherwise fight over one object — each deploy rewriting the other's rules.
func clusterScopedRBACName(sa *KubeconfigServiceAccountEntity) string {
	return "forge-" + sa.Namespace + "-" + sa.Name
}

func renderPolicyRules(rules []KubeconfigPolicyRuleEntity) string {
	var b strings.Builder
	b.WriteString("rules:\n")
	for _, r := range rules {
		groups := r.APIGroups
		if len(groups) == 0 {
			groups = []string{""}
		}
		b.WriteString("- apiGroups: " + yamlStringList(groups) + "\n")
		b.WriteString("  resources: " + yamlStringList(r.Resources) + "\n")
		b.WriteString("  verbs: " + yamlStringList(r.Verbs) + "\n")
		if len(r.ResourceNames) > 0 {
			b.WriteString("  resourceNames: " + yamlStringList(r.ResourceNames) + "\n")
		}
	}
	return b.String()
}

// yamlStringList renders a flow-style list with every element quoted — the
// core API group is the empty string, and `*` is a YAML alias indicator, so
// neither survives unquoted.
func yamlStringList(items []string) string {
	quoted := make([]string, 0, len(items))
	for _, s := range items {
		quoted = append(quoted, `"`+strings.ReplaceAll(s, `"`, `\"`)+`"`)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// adoptionTarget is one object the mint would write, for the pre-apply
// ownership check.
type adoptionTarget struct{ kind, name, namespace string }

func adoptionTargets(sa *KubeconfigServiceAccountEntity, tokenSecret string) []adoptionTarget {
	out := []adoptionTarget{
		{"serviceaccount", sa.Name, sa.Namespace},
		{"secret", tokenSecret, sa.Namespace},
	}
	if len(sa.Namespaces) == 0 {
		name := clusterScopedRBACName(sa)
		out = append(out,
			adoptionTarget{"clusterrole", name, ""},
			adoptionTarget{"clusterrolebinding", name, ""})
	} else {
		for _, ns := range sa.Namespaces {
			out = append(out,
				adoptionTarget{"role", sa.Name, ns},
				adoptionTarget{"rolebinding", sa.Name, ns})
		}
	}
	return out
}

// refuseUnmanagedAdoption fails when the object already exists WITHOUT
// forge's managed-by label. A missing object is fine (forge creates it); an
// object forge already manages is fine (it converges it). Anything else
// belongs to someone else on a shared cluster, and overwriting a
// ServiceAccount's bindings out from under its owner is not a thing a
// deploy may do silently.
func refuseUnmanagedAdoption(ctx context.Context, kctx, kind, name, namespace string) error {
	// jsonpath cannot address a dotted label key without escaping gymnastics,
	// so read the whole label map and match the pair textually.
	args := []string{"get", kind, name, "-o", "jsonpath={.metadata.labels}"}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	cmd := exec.CommandContext(ctx, "kubectl", cluster.KubectlArgs(kctx, args...)...)
	scrubSubprocessLogEnv(cmd)
	out, err := cmd.Output()
	if err != nil {
		// NotFound (and any other read failure) => nothing to adopt. A real
		// connectivity problem surfaces immediately after, on the apply,
		// with a clearer message than this read could give.
		return nil
	}
	labels := string(out)
	if strings.Contains(labels, `"`+forgeManagedLabelKey+`":"`+forgeManagedLabelValue+`"`) {
		return nil
	}
	where := name
	if namespace != "" {
		where = namespace + "/" + name
	}
	return fmt.Errorf(
		"refusing to adopt %s %q on cluster %q: it exists but is not labelled %s=%s, "+
			"so forge did not create it. Minting would rewrite permissions on an object "+
			"someone else owns. Rename the declared ServiceAccount, or delete the existing "+
			"object if it really is a leftover forge should own",
		kind, where, kctx, forgeManagedLabelKey, forgeManagedLabelValue)
}

// readServiceAccountToken reads the token and cluster CA out of the token
// Secret, waiting for the token controller to populate them.
//
// The wait is not optional: a `kubernetes.io/service-account-token` Secret
// is created EMPTY and filled asynchronously, so the read immediately after
// the apply nearly always finds no token on a first deploy. Without the
// wait, minting would fail on creation and succeed only on a second run —
// the kind of flake that gets diagnosed as "just deploy again".
func readServiceAccountToken(ctx context.Context, kctx, namespace, secret string) (token string, caData []byte, err error) {
	const (
		timeout = 60 * time.Second
		every   = time.Second
	)
	deadline := time.Now().Add(timeout)
	for {
		tokenB64 := kubectlSecretField(ctx, kctx, namespace, secret, "token")
		caB64 := kubectlSecretField(ctx, kctx, namespace, secret, `ca\.crt`)
		if tokenB64 != "" && caB64 != "" {
			tok, derr := base64.StdEncoding.DecodeString(tokenB64)
			if derr != nil {
				return "", nil, fmt.Errorf("decode ServiceAccount token: %w", derr)
			}
			ca, derr := base64.StdEncoding.DecodeString(caB64)
			if derr != nil {
				return "", nil, fmt.Errorf("decode cluster CA: %w", derr)
			}
			return string(tok), ca, nil
		}
		if time.Now().After(deadline) {
			return "", nil, fmt.Errorf(
				"ServiceAccount token Secret %s/%s on %q was not populated within %s. "+
					"The token controller fills it asynchronously; an empty Secret this long "+
					"usually means the ServiceAccount named in its annotation does not exist",
				namespace, secret, kctx, timeout)
		}
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case <-time.After(every):
		}
	}
}

// kubectlSecretField reads one base64 field from a Secret, returning "" for
// any failure (not found, not yet populated) so the caller can retry.
func kubectlSecretField(ctx context.Context, kctx, namespace, secret, field string) string {
	cmd := exec.CommandContext(ctx, "kubectl", cluster.KubectlArgs(kctx,
		"get", "secret", secret, "-n", namespace, "-o", "jsonpath={.data."+field+"}")...)
	scrubSubprocessLogEnv(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// describeServiceAccountMint is the --dry-run rendering: every object that
// WOULD be created, on which cluster, and the kubeconfig's shape — without
// contacting any cluster. The token is the one thing it cannot show,
// because it does not exist until a real deploy asks for one.
func describeServiceAccountMint(k KubeconfigSecretEntity, consumerNamespace string) string {
	sa := k.ServiceAccount
	targetContext, err := targetKubectlContext(k)
	if err != nil {
		targetContext = "<unresolved>"
	}
	ns := k.Namespace
	if ns == "" {
		ns = consumerNamespace
	}
	server := inClusterAPIServer
	if k.Reachability != "in-cluster" {
		server = "<the target's endpoint, read from context " + targetContext + ">"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  [dry-run] kubeconfig Secret %s/%s on %s\n", ns, k.Name, k.InCluster)
	fmt.Fprintf(&b, "    credential: ServiceAccount %s/%s minted on %s\n", sa.Namespace, sa.Name, targetContext)
	fmt.Fprintf(&b, "    server:     %s\n", server)
	fmt.Fprintf(&b, "    would apply to %s:\n", targetContext)
	for _, line := range strings.Split(strings.TrimRight(
		serviceAccountRBACManifests(sa, sa.Name+"-forge-token"), "\n"), "\n") {
		b.WriteString("      " + line + "\n")
	}
	return b.String()
}
